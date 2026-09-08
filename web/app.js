const cardsEl = document.getElementById("cards");
const fleetSummaryEl = document.getElementById("fleet-summary");
const lastUpdatedEl = document.getElementById("last-updated");
const pollIntervalEl = document.getElementById("poll-interval");
const rangeToggleEl = document.getElementById("range-toggle");

const POLL_MS = 5000;
const cardMap = new Map();
const historyByKey = new Map();
// Which keys' cached history is the server's recorded series
// (`equity_history`) rather than points this page appended from live
// snapshots. Only a recorded series can anchor a month-to-date figure:
// see baselineEquityAt (bot-strategy#959, PR #36 review).
const recordedHistoryKeys = new Set();
// Benchmark equity, cached per target on the same timestamps as
// historyByKey so drawdown and Calmar compare the bot and its buy & hold
// benchmark over exactly the same window (bot-strategy#955).
const benchmarkByKey = new Map();
// Which benchmark book each cached series belongs to. A later rollout
// can change the anchor under an unchanged target key, and appending the
// new book's values to the old book's series would read as a drawdown
// that never happened (Codex, PR #41).
const benchmarkAnchorByKey = new Map();
const bucketMap = new Map(); // bucket key -> { container, grid }
let hasRendered = false;

// Return-source bucket → group header label. The bucket comes from the
// API (`target.bucket`), resolved server-side against docs/buckets.md,
// itself a mirror of bot-strategy docs/return-source-taxonomy.md §3.
// Cards are grouped by bucket rather than by AWS region because the
// bucket, not the region, decides which benchmark the numbers on the
// card mean anything against (bot-strategy#959). The region stays on
// each card's "AWS Region" row.
const BUCKET_LABELS = {
  alpha_candidate: "α candidate",
  beta: "β — risk premium",
  subsidy: "Subsidy capture",
  unclassified: "Unclassified",
};

// One line per bucket saying what its cards are judged against, so the
// benchmark is visible next to the numbers instead of living only in
// the taxonomy document.
const BUCKET_BENCHMARKS = {
  alpha_candidate:
    "Benchmark: zero after all costs. Judged by its pre-registered gate on the readout date — not by running PnL.",
  beta: "Benchmark: buying the same exposure as spot and holding it.",
  subsidy:
    "Benchmark: cost per unit of subsidy earned. A negative PnL is the price paid, not a loss to fix.",
  unclassified:
    "Not in the return-source taxonomy — add a row to docs/buckets.md. Excluded from every aggregate.",
};

// Stable display order: α candidates first (they are the ones with a
// pending decision), then β, then subsidy, and anything unclassified
// last so a missing taxonomy row is visible at the bottom.
const BUCKET_ORDER = ["alpha_candidate", "beta", "subsidy", "unclassified"];

const RANGE_OPTIONS = [
  { id: "1d", label: "1D", ms: 24 * 60 * 60 * 1000 },
  { id: "1w", label: "1W", ms: 7 * 24 * 60 * 60 * 1000 },
  { id: "1m", label: "1M", ms: 30 * 24 * 60 * 60 * 1000 },
  { id: "all", label: "ALL", ms: null },
];
let currentRange = "1d";

const loadStatus = async (includeHistory = false) => {
  try {
    // History is always fetched for the maximum range available; the
    // range toggle (1D/1W/1M/ALL) only narrows what the chart renders,
    // not what the cache stores. This keeps `computeStats` (CAGR in
    // particular) able to see ≥ MIN_CAGR_DAYS of history regardless of
    // which range button is active. bot-strategy#333.
    const url = includeHistory ? "/api/status?range=all" : "/api/status";
    const response = await fetch(url, { cache: "no-store" });
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`);
    }
    const data = await response.json();
    render(data);
  } catch (err) {
    renderError(err);
  }
};

const render = (data) => {
  if (!data || !Array.isArray(data.targets)) {
    renderError(new Error("No data"));
    return;
  }
  const pollSecs = data.poll_interval_secs || 20;
  const updatedAt = data.updated_at ? new Date(data.updated_at) : null;
  lastUpdatedEl.textContent = updatedAt
    ? `Last refresh ${updatedAt.toLocaleString()}`
    : "Last refresh unknown";
  if (pollIntervalEl) {
    pollIntervalEl.textContent = `Poll ${pollSecs}s`;
  }

  // Group targets by return-source bucket. Each bucket gets its own
  // .bucket-group container with a header + aggregate row + grid; cards
  // are routed into the right grid by `target.bucket`. A target the API
  // reports without a bucket (an older server, or a service missing from
  // docs/buckets.md) falls under "unclassified" and stays out of the
  // aggregates. (bot-strategy#959; supersedes the region grouping from
  // #231 Phase A2 — region is still shown on each card.)
  const seenKeys = new Set();
  const seenBuckets = new Set();
  const cardsByBucket = new Map();

  data.targets.forEach((target, index) => {
    const bucket = bucketOf(target);
    seenBuckets.add(bucket);
    if (!cardsByBucket.has(bucket)) cardsByBucket.set(bucket, []);
    cardsByBucket.get(bucket).push({ target, index });
  });

  // Render each bucket's cards into its grid; create the group on
  // first sight, reuse on subsequent ticks.
  for (const bucket of cardsByBucket.keys()) {
    const group = getOrCreateBucketGroup(bucket);
    const orderedCards = [];
    const items = cardsByBucket.get(bucket);
    items.forEach(({ target, index }) => {
      const key = keyForTarget(target, index);
      let card = cardMap.get(key);
      if (!card) {
        card = createCard(key);
        cardMap.set(key, card);
        group.grid.appendChild(card);
      } else if (card.parentElement !== group.grid) {
        // Re-bucketed target (config change) — move the card.
        group.grid.appendChild(card);
      }
      updateCard(card, target, pollSecs, index, key);
      seenKeys.add(key);
      orderedCards.push(card);
    });
    reconcileOrderInGrid(group.grid, orderedCards);
    const countEl = group.container.querySelector('[data-field="bucket-count"]');
    if (countEl) {
      countEl.textContent = `${items.length} ${items.length === 1 ? "target" : "targets"}`;
    }
    updateBucketAggregate(group, bucket, items);
  }

  // Drop cards whose target disappeared between ticks.
  for (const [key, card] of cardMap.entries()) {
    if (!seenKeys.has(key)) {
      card.remove();
      cardMap.delete(key);
    }
  }

  // Drop bucket groups that no longer have any targets, then reorder
  // the remaining groups according to BUCKET_ORDER.
  for (const [bucket, group] of bucketMap.entries()) {
    if (!seenBuckets.has(bucket)) {
      group.container.remove();
      bucketMap.delete(bucket);
    }
  }
  reconcileBucketOrder();

  // Fleet summary: health counters only. Money aggregates moved into the
  // per-bucket headers — a fleet-wide equity or PnL sum mixes a held
  // asset balance, a subsidy bot's intentional cost and a paper book's
  // reference capital into one meaningless number (bot-strategy#959).
  updateFleetSummary(data.targets);

  if (!hasRendered) {
    hasRendered = true;
    cardsEl.classList.add("live");
  }
};

// The API always sends a bucket (main.go resolves it to "unclassified"
// when the taxonomy doesn't know the service). Treat anything else —
// missing field from an older server, or a value this frontend doesn't
// know — as unclassified rather than inventing a group for it.
const bucketOf = (target) => {
  const bucket = target && typeof target.bucket === "string" ? target.bucket.trim() : "";
  return Object.prototype.hasOwnProperty.call(BUCKET_LABELS, bucket) ? bucket : "unclassified";
};

const getOrCreateBucketGroup = (bucket) => {
  if (bucketMap.has(bucket)) return bucketMap.get(bucket);
  const container = document.createElement("section");
  container.className = "bucket-group";
  container.dataset.bucket = bucket;
  container.innerHTML = `
    <header class="bucket-header">
      <h2 class="bucket-name">${escapeHtml(BUCKET_LABELS[bucket] || bucket)}</h2>
      <span class="bucket-benchmark">${escapeHtml(BUCKET_BENCHMARKS[bucket] || "")}</span>
      <span class="bucket-count" data-field="bucket-count">0 targets</span>
    </header>
    <div class="bucket-aggregate" data-field="bucket-aggregate" hidden></div>
    <div class="grid bucket-grid"></div>
  `;
  cardsEl.appendChild(container);
  const group = {
    container,
    grid: container.querySelector(".grid"),
    bucket,
  };
  bucketMap.set(bucket, group);
  return group;
};

const reconcileBucketOrder = () => {
  const sortedBuckets = Array.from(bucketMap.keys()).sort((a, b) => {
    const ai = BUCKET_ORDER.indexOf(a);
    const bi = BUCKET_ORDER.indexOf(b);
    if (ai === -1 && bi === -1) return a.localeCompare(b);
    if (ai === -1) return 1;
    if (bi === -1) return -1;
    return ai - bi;
  });
  let node = cardsEl.firstElementChild;
  sortedBuckets.forEach((bucket) => {
    const group = bucketMap.get(bucket);
    if (group.container !== node) {
      cardsEl.insertBefore(group.container, node);
    } else {
      node = node.nextElementSibling;
    }
  });
};

// Per-bucket aggregates. Only figures that mean the same thing for every
// card in the bucket belong here; there is deliberately no cross-bucket
// total (bot-strategy#959).
//
// The α candidates' gate progress arrives with bot-strategy#958; until
// then that bucket shows its card count and its benchmark line only,
// rather than a placeholder number.
const bucketAggregateStats = (bucket, items) => {
  if (bucket === "subsidy") return subsidyAggregateStats(items);
  if (bucket !== "beta") return [];
  let equityTotal = 0;
  let equityCount = 0;
  let mtd = 0;
  // Counted, not flagged: a delta that covers only the targets which
  // happen to have a recorded baseline, presented beside an "Equity
  // held" that covers all of them, is a number whose label is wrong
  // (PR #36 review). MTD is shown only when every target contributing
  // equity also contributes a baseline.
  let baselineCount = 0;
  let excess = 0;
  let excessCount = 0;
  const monthStartMs = currentUtcMonthStartMs();
  items.forEach(({ target, index }) => {
    const data = target.status;
    if (!data) return;
    const equity = snapshotEquityValue(data);
    if (equity === null) return;
    equityTotal += equity;
    equityCount += 1;
    // Only targets with a configured, fully priced benchmark contribute;
    // a bot without one is left out of the sum rather than counted as
    // matching its benchmark exactly.
    const benchmark = benchmarkEquityValue(data);
    if (benchmark !== null) {
      excess += equity - benchmark;
      excessCount += 1;
    }
    // Both sides of the delta must come from snapshotEquityValue: the
    // cached baseline is whatever it stored for this target.
    const key = keyForTarget(target, index);
    const baseline = baselineEquityAt(
      historyByKey.get(key),
      monthStartMs,
      recordedHistoryKeys.has(key),
    );
    if (baseline !== null) {
      mtd += equity - baseline;
      baselineCount += 1;
    }
  });
  const mtdAvail = equityCount > 0 && baselineCount === equityCount;
  return [
    {
      label: "Equity held",
      value: equityCount > 0 ? formatUsdc(equityTotal) : "-",
      title:
        "Mark-to-market value of the exposure these bots hold. A β bot's job is to hold it with discipline, so this is a balance, not a profit.",
    },
    {
      label: "MTD change",
      value: mtdAvail ? formatSignedUsdc(mtd) : "-",
      signed: mtdAvail ? mtd : null,
      title:
        "Change in the held value since the most recent UTC month rollover, across every bot in this bucket. Shown only when all of them have a recorded month-start baseline. Meaningful only against the buy & hold benchmark (bot-strategy#955).",
    },
    {
      label: "vs buy & hold",
      value: excessCount > 0 ? formatSignedUsdc(excess) : "-",
      signed: excessCount > 0 ? excess : null,
      title:
        excessCount > 0 && excessCount < equityCount
          ? `Excess over holding the same exposure as spot, across the ${excessCount} of ${equityCount} targets that have a benchmark anchor configured.`
          : "Excess over holding the same exposure as spot, from each target's verified startup anchor (bot-strategy#955).",
    },
  ];
};

// Costs sum across the bucket, but units do not: points and qualifying
// activity notional are different things, so each unit gets its own
// total and its own cost per unit. Only the targets reporting that unit
// contribute to its cost, or the price per point would be inflated by
// another bot's spending.
const subsidyAggregateStats = (items) => {
  let cost = 0;
  let costCount = 0;
  const byUnit = new Map();
  items.forEach(({ target }) => {
    const data = target.status;
    if (!data) return;
    const kpi = target.subsidy_kpi;
    const reported = data.subsidy && Number.isFinite(data.subsidy.cost_total_usd)
      ? Number(data.subsidy.cost_total_usd)
      : null;
    const targetCost = reported === null ? subsidyCostFallback(data) : reported;
    if (targetCost !== null) {
      cost += targetCost;
      costCount += 1;
    }
    if (!kpi || !data.subsidy || !Number.isFinite(data.subsidy.units_total)) return;
    const unit = kpi.unit || "unit";
    // Key on the same normalization the server matches units by
    // (case-insensitive, trimmed), or two targets configured "points"
    // and "Points" would be accepted as the same unit there and split
    // into two uncombinable rows here (Codex, PR #38). The first
    // spelling seen is kept as the label.
    const key = unit.trim().toLowerCase();
    const entry = byUnit.get(key) || { unit, units: 0, cost: 0, costKnown: true };
    entry.units += Number(data.subsidy.units_total);
    if (targetCost === null) {
      entry.costKnown = false;
    } else {
      entry.cost += targetCost;
    }
    byUnit.set(key, entry);
  });
  const stats = [
    {
      label: "Cost paid",
      value: costCount > 0 ? formatUsdc(cost) : "-",
      title:
        "What this bucket has spent in fees, slippage and adverse selection to earn its subsidy. Negative PnL here is the price, not a loss to fix.",
    },
  ];
  for (const entry of byUnit.values()) {
    stats.push({ label: `Units (${entry.unit})`, value: formatUnits(entry.units, entry.unit) });
    stats.push({
      label: `Cost / ${entry.unit}`,
      value: entry.costKnown ? formatCostPerUnit(entry.cost, entry.units, entry.unit) : "-",
    });
  }
  return stats;
};

const updateBucketAggregate = (group, bucket, items) => {
  const el = group.container.querySelector('[data-field="bucket-aggregate"]');
  if (!el) return;
  const stats = bucketAggregateStats(bucket, items);
  if (!stats.length) {
    el.hidden = true;
    el.replaceChildren();
    return;
  }
  el.hidden = false;
  el.replaceChildren();
  stats.forEach((stat) => {
    const wrap = document.createElement("div");
    wrap.className = "bucket-stat";
    if (stat.title) wrap.setAttribute("title", stat.title);
    const label = document.createElement("span");
    label.className = "bucket-stat-label";
    label.textContent = stat.label;
    const value = document.createElement("span");
    value.className = "bucket-stat-value";
    value.textContent = stat.value;
    applySignedClass(value, stat.signed === undefined ? null : stat.signed);
    wrap.appendChild(label);
    wrap.appendChild(value);
    el.appendChild(wrap);
  });
};

// Equity at-or-just-before the given timestamp from a sorted history.
// Returns the latest point with ts < anchorMs (preferred baseline), or
// the earliest point in the array when the bot's history starts after
// the anchor (e.g. bot was provisioned mid-month). Returns null when
// the cache is empty.
const baselineEquityAt = (history, anchorMs, fromRecordedHistory = false) => {
  if (!history || history.length === 0) return null;
  let i = 0;
  while (i < history.length && history[i].ts < anchorMs) i++;
  if (i === 0) {
    // Nothing at or before the anchor: the series begins inside the
    // month. That is a real baseline only when the series is the
    // server's recorded history (a bot provisioned mid-month has no
    // earlier point to offer). When it is points this page appended
    // from live snapshots — every beta target without a server
    // `equity_history`, bull-holder among them — `history[0]` is just
    // "whatever the equity was when this tab opened", which would
    // report an MTD of 0.0 on every reload and then change-since-load
    // (PR #36 review). No baseline is the honest answer; the caller
    // renders "-".
    return fromRecordedHistory ? history[0].equity : null;
  }
  return history[i - 1].equity;
};

// First millisecond of the current UTC month. Matches `pnl_today`'s
// UTC day rollover semantics (see status.rs `update_equity`).
const currentUtcMonthStartMs = () => {
  const now = new Date();
  return Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1);
};

const updateFleetSummary = (targets) => {
  if (!fleetSummaryEl) return;
  if (!targets || targets.length === 0) {
    fleetSummaryEl.hidden = true;
    return;
  }
  // Health counters only. Equity, PnL and return aggregates live in the
  // per-bucket headers: summing a β bot's held asset value, a subsidy
  // bot's deliberate cost and an α candidate's paper equity produces a
  // number no decision can be made from (bot-strategy#959).
  let halts = 0;
  let killSwitches = 0;
  let servicesDown = 0;
  let halts24h = 0;
  // Summed only over targets whose position snapshot is ready so a bot
  // still waiting on its initial WS position sync
  // (`positions_ready: false`) doesn't report as falsely flat.
  let positionsTotal = 0;
  // Kept apart from positionsTotal because that one is halved below.
  let bookPositionsTotal = 0;
  const cutoff24hSec = Math.floor(Date.now() / 1000) - 86400;
  targets.forEach((target) => {
    const data = target.status;
    if (isBullHolderStatus(data) && data.bull_holder.halted === true) halts += 1;
    if (isArcusStatus(data) && data.arcus.risk_halt) halts += 1;
    if (data && !isAccumulatorStatus(data) && !isBullHolderStatus(data) && !isArcusStatus(data)) {
      if (data.positions_ready !== false && typeof data.position_count === "number") {
        // A cross-sectional book holds one position per symbol; only
        // pairtrade's legs come in pairs (see the halving below).
        if (isBookStatus(data)) {
          bookPositionsTotal += data.position_count;
        } else {
          positionsTotal += data.position_count;
        }
      }
      if (data.session_risk && data.session_risk.session_halted === true) halts += 1;
      if (data.daily_risk && data.daily_risk.risk_halted === true) halts += 1;
      if (data.circuit_breaker && data.circuit_breaker.active === true) halts += 1;
      if (isHanBridgeHalted(data)) halts += 1;
      if (isBookHalted(data)) halts += 1;
      if (Array.isArray(data.risk_history)) {
        for (const ev of data.risk_history) {
          if (ev.event_type === "activated" && ev.ts >= cutoff24hSec) {
            halts24h += 1;
          }
        }
      }
    }
    if (target.kill_switch_active === true) killSwitches += 1;
    if (isTargetUnhealthy(target)) servicesDown += 1;
  });
  setField("fleet-total", `${targets.length}`);
  // Each pairtrade position is two legs (e.g. BTC+ETH); halve the raw leg
  // count so this reads as a pair count. A cross-sectional book's legs are
  // single-symbol and are counted whole.
  setField("fleet-positions-total", `${positionsTotal / 2 + bookPositionsTotal}`);
  setField("fleet-halts", `${halts}`, halts > 0 ? "alert" : null);
  setField("fleet-kill-switches", `${killSwitches}`, killSwitches > 0 ? "alert" : null);
  setField("fleet-services-down", `${servicesDown}`, servicesDown > 0 ? "alert" : null);
  setField("fleet-halts-24h", `${halts24h}`, halts24h > 0 ? "alert" : null);
  fleetSummaryEl.hidden = false;
};

const setField = (field, text, severity) => {
  const el = fleetSummaryEl.querySelector(`[data-field="${field}"]`);
  if (!el) return;
  el.textContent = text;
  const stat = el.closest(".fleet-stat");
  if (stat) {
    stat.classList.toggle("alert", severity === "alert");
  }
};

const renderError = (err) => {
  lastUpdatedEl.textContent = "Waiting for data";
  if (pollIntervalEl) {
    pollIntervalEl.textContent = "";
  }
  if (hasRendered) {
    return;
  }
  cardsEl.innerHTML = `
      <div class="card">
        <div class="card-header">
          <h2 class="card-title">Dashboard</h2>
          <span class="status-pill unknown">offline</span>
        </div>
        <div class="error">${escapeHtml(err.message || "Fetch failed")}</div>
      </div>
    `;
};

const keyForTarget = (target, index) => {
  const key = [target.name || "", target.service || "", target.instance_id || ""].join("|");
  return key.replace(/\|+/g, "|").replace(/^\|+|\|+$/g, "") || `target-${index}`;
};

const createCard = (key) => {
  const card = document.createElement("article");
  card.className = "card";
  card.dataset.key = key;
  card.innerHTML = `
      <div class="card-header" data-field="header">
        <button class="card-collapse-toggle" type="button" data-field="collapse-toggle" aria-label="Toggle details" title="Click to collapse / expand">▾</button>
        <h2 class="card-title" data-field="name"></h2>
        <span class="status-pill bucket" data-field="bucket"></span>
        <span class="status-pill" data-field="status"></span>
        <span class="status-pill maintenance" data-field="maintenance" hidden></span>
        <span class="status-pill kpi-stale" data-field="kpi-stale" hidden></span>
        <span class="status-pill errors" data-field="errors" hidden></span>
        <span class="status-pill ws-reset" data-field="ws-reset" hidden></span>
        <span class="status-pill kill-switch" data-field="kill-switch" hidden></span>
        <span class="status-pill session-dd-halt" data-field="session-dd-halt" hidden></span>
        <span class="status-pill daily-dd-halt" data-field="daily-dd-halt" hidden></span>
        <span class="status-pill circuit-breaker" data-field="circuit-breaker" hidden></span>
        <span class="status-pill dry-run" data-field="dry-run" hidden></span>
        <span class="status-pill backtest-mode" data-field="backtest-mode" hidden></span>
      </div>
      <div class="card-body" data-field="body">
      <div class="risk-panel" data-field="risk-panel" hidden>
        <div class="risk-bar" data-field="daily-dd-bar" hidden>
          <div class="risk-bar-label">
            <span class="risk-bar-name">Daily DD</span>
            <span class="risk-bar-value" data-field="daily-dd-text"></span>
          </div>
          <div class="risk-bar-track">
            <div class="risk-bar-fill" data-field="daily-dd-fill"></div>
          </div>
        </div>
        <div class="risk-bar" data-field="session-dd-bar" hidden>
          <div class="risk-bar-label">
            <span class="risk-bar-name">Session DD</span>
            <span class="risk-bar-value" data-field="session-dd-text"></span>
          </div>
          <div class="risk-bar-track">
            <div class="risk-bar-fill" data-field="session-dd-fill"></div>
          </div>
        </div>
        <div class="risk-bar" data-field="circuit-bar" hidden>
          <div class="risk-bar-label">
            <span class="risk-bar-name">Consecutive losses</span>
            <span class="risk-bar-value" data-field="circuit-text"></span>
          </div>
          <div class="risk-bar-track">
            <div class="risk-bar-fill" data-field="circuit-fill"></div>
          </div>
        </div>
        <div class="risk-history" data-field="risk-history" hidden>
          <div class="risk-history-label">
            <span class="risk-history-name">Halt history</span>
            <span class="risk-history-axis">30 d ← now</span>
          </div>
          <div class="risk-history-strip" data-field="risk-history-strip"></div>
        </div>
      </div>
      <div class="row" data-field="dex-row" hidden><span>DEX</span><strong data-field="dex"></strong></div>
      <div class="row"><span>Instance</span><strong data-field="instance"></strong></div>
      <div class="row"><span>AWS Region</span><strong data-field="region"></strong></div>
      <div class="row"><span>Service</span><strong data-field="service"></strong></div>
      <div class="row"><span>Started</span><strong data-field="started"></strong></div>
      <div class="row"><span>Last update</span><strong data-field="age"></strong></div>
      <div class="row shutdown-row" data-field="shutdown-row" hidden><span>Shutdown</span><strong data-field="shutdown-eta"></strong></div>
      <section class="benchmark-panel" data-field="subsidy-panel" hidden aria-label="Subsidy KPI">
        <div class="benchmark-title" title="A subsidy bot buys points or qualifying activity with fees, slippage and adverse selection. Its PnL is the price paid, so it is judged on the price per unit, not on the PnL (bot-strategy#938, taxonomy §4.2).">Cost per unit of subsidy</div>
        <div class="row"><span data-field="subsidy-cpu-7d-label">Cost / unit (7d)</span><strong data-field="subsidy-cpu-7d"></strong></div>
        <div class="row"><span data-field="subsidy-cpu-total-label">Cost / unit (since start)</span><strong data-field="subsidy-cpu-total"></strong></div>
        <div class="row"><span data-field="subsidy-units-label">Units earned</span><strong data-field="subsidy-units"></strong></div>
        <div class="row"><span>Cumulative cost</span><strong data-field="subsidy-cost"></strong></div>
        <div class="row"><span>Imputed value</span><strong data-field="subsidy-value"></strong></div>
        <div class="benchmark-note" data-field="subsidy-note" hidden></div>
      </section>
      <section class="arcus-view" data-field="arcus-view" hidden aria-label="Arcus spot status">
        <div class="equity-headline">
          <div class="equity-headline-label">
            <span>Inventory equity</span>
            <span class="status-pill" data-field="arcus-mode-pill"></span>
          </div>
          <strong data-field="arcus-equity"></strong>
          <div class="equity-headline-meta" data-field="arcus-last-trade"></div>
        </div>
        <div class="chart">
          <div class="chart-title">Inventory equity trend</div>
          <svg class="sparkline" data-field="arcus-chart" viewBox="0 0 100 40" preserveAspectRatio="none"></svg>
          <div class="chart-empty" data-field="arcus-chart-empty" hidden>No history yet</div>
        </div>
        <details class="panel-details" data-field="arcus-details">
          <summary>Details</summary>
          <div data-field="arcus-details-body"></div>
        </details>
      </section>
      <section class="bull-holder-view" data-field="bull-holder-view" hidden aria-label="Bull-holder status">
        <div class="equity-headline">
          <div class="equity-headline-label">
            <span>Total equity</span>
            <span class="status-pill" data-field="holder-mode-pill"></span>
          </div>
          <strong data-field="holder-equity"></strong>
          <div class="equity-headline-meta" data-field="holder-last-trade"></div>
        </div>
        <div class="benchmark-panel" data-field="holder-benchmark">
          <div class="benchmark-title" title="A β bot is judged against buying the same exposure as spot and holding it, not against zero (bot-strategy#954 §4.1).">Buy &amp; hold benchmark</div>
          <div class="row"><span>Excess vs b&amp;h</span><strong data-field="holder-bench-excess"></strong></div>
          <div class="row"><span>Max DD (bot / b&amp;h)</span><strong data-field="holder-bench-dd"></strong></div>
          <div class="row"><span>Calmar (bot / b&amp;h)</span><strong data-field="holder-bench-calmar"></strong></div>
          <div class="row"><span>Funding + fees paid</span><strong data-field="holder-bench-costs"></strong></div>
          <div class="benchmark-note" data-field="holder-bench-note" hidden></div>
        </div>
        <div class="chart">
          <div class="chart-title">Equity trend</div>
          <svg class="sparkline" data-field="holder-chart" viewBox="0 0 100 40" preserveAspectRatio="none"></svg>
          <div class="chart-empty" data-field="holder-chart-empty" hidden>No history yet</div>
        </div>
        <details class="panel-details" data-field="holder-details">
          <summary>Details</summary>
          <div data-field="holder-details-body"></div>
        </details>
      </section>
      <div class="accumulator-view" data-field="accumulator-view" hidden>
        <div class="equity-headline">
          <div class="equity-headline-label"><span>Total equity</span></div>
          <strong data-field="accumulator-total"></strong>
        </div>
        <div class="kv accumulator-balances">
          <div>USDC amount <span data-field="accumulator-usdc"></span></div>
          <div>HYPE amount <span data-field="accumulator-hype"></span></div>
          <div>HYPE mark <span data-field="accumulator-mark"></span></div>
        </div>
        <div class="row" data-field="accumulator-pnl-row" hidden><span>Unrealized PnL</span><strong data-field="accumulator-pnl"></strong></div>
        <div class="row"><span>Last trade</span><strong data-field="accumulator-last-trade"></strong></div>
        <div class="row"><span>Cadence</span><strong data-field="accumulator-cadence"></strong></div>
        <div class="row"><span>Balance observed</span><strong data-field="accumulator-observed"></strong></div>
      </div>
      <div data-field="trading-view">
      <div class="equity-headline">
        <div class="equity-headline-label"><span>Total equity</span></div>
        <strong data-field="pnl-total"></strong>
      </div>
      <div class="kv">
        <div><span data-field="pnl-today-label">PnL today</span> <span data-field="pnl-today"></span></div>
        <div title="Sum of funding_carry_usd across cycles closed today (UTC). Same window as PnL today, so PnL today = price PnL + funding today. From pairtrade since bot-strategy#371; pre-371 binaries render as '-' until restart.">Funding today <span data-field="funding-today"></span></div>
      </div>
      <div class="kv-stats-header" title="Lifetime counters since the bot's risk_state was last reset. The 1D/1W/1M/ALL toggle only filters the equity chart, not these stats.">Stats <small>(lifetime)</small></div>
      <div class="kv kv-stats">
        <div>Max DD <span data-field="max-dd"></span></div>
        <div>Win Rate <span data-field="win-rate"></span></div>
        <div>Trades <span data-field="num-trades"></span></div>
        <div>CAGR <span data-field="cagr"></span></div>
      </div>
      <div class="chart">
        <div class="chart-title">Equity trend</div>
        <svg class="sparkline" data-field="equity-chart" viewBox="0 0 100 40" preserveAspectRatio="none"></svg>
        <div class="chart-empty" data-field="equity-empty" hidden>No history yet</div>
      </div>
      <div class="positions" data-field="positions-list"></div>
      <div class="han-bridge-view" data-field="han-bridge-view" hidden>
        <div class="han-bridge-header">Engine B (Han Bridge)</div>
        <div class="row"><span>Pair</span><strong data-field="han-bridge-pair"></strong></div>
        <div class="row"><span>Today</span><strong class="tone-neutral" data-field="han-bridge-today"></strong></div>
        <div class="row" data-field="han-bridge-reasons-row" hidden><span>Reason</span><strong data-field="han-bridge-reasons"></strong></div>
        <div class="row" data-field="han-bridge-halt-row" hidden><span>Session halt</span><strong data-field="han-bridge-halt"></strong></div>
      </div>
      <div class="han-bridge-view" data-field="book-view" hidden>
        <div class="han-bridge-header" data-field="book-header">Book runtime</div>
        <div class="row"><span>Decision</span><strong class="tone-neutral" data-field="book-decision"></strong></div>
        <div class="row"><span>Signal</span><strong data-field="book-signal"></strong></div>
        <div class="row"><span>Book</span><strong data-field="book-exposure"></strong></div>
        <div class="row"><span>Next decision</span><strong data-field="book-next"></strong></div>
        <div class="row" data-field="book-note-row" hidden><span>Note</span><strong class="tone-warn" data-field="book-note"></strong></div>
      </div>
      </div>
      <div class="error" data-field="error" hidden></div>
      </div>
    `;
  // Collapse-toggle wiring (#231 Phase A3). Click anywhere on the
  // header (or the explicit ▾ button) to flip the .collapsed class.
  // updateCard auto-expands cards in halt state on every tick so a
  // newly-tripping bot pops open without the operator clicking.
  const toggle = card.querySelector('[data-field="collapse-toggle"]');
  if (toggle) {
    toggle.addEventListener("click", (ev) => {
      ev.stopPropagation();
      card.classList.toggle("collapsed");
    });
  }
  return card;
};

const updateCard = (card, target, pollSecs, index, key) => {
  const status = target.service_status || "unknown";
  const data = target.status || {};
  const accumulator = isAccumulatorStatus(data) ? data.accumulator : null;
  const bullHolder = isBullHolderStatus(data) ? data.bull_holder : null;
  const arcus = isArcusStatus(data) ? data.arcus : null;
  const arcusDegraded = arcus !== null && (arcus.healthy !== true || Boolean(arcus.risk_halt));
  const holderDegraded = bullHolder !== null && isBullHolderDegraded(bullHolder);
  const accumulatorDegraded = accumulator !== null && accumulator.healthy !== true;
  // A halted book (session/daily halt, or a venue-equity outage that
  // blocks every opening intent) is degraded like any other stopped bot:
  // without this the card stays green and "active" while the fleet
  // summary already counts it under halts.
  const bookDegraded = isBookHalted(data);
  const displayStatus = status === "active" && accumulator
    ? accumulatorDegraded ? "degraded" : "healthy"
    : status === "active" && (holderDegraded || arcusDegraded || bookDegraded) ? "degraded" : status;
  const statusClass = displayStatus === "healthy" || displayStatus === "active"
    ? "active"
    : displayStatus === "inactive" ? "inactive" : displayStatus === "degraded" ? "degraded" : "unknown";
  const updatedAt = data.updated_at ? new Date(data.updated_at) : null;
  const stale = status === "stale" || isStale(updatedAt, target.stale_after_secs);
  const pnlTodayValue = parseNumber(data.pnl_today);
  // "Equity total" is capital, so it goes through the same book-aware
  // helper the fleet total and the sparkline use: a book's capital lives
  // in book.equity_usd, while its pnl_total is PnL against the reference.
  const pnlTotalValue = snapshotEquityValue(data);
  const pnlToday = formatPnl(pnlTodayValue);
  const pnlTotal = formatUsdc(pnlTotalValue);
  // funding_carry_today is omitted by pre-#371 binaries — distinguish
  // "field missing" (parseNumber returns null → render "-") from "real
  // zero today" (parseNumber returns 0 → render "$0.00") so the card
  // doesn't claim a measurement that hasn't been deployed yet.
  const fundingTodayValue = parseNumber(data.funding_carry_today);
  const fundingToday = fundingTodayValue === null ? "-" : formatPnl(fundingTodayValue);
  const positions = Array.isArray(data.positions) ? data.positions : [];
  const ageText = updatedAt ? `${formatAge(Date.now() - updatedAt.getTime())} ago` : "unknown";
  // Hoisted above the accumulator/bull-holder/arcus branches (which
  // `return` early) so their equity-trend sparklines get the same
  // history cache the generic trading view already builds from
  // repeated snapshots. See snapshotToPoint's bull_holder/arcus
  // fallback below.
  const history = updateHistoryCache(key, data);

  card.classList.toggle("stale", stale);
  card.classList.toggle(
    "degraded",
    accumulatorDegraded || holderDegraded || arcusDegraded || bookDegraded,
  );
  card.classList.toggle("arcus", arcus !== null);
  card.classList.toggle("bull-holder", bullHolder !== null);
  card.style.animationDelay = `${index * 0.04}s`;

  const nameEl = card.querySelector('[data-field="name"]');
  const statusEl = card.querySelector('[data-field="status"]');
  const dexRowEl = card.querySelector('[data-field="dex-row"]');
  const dexEl = card.querySelector('[data-field="dex"]');
  const instanceEl = card.querySelector('[data-field="instance"]');
  const regionEl = card.querySelector('[data-field="region"]');
  const serviceEl = card.querySelector('[data-field="service"]');
  const ageEl = card.querySelector('[data-field="age"]');
  const pnlTodayEl = card.querySelector('[data-field="pnl-today"]');
  const pnlTotalEl = card.querySelector('[data-field="pnl-total"]');
  const fundingTodayEl = card.querySelector('[data-field="funding-today"]');
  const positionsListEl = card.querySelector('[data-field="positions-list"]');
  const errorEl = card.querySelector('[data-field="error"]');
  const chartEl = card.querySelector('[data-field="equity-chart"]');
  const chartEmptyEl = card.querySelector('[data-field="equity-empty"]');

  nameEl.textContent = target.name || target.service || "debot";
  statusEl.textContent = displayStatus;
  statusEl.className = `status-pill ${statusClass}`;

  // Bucket badge: the card sits inside its bucket group already, but the
  // badge keeps the classification attached to the card when it is read
  // on its own (collapsed list, screenshot, narrow screen).
  const bucketEl = card.querySelector('[data-field="bucket"]');
  if (bucketEl) {
    const bucket = bucketOf(target);
    bucketEl.textContent = BUCKET_LABELS[bucket];
    bucketEl.title = BUCKET_BENCHMARKS[bucket];
    bucketEl.className = `status-pill bucket bucket-${bucket}`;
  }

  // Maintenance badge
  const maintEl = card.querySelector('[data-field="maintenance"]');
  if (maintEl) {
    if (data.maintenance) {
      maintEl.hidden = false;
      maintEl.textContent = "maintenance";
    } else {
      maintEl.hidden = true;
      maintEl.textContent = "";
    }
  }

  // Error summary pill: always shown when the bot self-reports an
  // error_summary block so operators can tell "0 errors" from "no data".
  // See bot-strategy#45.
  const errorsEl = card.querySelector('[data-field="errors"]');
  if (errorsEl) {
    const es = data.error_summary;
    if (es) {
      // Prefer the 30m window (bot-strategy#168); fall back to the legacy
      // 5m field for bots that haven't been restarted onto the new code.
      const eWin = es.error_count_30m ?? es.error_count_5m ?? 0;
      const wWin = es.warn_count_30m ?? es.warn_count_5m ?? 0;
      const windowLabel = es.error_count_30m != null || es.warn_count_30m != null ? "30m" : "5m";
      errorsEl.textContent = `${windowLabel}: ${eWin}E/${wWin}W`;
      errorsEl.classList.toggle("has-error", eWin > 0);
      errorsEl.classList.toggle("has-warn", eWin === 0 && wWin > 0);
      errorsEl.title = es.last_error_message
        ? `last error: ${es.last_error_message}\n\ntotals since start: ${es.error_count_total || 0}E / ${es.warn_count_total || 0}W`
        : `totals since start: ${es.error_count_total || 0}E / ${es.warn_count_total || 0}W`;
      errorsEl.hidden = false;
    } else {
      errorsEl.hidden = true;
      errorsEl.textContent = "";
      errorsEl.removeAttribute("title");
      errorsEl.classList.remove("has-error", "has-warn");
    }
  }

  // WS reset pill: count of `Connection reset without closing handshake`
  // events in the service's journalctl log over the last 24h. Threshold
  // 25/day per bot-strategy#47 (raised from 10, see #547) — above that,
  // the pill turns red and the error-watch workflow auto-creates an issue.
  const wsResetEl = card.querySelector('[data-field="ws-reset"]');
  if (wsResetEl) {
    const count = target.ws_reset_24h;
    if (count !== undefined && count !== null) {
      wsResetEl.textContent = `WS: ${count}/24h`;
      wsResetEl.classList.toggle("has-error", count > 25);
      wsResetEl.classList.toggle("has-warn", count > 0 && count <= 25);
      wsResetEl.title = `Connection reset events in last 24h (alert threshold: >25)`;
      wsResetEl.hidden = false;
    } else {
      wsResetEl.hidden = true;
      wsResetEl.textContent = "";
      wsResetEl.removeAttribute("title");
      wsResetEl.classList.remove("has-error", "has-warn");
    }
  }

  // KILL_SWITCH pill: true when /opt/debot/KILL_SWITCH exists on the target
  // instance. Sourced via SSM, independent of bot-side code. See
  // bot-strategy#185. Only visible while the flag is active so the UI stays
  // quiet during normal operation.
  const killSwitchEl = card.querySelector('[data-field="kill-switch"]');
  if (killSwitchEl) {
    if (target.kill_switch_active === true) {
      killSwitchEl.textContent = "KILL SWITCH";
      killSwitchEl.title = "/opt/debot/KILL_SWITCH is present — new entries blocked. Remove with `sudo rm /opt/debot/KILL_SWITCH`.";
      killSwitchEl.hidden = false;
    } else {
      killSwitchEl.hidden = true;
      killSwitchEl.textContent = "";
      killSwitchEl.removeAttribute("title");
    }
  }

  // Risk gate pills (bot-strategy#185 + #231 dashboard redesign):
  // surface daily DD, session DD, and circuit-breaker state in the
  // header so the operator sees halt conditions at a glance, mirroring
  // the KILL_SWITCH pill pattern. Each pill is hidden in steady state.
  // session-dd-halt is the most severe (sticky, requires manual ack) so
  // it goes first in the markup; daily-dd-halt is auto-clearing at UTC
  // midnight; circuit-breaker auto-clears on cooldown.
  const sessionDdEl = card.querySelector('[data-field="session-dd-halt"]');
  if (sessionDdEl) {
    const sr = data.session_risk;
    if (sr && sr.session_halted === true) {
      sessionDdEl.textContent = "SESSION DD";
      const reason = sr.halt_reason ? ` (${sr.halt_reason})` : "";
      sessionDdEl.title =
        `Session DD halt active${reason}: dd_bps=${sr.dd_bps.toFixed(1)} ≥ effective threshold ${sr.effective_max_session_loss_bps.toFixed(0)} bps. ` +
        `Sticky — clear with: sudo touch /opt/debot/RISK_ACK (writing a JSON ack reason inside is recommended for the audit log).`;
      sessionDdEl.hidden = false;
    } else {
      sessionDdEl.hidden = true;
      sessionDdEl.textContent = "";
      sessionDdEl.removeAttribute("title");
    }
  }

  const dailyDdEl = card.querySelector('[data-field="daily-dd-halt"]');
  if (dailyDdEl) {
    const dr = data.daily_risk;
    if (dr && dr.risk_halted === true) {
      dailyDdEl.textContent = "DAILY DD";
      dailyDdEl.title =
        `Daily DD halt active: realized loss ${(-dr.daily_pnl).toFixed(2)} (${(-dr.daily_pnl_bps).toFixed(0)} bps) ≥ effective threshold ${dr.effective_max_daily_loss_bps.toFixed(0)} bps. ` +
        `Auto-clears at next UTC midnight; existing positions exit normally.`;
      dailyDdEl.hidden = false;
    } else {
      dailyDdEl.hidden = true;
      dailyDdEl.textContent = "";
      dailyDdEl.removeAttribute("title");
    }
  }

  const circuitBreakerEl = card.querySelector('[data-field="circuit-breaker"]');
  if (circuitBreakerEl) {
    const cb = data.circuit_breaker;
    if (cb && cb.active === true && cb.cooldown_remaining_secs) {
      const remaining = formatAge(cb.cooldown_remaining_secs * 1000);
      circuitBreakerEl.textContent = `CIRCUIT (${remaining})`;
      circuitBreakerEl.title =
        `Circuit breaker active after ${cb.consecutive_losses} consecutive losses ` +
        `(tier1=${cb.tier1_threshold} / tier2=${cb.tier2_threshold}). ` +
        `Auto-clears in ${remaining}; a winning trade also resets, but new entries are blocked while active.`;
      circuitBreakerEl.hidden = false;
    } else {
      circuitBreakerEl.hidden = true;
      circuitBreakerEl.textContent = "";
      circuitBreakerEl.removeAttribute("title");
    }
  }

  // Risk progress panel (#231 Phase A5). One bar per active risk gate
  // showing observed bps vs effective threshold (or losses vs tier
  // threshold for circuit breaker). Bars colour-grade by % of
  // threshold so a glance at the panel ranks the bot's distance to
  // halt without the operator doing arithmetic. Hidden when all three
  // gates are disabled or unset.
  renderRiskPanel(card, data);

  // Auto-expand on halt (#231 Phase A3). A card with any active halt
  // ignores the operator's previous collapse choice and opens — the
  // operator should always see the halt context. Steady-state cards
  // honour the click-collapse toggle; we don't auto-collapse, only
  // auto-expand-on-trouble.
  const inTrouble =
    target.kill_switch_active === true ||
    (data.session_risk && data.session_risk.session_halted === true) ||
    (data.daily_risk && data.daily_risk.risk_halted === true) ||
    (data.circuit_breaker && data.circuit_breaker.active === true) ||
    isHanBridgeHalted(data) ||
    isBookHalted(data) ||
    (bullHolder && (holderDegraded || status !== "active")) ||
    (arcus && (arcusDegraded || status !== "active"));
  if (inTrouble) {
    card.classList.remove("collapsed");
  }

  // DRY_RUN / BACKTEST mode pills: surface non-live execution modes so an
  // operator glancing at the dashboard cannot mistake a paper-trading or
  // replay bot for a live one. See bot-strategy#215.
  const dryRunEl = card.querySelector('[data-field="dry-run"]');
  if (dryRunEl) {
    if (data.dry_run === true) {
      dryRunEl.textContent = "DRY RUN";
      dryRunEl.title = "Bot is running with DRY_RUN=1 — no real orders are submitted.";
      dryRunEl.hidden = false;
    } else {
      dryRunEl.hidden = true;
      dryRunEl.textContent = "";
      dryRunEl.removeAttribute("title");
    }
  }

  const backtestModeEl = card.querySelector('[data-field="backtest-mode"]');
  if (backtestModeEl) {
    if (data.backtest_mode === true) {
      backtestModeEl.textContent = "BACKTEST";
      backtestModeEl.title = "Bot is running in backtest/replay mode — not connected to live markets.";
      backtestModeEl.hidden = false;
    } else {
      backtestModeEl.hidden = true;
      backtestModeEl.textContent = "";
      backtestModeEl.removeAttribute("title");
    }
  }

  // Graceful-shutdown ETA row: shows the earliest force_close ETA and
  // the grace deadline while the bot is winding down. See pairtrade#6.
  const shutdownRowEl = card.querySelector('[data-field="shutdown-row"]');
  const shutdownEtaEl = card.querySelector('[data-field="shutdown-eta"]');
  if (shutdownRowEl && shutdownEtaEl) {
    if (data.shutdown && data.shutdown.pending) {
      const nowSec = Math.floor(Date.now() / 1000);
      const parts = [];
      if (data.shutdown.force_close_eta_ts) {
        const etaIn = Math.max(0, data.shutdown.force_close_eta_ts - nowSec);
        parts.push(`force_close in ${formatAge(etaIn * 1000)}`);
      }
      if (data.shutdown.grace_deadline_ts) {
        const graceIn = Math.max(0, data.shutdown.grace_deadline_ts - nowSec);
        parts.push(`grace ${formatAge(graceIn * 1000)}`);
      }
      shutdownEtaEl.textContent = parts.length ? parts.join(" · ") : "pending";
      shutdownRowEl.hidden = false;
    } else {
      shutdownRowEl.hidden = true;
      shutdownEtaEl.textContent = "";
    }
  }

  if (dexRowEl && dexEl) {
    if (data.dex) {
      dexRowEl.hidden = false;
      dexEl.textContent = formatDexLabel(data.dex);
    } else {
      dexRowEl.hidden = true;
      dexEl.textContent = "";
    }
  }
  instanceEl.textContent = target.instance_id || "-";
  regionEl.textContent = target.region || "-";
  serviceEl.textContent = target.service || "-";
  const startedEl = card.querySelector('[data-field="started"]');
  if (startedEl) {
    startedEl.textContent = formatStarted(target.service_started_at);
  }
  ageEl.textContent = ageText;
  // Rendered before the per-shape branches below, which return early:
  // the KPI panel is the headline for a subsidy bot whatever shape its
  // status payload has (pairtrade-like for Robinhood, Arcus for Arcus).
  renderSubsidyPanel(card, target, data);
  const accumulatorViewEl = card.querySelector('[data-field="accumulator-view"]');
  const tradingViewEl = card.querySelector('[data-field="trading-view"]');
  const holderViewEl = card.querySelector('[data-field="bull-holder-view"]');
  const arcusViewEl = card.querySelector('[data-field="arcus-view"]');
  if (arcusViewEl) arcusViewEl.hidden = arcus === null;
  if (holderViewEl) holderViewEl.hidden = bullHolder === null;
  if (accumulatorViewEl) accumulatorViewEl.hidden = accumulator === null;
  if (tradingViewEl) tradingViewEl.hidden = accumulator !== null || bullHolder !== null || arcus !== null;
  if (arcus) {
    renderArcusSummary(card, arcus, filterHistoryByRange(history), status);
    renderArcusStatus(card.querySelector('[data-field="arcus-details-body"]'), arcus);
    errorEl.hidden = !target.error;
    errorEl.textContent = target.error || "";
    return;
  }
  if (bullHolder) {
    renderHolderSummary(card, bullHolder, filterHistoryByRange(history), status);
    renderHolderBenchmark(card, bullHolder, pnlTotalValue, history, updateBenchmarkCache(key, data));
    renderBullHolderStatus(card.querySelector('[data-field="holder-details-body"]'), bullHolder, data.dry_run);
    errorEl.hidden = !target.error;
    errorEl.textContent = target.error || "";
    if (target.kill_switch_active === true) {
      killSwitchEl.title = "Bull-holder KILL_SWITCH: new entries and stop replacement blocked; protective exits remain enabled unless halted.";
    }
    return;
  }
  if (accumulator) {
    renderAccumulatorStatus(card, accumulator, data.operations || null);
    if (target.error || accumulatorDegraded) {
      errorEl.hidden = false;
      errorEl.textContent = target.error || accumulator.health_reason || "Accumulator health check failed";
    } else {
      errorEl.hidden = true;
      errorEl.textContent = "";
    }
    return;
  }
  // On a subsidy bot the daily PnL is the day's price paid, not a
  // result to improve; the KPI panel above is what the bot is judged on
  // (bot-strategy#957).
  const pnlTodayLabelEl = card.querySelector('[data-field="pnl-today-label"]');
  if (pnlTodayLabelEl) {
    pnlTodayLabelEl.textContent = bucketOf(target) === "subsidy" ? "Cost today (PnL)" : "PnL today";
  }
  pnlTodayEl.textContent = pnlToday;
  pnlTotalEl.textContent = pnlTotal;
  applySignedClass(pnlTodayEl, pnlTodayValue);
  if (fundingTodayEl) {
    fundingTodayEl.textContent = fundingToday;
    applySignedClass(fundingTodayEl, fundingTodayValue);
  }

  // Chart honours the range toggle; stats (CAGR / fallback Win Rate
  // etc.) intentionally use the full history so they don't disappear
  // when the user is viewing 1D. bot-strategy#333.
  const chartHistory = filterHistoryByRange(history);
  renderEquityChart(chartEl, chartEmptyEl, chartHistory);

  // Stats: prefer BOT-reported trade_stats, fallback to equity-derived
  const maxDdEl = card.querySelector('[data-field="max-dd"]');
  const winRateEl = card.querySelector('[data-field="win-rate"]');
  const numTradesEl = card.querySelector('[data-field="num-trades"]');
  const cagrEl = card.querySelector('[data-field="cagr"]');
  const botStats = data.trade_stats;
  const equityStats = computeStats(history);

  if (botStats) {
    if (maxDdEl) {
      maxDdEl.textContent = formatPnl(-botStats.max_dd);
      applySignedClass(maxDdEl, -botStats.max_dd);
    }
    if (winRateEl) {
      winRateEl.textContent = `${botStats.win_rate.toFixed(0)}%`;
    }
    if (numTradesEl) {
      numTradesEl.textContent = botStats.trades;
    }
    if (cagrEl) {
      // Align CAGR with the bot-reported lifetime window. trade_stats
      // (max_dd / win_rate / trades) reset to 0 on bot restart, but the
      // dashboard's equity history cache survives, so a full-history
      // CAGR keeps reflecting pre-restart performance and reads as
      // "Trades 0 / CAGR +53%". Filter to points after service start so
      // all four stats reset together. bot-strategy#351.
      const sessionStartMs = target.service_started_at
        ? Date.parse(target.service_started_at)
        : NaN;
      const sessionHistory = Number.isFinite(sessionStartMs)
        ? history.filter((p) => p.ts >= sessionStartMs)
        : history;
      const cagr = computeStats(sessionHistory).cagr;
      cagrEl.textContent = cagr !== null ? `${cagr > 0 ? "+" : ""}${cagr.toFixed(0)}%` : "-";
      applySignedClass(cagrEl, cagr);
    }
  } else {
    if (maxDdEl) {
      maxDdEl.textContent = equityStats.maxDd !== null ? formatPnl(-equityStats.maxDd) : "-";
      applySignedClass(maxDdEl, equityStats.maxDd !== null ? -equityStats.maxDd : null);
    }
    if (winRateEl) {
      winRateEl.textContent = equityStats.winRate !== null ? `${equityStats.winRate.toFixed(0)}%` : "-";
    }
    if (numTradesEl) {
      numTradesEl.textContent = equityStats.numTrades !== null ? equityStats.numTrades : "-";
    }
    if (cagrEl) {
      cagrEl.textContent = equityStats.cagr !== null ? `${equityStats.cagr > 0 ? "+" : ""}${equityStats.cagr.toFixed(0)}%` : "-";
      applySignedClass(cagrEl, equityStats.cagr);
    }
  }

  const positionsHtml = positions.length
    ? positions
        .map(
          (pos) => `
          <div class="position">
            <span class="position-tag">${escapeHtml(pos.symbol || "?")}</span>
            <span class="position-side">${escapeHtml(pos.side || "")}</span>
            <span class="position-size">${escapeHtml(formatPositionSize(pos.size))}</span>
          </div>
        `
        )
        .join("")
    : `<div class="empty">No open positions</div>`;
  positionsListEl.innerHTML = positionsHtml;

  const bookViewEl = card.querySelector('[data-field="book-view"]');
  const book = isBookStatus(data) ? data.book : null;
  if (bookViewEl) {
    bookViewEl.hidden = book === null;
    if (book) renderBookStatus(card, book);
  }

  const hanBridgeViewEl = card.querySelector('[data-field="han-bridge-view"]');
  const hanBridge = isHanBridgeStatus(data) ? data.han_bridge : null;
  if (hanBridgeViewEl) {
    hanBridgeViewEl.hidden = hanBridge === null;
    if (hanBridge) {
      renderHanBridgeStatus(card, hanBridge, {
        hasPosition: Boolean(data.has_position),
        killSwitchActive: target.kill_switch_active === true,
      });
    }
  }

  if (target.error) {
    errorEl.hidden = false;
    errorEl.textContent = target.error;
  } else {
    errorEl.hidden = true;
    errorEl.textContent = "";
  }
};

const isAccumulatorStatus = (data) => Boolean(data && data.accumulator);

const holderNumber = (value) => {
  if (value == null || (typeof value !== "number" && typeof value !== "string") || (typeof value === "string" && value.trim() === "")) return null;
  return parseNumber(value);
};
// Money rows share MONEY_DIGITS with formatUsdc/usdCurrency so every
// panel reads at the same precision ("1,301.0 USDC", never
// "1,301.004651 USDC" next to a "2483.8 USDC" neighbour).
const holderMoney = (value) => {
  const n = holderNumber(value);
  return n === null ? "—" : `${groupedFixed(n, MONEY_DIGITS)} USDC`;
};
// Plain USD figure without a unit suffix (rows whose label already says USD).
const holderUsd = (value) => {
  const n = holderNumber(value);
  return n === null ? "—" : groupedFixed(n, MONEY_DIGITS);
};
// Token quantities (BTC/ETH sizes): up to AMOUNT_DIGITS, trailing zeros dropped.
const holderAmount = (value) => {
  const n = holderNumber(value);
  return n === null ? "—" : n.toLocaleString("en-US", { maximumFractionDigits: AMOUNT_DIGITS });
};
const holderTime = (value) => value ? formatDateWithAge(new Date(value * 1000).toISOString()) : "—";
const bullHolderViewModel = (b) => ({
  mode: ({ Off: "Off · awaiting ARM", On: "On · holding / scheduled entries", Exited: "Exited · manual ARM required" })[b.mode] || "State unavailable",
  total: holderMoney(b.total_equity_usdc),
  pending: b.pending == null ? "Unavailable" : Object.entries(b.pending)
    .filter(([name, active]) => active && name !== "KILL_SWITCH")
    .map(([name]) => name === "ADD" && b.pending_add != null ? `ADD × ${b.pending_add}` : name).join(" · ") || "None pending",
  legs: Object.entries(b.legs || {}).sort(([a], [z]) => a.localeCompare(z)).map(([symbol, leg]) => {
    const close = holderNumber(leg.last_close), peak = holderNumber(leg.peak_close);
    const drop = close !== null && close > 0 && peak !== null && peak > 0 ? Math.max(0, 100 * (1 - close / peak)) : null;
    const exit = holderNumber(leg.exit_level);
    return { symbol, ...leg, drop, triggerPct: exit !== null && exit > 0 && peak > 0 ? 100 * (1 - exit / peak) : null };
  }),
});

// Short "Xh ago" form for the summary meta line, as opposed to
// holderTime's full "date · age ago" (too long for a one-line
// headline). `value` is epoch seconds, matching armed_at/exited_at.
const holderAgo = (value) => {
  const n = holderNumber(value);
  return n === null ? null : `${formatAge(Date.now() - n * 1000)} ago`;
};

// "How and when did this bot last actually trade" — the one question
// buried deepest in the old all-text dump. bull_holder has no per-fill
// timestamp, only a daily tranche counter/date and ARM/exit epochs, so
// this is the most specific answer the payload supports.
const holderLastTradeText = (b) => {
  if (b.exited_at) return `Exited ${holderAgo(b.exited_at) || "recently"}`;
  if (b.last_tranche_date) {
    const done = Number.isFinite(b.tranches_done) ? b.tranches_done : "?";
    const remaining = Number.isFinite(b.tranches_remaining) ? b.tranches_remaining : "?";
    return `Last tranche ${b.last_tranche_date} UTC · ${done} done, ${remaining} left`;
  }
  if (b.armed_at) return `Armed ${holderAgo(b.armed_at) || "recently"} · no tranche yet`;
  return "No tranches yet";
};

// Always-visible headline for the bull-holder panel: equity, mode, and
// last-trade timing up top, plus the equity-trend sparkline (reusing
// the same history cache / chart renderer the generic trading view
// uses). The exhaustive per-leg/per-account dump stays in
// renderBullHolderStatus, now tucked behind the <details> this feeds.
// `card` is always a real DOM node here (called only from updateCard),
// unlike renderBullHolderStatus which is also driven directly by tests
// with a minimal mock container — keep DOM APIs beyond
// textContent/className/appendChild out of that function.
const renderHolderSummary = (card, b, chartHistory, serviceStatus) => {
  const equityEl = card.querySelector('[data-field="holder-equity"]');
  const modeEl = card.querySelector('[data-field="holder-mode-pill"]');
  const lastTradeEl = card.querySelector('[data-field="holder-last-trade"]');
  const chartEl = card.querySelector('[data-field="holder-chart"]');
  const chartEmptyEl = card.querySelector('[data-field="holder-chart-empty"]');
  const detailsEl = card.querySelector('[data-field="holder-details"]');
  if (equityEl) equityEl.textContent = holderMoney(b.total_equity_usdc);
  if (modeEl) {
    const label = { Off: "Off", On: "On", Exited: "Exited" }[b.mode] || "Unknown";
    const tone = b.mode === "On" ? "active" : b.mode === "Exited" ? "degraded" : "unknown";
    modeEl.textContent = label;
    modeEl.className = `status-pill ${tone}`;
  }
  if (lastTradeEl) lastTradeEl.textContent = holderLastTradeText(b);
  renderEquityChart(chartEl, chartEmptyEl, chartHistory);
  // isBullHolderDegraded alone misses a stale/hung producer (fetchBullHolder
  // only sets ServiceStatus="stale" from the local status-file age; it
  // doesn't touch halted/operator_error/*.error) — same class of gap Codex
  // flagged for Arcus on PR #32, fixed here too for consistency so a stale
  // bull-holder doesn't hide its own diagnostic rows behind a collapsed
  // <details>. An engaged (or pending) KILL_SWITCH is also "trouble" the
  // outer card already auto-expands for (updateCard's inTrouble checks
  // target.kill_switch_active) — b.kill_switch mirrors that same flag, so
  // check it here too rather than leaving the inner KILL_SWITCH/operator-
  // request rows collapsed while the card itself pops open (Codex review,
  // PR #32).
  const killSwitchEngaged = Boolean(b.kill_switch) || Boolean(b.pending?.KILL_SWITCH);
  if (detailsEl && (isBullHolderDegraded(b) || serviceStatus !== "active" || killSwitchEngaged)) detailsEl.open = true;
};

// Max drawdown as a fraction of the running peak, which is what Calmar
// divides into the annualized return. computeStats reports drawdown in
// currency; the same currency drawdown means something very different on
// a $1,000 book and a $100,000 one.
const maxDrawdownPct = (history) => {
  if (!history || history.length < 2) return null;
  let peak = -Infinity;
  let worst = 0;
  for (const point of history) {
    if (point.equity > peak) peak = point.equity;
    if (peak > 0) {
      const dd = (peak - point.equity) / peak;
      if (dd > worst) worst = dd;
    }
  }
  return worst * 100;
};

// Calmar = annualized return / max drawdown, both in percent. Null
// whenever computeStats withholds CAGR (< 7 days of history, so the
// annualization exponent would explode) or the series never drew down,
// which makes the ratio undefined rather than infinitely good.
const calmarRatio = (history) => {
  const cagr = computeStats(history).cagr;
  const dd = maxDrawdownPct(history);
  if (cagr === null || dd === null || dd <= 0) return null;
  return cagr / dd;
};

const formatRatio = (value) => (value === null ? "-" : value.toFixed(2));

// Four rows, per the β evaluation rule (bot-strategy#954 §4.1): excess
// over buy & hold, the two drawdown-shaped comparisons, and what the
// hedge cost in funding and fees. Anything the dashboard cannot source
// renders "-" — a β bot that looks like it beats buy & hold because a
// leg was silently dropped is the failure this card exists to prevent.
const renderHolderBenchmark = (card, b, botEquity, history, benchmarkHistory) => {
  const panel = card.querySelector('[data-field="holder-benchmark"]');
  if (!panel) return;
  const excessEl = card.querySelector('[data-field="holder-bench-excess"]');
  const ddEl = card.querySelector('[data-field="holder-bench-dd"]');
  const calmarEl = card.querySelector('[data-field="holder-bench-calmar"]');
  const costsEl = card.querySelector('[data-field="holder-bench-costs"]');
  const noteEl = card.querySelector('[data-field="holder-bench-note"]');

  const benchmark = b && b.benchmark ? b.benchmark : null;
  const benchmarkEquity = benchmark && Number.isFinite(benchmark.equity_usd) ? Number(benchmark.equity_usd) : null;
  const equity = Number.isFinite(botEquity) ? Number(botEquity) : null;

  let excessText = "-";
  let excessValue = null;
  if (benchmarkEquity !== null && equity !== null && benchmarkEquity !== 0) {
    excessValue = equity - benchmarkEquity;
    excessText = `${formatSignedUsdc(excessValue)} (${((excessValue / benchmarkEquity) * 100).toFixed(1)}%)`;
  }
  if (excessEl) {
    excessEl.textContent = excessText;
    applySignedClass(excessEl, excessValue);
  }

  // When the current snapshot has no benchmark — a producer config_fp
  // change makes the investment snapshot unverifiable, or a leg goes
  // unpriced — the bot's series keeps growing while the cached benchmark
  // series stops. Comparing them then puts two windows with different
  // ends side by side and shows stale benchmark statistics next to a
  // "benchmark unavailable" note. Suppress the comparison instead of
  // discarding the cached series, so a transient outage costs one tick
  // rather than the whole history (Codex, PR #37).
  // Both windows come from the observations the two series share, so a
  // one-sided outage (Lighter down while Hyperliquid marks still price
  // the benchmark, or the reverse) drops those ticks from both rather
  // than shifting one window against the other.
  const [botSeries, benchmarkSeries] = benchmarkEquity === null
    ? [history, []]
    : pairedSeries(history, benchmarkHistory);
  const botDd = maxDrawdownPct(botSeries);
  const benchDd = maxDrawdownPct(benchmarkSeries);
  if (ddEl) {
    ddEl.textContent =
      botDd === null && benchDd === null
        ? "-"
        : `${botDd === null ? "-" : botDd.toFixed(1) + "%"} / ${benchDd === null ? "-" : benchDd.toFixed(1) + "%"}`;
    ddEl.title =
      "Peak-to-trough drawdown of each series over the window this page has cached. " +
      "The bot's claim is a shallower drawdown for the same exposure, so this is the row that claim lives or dies on.";
  }
  if (calmarEl) {
    const botCalmar = calmarRatio(botSeries);
    const benchCalmar = calmarRatio(benchmarkSeries);
    calmarEl.textContent =
      botCalmar === null && benchCalmar === null
        ? "-"
        : `${formatRatio(botCalmar)} / ${formatRatio(benchCalmar)}`;
    calmarEl.title =
      "Annualized return divided by max drawdown, for the bot and for buy & hold. " +
      "Needs at least 7 days of cached history; until then it reads \"-\".";
  }

  // Funding and fees are producer figures. Partial data is still worth
  // showing (funding alone is the dominant term for a hedged holder), so
  // render whichever side exists and name the missing one.
  const funding = holderNumber(b ? b.cum_funding_usdc : null);
  const fees = holderNumber(b ? b.cum_fees_usdc : null);
  if (costsEl) {
    if (funding === null && fees === null) {
      costsEl.textContent = "-";
      applySignedClass(costsEl, null);
      costsEl.title = "The producer does not report cumulative funding or fees yet.";
    } else {
      const total = (funding || 0) + (fees || 0);
      costsEl.textContent = formatSignedUsdc(total);
      applySignedClass(costsEl, total);
      costsEl.title =
        funding === null
          ? "Fees only — the producer does not report cumulative funding yet."
          : fees === null
            ? "Funding only — the producer does not report cumulative fees yet."
            : "Cumulative funding paid/received plus fees, as reported by the bot.";
    }
  }

  if (noteEl) {
    const note = benchmarkEquity !== null
      ? benchmarkHistory && benchmarkHistory.length >= 2
        ? ""
        : "Drawdown and Calmar start filling in once this page has watched both series for a while."
      : (b && b.benchmark_error) || "Buy & hold benchmark unavailable";
    noteEl.textContent = note;
    noteEl.hidden = note === "";
  }
};

// Cumulative cost when the bot does not (yet) report one. Both fallbacks
// are the bot's own net result read as a price: Arcus values its initial
// basket at current prices, so cumulative_loss_usd is already
// price-neutral, and a pairtrade-shaped bot's lifetime trade_stats.pnl
// is net of the fees and slippage that make up the cost.
const subsidyCostFallback = (data) => {
  if (!data) return null;
  if (data.arcus) {
    return Number.isFinite(data.arcus.cumulative_loss_usd) ? Number(data.arcus.cumulative_loss_usd) : null;
  }
  if (data.trade_stats && Number.isFinite(data.trade_stats.pnl)) {
    return -Number(data.trade_stats.pnl);
  }
  return null;
};

// Cost per unit is a price, often a small one (fractions of a cent per
// point), so it gets its own precision rather than the card's money
// rounding, which would show every value as 0.0.
const costPerUnit = (cost, units) => {
  if (!Number.isFinite(cost) || !Number.isFinite(units) || units === 0) return null;
  return cost / units;
};

const formatCostPerUnit = (cost, units, unit) => {
  const value = costPerUnit(cost, units);
  if (value === null) return "-";
  return `${groupedFixed(value, 4)} USDC / ${unit}`;
};

const formatUnits = (value, unit) =>
  Number.isFinite(value) ? `${groupedFixed(value, 2)} ${unit}` : "-";

// The subsidy KPI panel (bot-strategy#957). The denominator comes from
// the bot's daily ledger (bot-strategy#938) and is absent until that
// lands; the numerator can already be sourced today. Nothing here is
// filled in from the other half: a cost with no units earned renders as
// a cost, never as a cost per unit.
const renderSubsidyPanel = (card, target, data) => {
  const panel = card.querySelector('[data-field="subsidy-panel"]');
  const staleEl = card.querySelector('[data-field="kpi-stale"]');
  const kpi = target ? target.subsidy_kpi : null;
  if (!panel) return;
  if (!kpi) {
    panel.hidden = true;
    if (staleEl) {
      staleEl.hidden = true;
      staleEl.textContent = "";
      staleEl.removeAttribute("title");
    }
    return;
  }
  panel.hidden = false;

  const unit = kpi.unit || "unit";
  const units = data && data.subsidy ? data.subsidy : null;
  const unitsTotal = units && Number.isFinite(units.units_total) ? Number(units.units_total) : null;
  const units7d = units && Number.isFinite(units.units_7d) ? Number(units.units_7d) : null;
  const reportedCost = units && Number.isFinite(units.cost_total_usd) ? Number(units.cost_total_usd) : null;
  const cost7d = units && Number.isFinite(units.cost_7d_usd) ? Number(units.cost_7d_usd) : null;
  const costTotal = reportedCost === null ? subsidyCostFallback(data) : reportedCost;

  const setRow = (field, text, signed) => {
    const el = card.querySelector(`[data-field="${field}"]`);
    if (!el) return;
    el.textContent = text;
    if (signed !== undefined) applySignedClass(el, signed);
  };

  setRow("subsidy-cpu-7d", formatCostPerUnit(cost7d, units7d, unit));
  setRow("subsidy-cpu-total", formatCostPerUnit(costTotal, unitsTotal, unit));
  setRow("subsidy-units", formatUnits(unitsTotal, unit));
  // Cost is money given up, so it is not tinted green when it grows.
  setRow("subsidy-cost", costTotal === null ? "-" : formatUsdc(costTotal));
  const labelEl = card.querySelector('[data-field="subsidy-units-label"]');
  if (labelEl) labelEl.textContent = `Units earned (${unit})`;

  const valueEl = card.querySelector('[data-field="subsidy-value"]');
  if (valueEl) {
    const rate = Number.isFinite(kpi.imputed_unit_value_usd) ? Number(kpi.imputed_unit_value_usd) : null;
    if (rate === null) {
      valueEl.textContent = "-";
      valueEl.title = "No payout assumption is configured. An unpriced subsidy is a legitimate state; the KPI still prices the cost.";
    } else if (unitsTotal === null) {
      valueEl.textContent = "-";
      valueEl.title = `Assumed ${groupedFixed(rate, 4)} USDC per ${unit} as of ${kpi.value_source_date}, but no units are reported yet.`;
    } else {
      valueEl.textContent = `${formatUsdc(unitsTotal * rate)} (as of ${kpi.value_source_date})`;
      valueEl.title = `Units earned × the operator's assumed ${groupedFixed(rate, 4)} USDC per ${unit}, sourced ${kpi.value_source_date}. An assumption, not a payout.`;
    }
  }

  const noteEl = card.querySelector('[data-field="subsidy-note"]');
  if (noteEl) {
    const note =
      unitsTotal === null
        ? "Units are not reported yet — the daily subsidy ledger lands with bot-strategy#938. Cost is shown from the bot's own net result."
        : units7d === null
          ? "The rolling 7-day window comes from the bot's daily ledger and is not being reported yet."
          : "";
    noteEl.textContent = note;
    noteEl.hidden = note === "";
  }

  if (staleEl) {
    if (kpi.stale) {
      staleEl.textContent = `KPI STALE since ${kpi.stale_since}`;
      staleEl.title =
        "The venue changed the subsidy program (points weights, activity rules) more recently than the KPI was re-evaluated. " +
        "Numbers below describe the old program until an operator reviews it and updates kpi_reviewed_on.";
      staleEl.hidden = false;
    } else {
      staleEl.hidden = true;
      staleEl.textContent = "";
      staleEl.removeAttribute("title");
    }
  }
};

const renderBullHolderStatus = (container, b, dryRun) => {
  if (!container) return;
  container.replaceChildren();
  const view = bullHolderViewModel(b);
  const add = (tag, text, parent = container, className = "") => {
    const el = document.createElement(tag);
    el.textContent = text; el.className = className; parent.appendChild(el); return el;
  };
  const row = (label, value, parent = container) => {
    const el = add("div", "", parent, "row"); add("span", label, el); add("strong", value, el); return el;
  };
  add("h3", "Bull-holder");
  row("Mode", view.mode);
  add("h3", "Configured investment · read-only");
  const investment = b.investment;
  row("Configured capital (USD)", holderUsd(investment?.equity_usd));
  row("Hyperliquid spot allocation (USD)", investment ? holderUsd(investment.equity_usd * investment.spot_fraction) : "—");
  row("Lighter perp notional target (USD)", investment ? holderUsd(investment.equity_usd * investment.perp_fraction) : "—");
  add("p", investment ? `Verified startup settings · fingerprint ${investment.config_fp}. These are configured targets, not account balances or remaining purchase amounts. Perp notional is not required margin; ADD can increase the cycle beyond the initial allocation. Editing the dashboard does not change bot settings.` : b.investment_error || "Verified startup investment settings unavailable.", container, "holder-note");
  row("ARM accepted", holderTime(b.armed_at));
  row("Last exit", holderTime(b.exited_at));
  if (b.exit_reason) row("Exit reason", b.exit_reason);
  row("Entries", b.mode ? `${b.tranches_done} done · ${b.tranches_remaining} scheduled` : "—");
  row("Last tranche (UTC)", b.last_tranche_date || "—");
  row("Tranche / symbol", b.armed_at ? `spot ${holderMoney(b.tranche_spot_usd)} · perp ${holderMoney(b.tranche_perp_usd)}` : "Set at ARM");
  row("Operator requests", view.pending);
  add("p", "Pending files are sampled requests, not execution confirmations. Consumed ADD requests are reflected in scheduled entries.", container, "holder-note");
  const killKnown = b.pending != null || ["Off", "On", "Exited"].includes(b.mode);
  row("KILL_SWITCH", !killKnown ? "Unknown · monitoring unavailable" : b.pending?.KILL_SWITCH || b.kill_switch ? "Engaged · entries paused" : "Not engaged");
  row("Risk", b.halted ? `HALTED · ${b.halt_reason || "RISK_ACK required"}` : b.mode ? "No bot halt reported" : "Unknown");
  if (b.halted) add("p", "While halted, automatic exits and DISARM can also be blocked.", container, "holder-warning");
  if (b.operator_error) add("p", b.operator_error, container, "holder-warning");
  add("h3", dryRun ? "Strategy holdings · simulated" : "Strategy holdings · bot state");
  add("p", "Daily-close drop from the highest close since ARM. Either BTC or ETH crossing its exit level closes the whole book. This is not an equity-loss cap.", container, "holder-note");
  if (!view.legs.length) add("p", b.mode === "Off" ? "ARM has not been accepted; no peak or strategy holdings yet." : "No strategy legs reported.", container, "holder-note");
  view.legs.forEach((leg) => {
    const panel = add("div", "", container, "holder-leg");
    add("h4", leg.symbol, panel);
    row("Spot / perp quantity", `${holderAmount(leg.spot_size)} / ${holderAmount(leg.perp_size)}`, panel);
    row("Peak close", holderMoney(leg.peak_close > 0 ? leg.peak_close : null), panel);
    row("Last daily close", `${holderMoney(leg.last_close)} · ${leg.last_close_date || "—"} UTC`, panel);
    row("Drop from peak", leg.drop === null ? "—" : `${leg.drop.toFixed(2)}% · exit > ${leg.triggerPct?.toFixed(2) ?? "—"}%`, panel);
    if (leg.drop !== null && leg.triggerPct > 0) {
      const meter = document.createElement("progress");
      meter.max = leg.triggerPct; meter.value = leg.drop;
      meter.setAttribute("aria-label", `${leg.symbol} daily-close drawdown toward exit`);
      panel.appendChild(meter);
    }
    row("Daily exit price", holderMoney(leg.exit_level > 0 ? leg.exit_level : null), panel);
    row(dryRun ? "Simulated perp stop" : "Perp stop (bot-reported)", `${holderMoney(leg.stop_level)} · qty ${holderAmount(leg.stop_size)}`, panel);
  });
  add("h3", "Actual account assets");
  row("Combined monitored equity", view.total);
  row("Combined unrealized PnL · estimate", holderMoney(b.unrealized_pnl_usdc));
  add("p", "Actual account PnL only, never DRY_RUN simulation. Spot estimate = current value minus venue-reported entry notional; transfers and fees may not be fully reflected. Missing/zero basis for held tokens makes PnL unavailable. PnL is already included in equity; do not add it again.", container, "holder-note");
  add("p", "Hyperliquid spot value + Lighter account equity. Perp notional is exposure, not an asset to add again. Actual balances remain separate from simulated strategy holdings.", container, "holder-note");
  [["Hyperliquid · spot", b.hyperliquid || {}, false], ["Lighter · perpetuals", b.lighter || {}, true]].forEach(([name, account, perp]) => {
    const panel = add("div", "", container, "holder-account");
    add("h4", name, panel);
    if (account.error) add("p", account.error, panel, "holder-warning");
    row(perp ? "Account equity (incl. unrealized PnL)" : "Spot account value", holderMoney(account.equity_usdc), panel);
    row(perp ? "USDC collateral" : "USDC balance", holderMoney(account.usdc), panel);
    row("Available USDC", holderMoney(account.available_usdc), panel);
    row(perp ? "Unrealized PnL" : "Unrealized PnL · estimate", holderMoney(account.unrealized_pnl_usdc), panel);
    row("Balance observed", holderTime(account.observed_at), panel);
    if (!(account.holdings || []).length) add("p", account.observed_at ? (perp ? "No open perp positions." : "No non-USDC spot holdings.") : "Holdings unavailable.", panel, "holder-note");
    (account.holdings || []).forEach((h) => {
      row(h.symbol, `${holderAmount(h.size)} · ${perp ? "notional" : "value"} ${holderMoney(h.value_usdc)}`, panel);
      row("Mark", holderMoney(h.price_usdc), panel);
      row(perp ? "Unrealized PnL" : "Unrealized PnL · estimate", holderMoney(h.unrealized_pnl_usdc), panel);
      if (!perp) row("Venue entry notional", holderMoney(h.cost_basis_usdc), panel);
      if (perp) {
        row("Liquidation price", holderMoney(h.liquidation_price), panel);
      }
    });
  });
};
const isArcusStatus = (data) => Boolean(data && data.arcus);

const usdCurrency = (v) => {
  const n = holderNumber(v);
  return n === null ? "—" : n.toLocaleString("en-US", { style: "currency", currency: "USD", minimumFractionDigits: MONEY_DIGITS, maximumFractionDigits: MONEY_DIGITS });
};

// Compact loss/limit gauge reusing the same risk-bar visual pattern
// (and CSS classes) already used for pairtrade's daily/session DD
// bars, so Arcus's "loss vs limit" figures read as a proximity-to-halt
// gauge instead of a bare "$X / $Y" fraction to compare by eye.
// Deliberately built from createElement/className/appendChild only —
// no classList/setAttribute, and `.style` is feature-detected — so it
// keeps working unchanged inside renderArcusStatus, which unit tests
// also drive directly against a minimal mock container.
const miniBar = (parent, name, valueText, pct) => {
  const wrap = document.createElement("div");
  wrap.className = "risk-bar";
  const label = document.createElement("div");
  label.className = "risk-bar-label";
  const nameEl = document.createElement("span");
  nameEl.className = "risk-bar-name";
  nameEl.textContent = name;
  const valueEl = document.createElement("span");
  valueEl.className = "risk-bar-value";
  valueEl.textContent = valueText;
  label.appendChild(nameEl);
  label.appendChild(valueEl);
  const track = document.createElement("div");
  track.className = "risk-bar-track";
  const fill = document.createElement("div");
  const clamped = clampPct(pct);
  fill.className = `risk-bar-fill ${clamped >= 80 ? "severity-danger" : clamped >= 50 ? "severity-warn" : "severity-ok"}`;
  if (fill.style) fill.style.width = `${clamped}%`;
  track.appendChild(fill);
  wrap.appendChild(label);
  wrap.appendChild(track);
  parent.appendChild(wrap);
  return wrap;
};

// "How and when did this bot last actually trade" — last_swap_at is
// the one field in the payload that answers it directly; everything
// else in the panel is inventory/mode state as of the last poll.
const arcusLastTradeText = (a) => {
  if (a.last_swap_at) {
    const ts = Date.parse(a.last_swap_at);
    if (Number.isFinite(ts)) return `Last swap ${formatAge(Date.now() - ts)} ago`;
  }
  return a.sequence > 0 ? "No swap observed yet" : "Awaiting first tick";
};

// Always-visible headline for the Arcus panel: inventory equity, mode,
// and last-swap timing up top, plus the equity-trend sparkline. Mirrors
// renderHolderSummary — see its comment for why DOM APIs beyond
// textContent/className/appendChild stay out of renderArcusStatus
// itself. `card` is always real DOM here (called only from updateCard).
const renderArcusSummary = (card, a, chartHistory, serviceStatus) => {
  const equityEl = card.querySelector('[data-field="arcus-equity"]');
  const modeEl = card.querySelector('[data-field="arcus-mode-pill"]');
  const lastTradeEl = card.querySelector('[data-field="arcus-last-trade"]');
  const chartEl = card.querySelector('[data-field="arcus-chart"]');
  const chartEmptyEl = card.querySelector('[data-field="arcus-chart-empty"]');
  const detailsEl = card.querySelector('[data-field="arcus-details"]');
  // healthy/risk_halt alone miss a stale/hung exporter: Status.ServiceStatus
  // (arcusstatus/status.go) ages the tick/observation/heartbeat clocks
  // independently of `healthy`, so a target can go "stale" while its last
  // self-reported healthy=true payload sits frozen. Without also checking
  // serviceStatus, details (which carry Last tick/Observation/heartbeat —
  // exactly what's needed to diagnose staleness) would stay collapsed on
  // an otherwise-invisible hang (Codex review, PR #32).
  const degraded = a.healthy !== true || Boolean(a.risk_halt) || serviceStatus !== "active";
  if (equityEl) equityEl.textContent = usdCurrency(a.equity_usd);
  if (modeEl) {
    modeEl.textContent = a.mode || "Unknown";
    modeEl.className = `status-pill ${degraded ? "degraded" : a.mode ? "active" : "unknown"}`;
  }
  if (lastTradeEl) lastTradeEl.textContent = arcusLastTradeText(a);
  renderEquityChart(chartEl, chartEmptyEl, chartHistory);
  if (detailsEl && degraded) detailsEl.open = true;
};

const renderArcusStatus = (root, a) => {
  if (!root) return;
  root.replaceChildren();
  const add = (tag, text, parent = root, className = "") => {
    const node = document.createElement(tag);
    node.textContent = text;
    if (className) node.className = className;
    parent.appendChild(node);
    return node;
  };
  const row = (label, text, parent) => {
    const r = add("div", "", parent, "row");
    add("span", label, r);
    add("strong", text, r);
  };
  const amount = (v, digits = AMOUNT_DIGITS) => {
    const n = holderNumber(v);
    return n === null ? "—" : n.toLocaleString("en-US", { maximumFractionDigits: digits });
  };
  const usd = usdCurrency;
  const at = (v) => v ? formatDateWithAge(v) : "Unknown";
  const yes = (v) => v === true ? "Yes" : v === false ? "No" : "Unknown";
  add("h3", `Arcus spot · ${a.pair || "Unknown pair"}`);
  row("Mode", a.mode || "Unknown");
  row("Last tick", `${a.tick_outcome || "unknown"} · ${at(a.last_tick_at)}`);
  if (a.tick_outcome === "failed") row("Execution result", `${a.service_result || "unknown"} · exit ${a.exit_code ?? "—"}`);
  row("Timer active / enabled", `${yes(a.timer_active)} / ${yes(a.timer_enabled)}`);
  row("Observation", at(a.last_observation_at));
  row("Monitoring heartbeat", at(a.exported_at));
  (a.health_reasons || []).forEach((reason) => add("p", reason, root, "holder-warning"));
  add("h4", "Latest decision");
  row("Action", `${a.decision || "Unknown"}${a.hold_code ? ` · ${a.hold_code}` : ""}${a.decision_pending ? " · pending event commit" : ""}`);
  row("Decision observed", at(a.decision_at));
  row("Signal z", amount(a.z_score, 3));
  row("Plan quote observed", at(a.quote_received_at));
  row("Regime", a.regime || "Unknown");
  row("Rotation started", a.last_rotation_at ? at(a.last_rotation_at) : a.regime === "neutral" ? "No open rotation" : "Unknown");
  if (a.rotated_quantity != null) row("Rotated quantity", amount(a.rotated_quantity));
  add("h4", "Managed inventory");
  (a.inventory || []).forEach((i) => {
    row(i.symbol, `${amount(i.amount)} · ${usd(i.value_usd)}`);
    row(`${i.symbol} reference mark`, usd(i.reference_price_usd));
  });
  row("Inventory equity", usd(a.equity_usd));
  add("h4", "Strategy risk");
  row(`Daily strategy loss · ${a.daily_baseline_day || "unknown day"} UTC`, `${usd(a.daily_loss_usd)} / ${usd(a.daily_loss_limit_usd)} limit`);
  {
    const loss = holderNumber(a.daily_loss_usd);
    const limit = holderNumber(a.daily_loss_limit_usd);
    if (loss !== null && limit !== null && limit > 0) {
      miniBar(root, "Daily loss", `${clampPct((loss / limit) * 100).toFixed(0)}%`, (loss / limit) * 100);
    }
  }
  row("Cumulative strategy loss", `${usd(a.cumulative_loss_usd)} / ${usd(a.cumulative_loss_limit_usd)} limit`);
  {
    const loss = holderNumber(a.cumulative_loss_usd);
    const limit = holderNumber(a.cumulative_loss_limit_usd);
    if (loss !== null && limit !== null && limit > 0) {
      miniBar(root, "Cumulative loss", `${clampPct((loss / limit) * 100).toFixed(0)}%`, (loss / limit) * 100);
    }
  }
  row("Starting basket drawdown", usd(a.inventory_drawdown_usd));
  add("p", "Strategy loss compares managed inventory with the original basket at the same reference prices. Basket drawdown measures price movement separately. These are loss measures, not realized PnL; gas is separate.", root, "holder-note");
  if (a.risk_halt) {
    row("Risk halt", a.risk_halt.kind || "Engaged");
    row("Halt engaged", at(a.risk_halt.engaged_at));
    row("Loss at halt / limit", `${usd(a.risk_halt.loss_usd)} / ${usd(a.risk_halt.limit_usd)}`);
  } else row("Risk halt", a.sequence > 0 ? "Not engaged" : "Unknown");
  add("h4", "Execution & gas");
  row(`Daily execution budget · ${a.budget_day || "unknown day"} UTC`, `${a.daily_budget_used ?? "—"} / ${a.max_swaps_per_day ?? "—"}`);
  add("p", "Budget usage follows the executor's archived-attempt counter, including rejections. It is not a count of filled swaps.", root, "holder-note");
  row("Latest execution phase", a.active_execution_phase || "None / unavailable");
  row("Last reconciled swap", at(a.last_swap_at));
  row("Gas · last reconciled snapshot", a.gas_balance_eth == null ? "Unknown" : `${amount(a.gas_balance_eth, 9)} ETH`);
  row("Gas observed", at(a.gas_observed_at));
};

const isBullHolderStatus = (data) => Boolean(data && data.bull_holder);
const isBullHolderDegraded = (b) => Boolean(b.halted || b.operator_error || b.hyperliquid?.error || b.lighter?.error);

const isHanBridgeStatus = (data) => Boolean(data && data.han_bridge);

// Han Bridge's risk halt is a single string reason on its own nested
// block (session_halt_reason), not the session_risk/daily_risk/
// circuit_breaker shape pairtrade emits -- callers that fold halts into
// a fleet-wide count or an auto-expand condition need this alongside
// those three checks, or a halted Han Bridge target silently drops out
// of both (code-review finding on PR #23).
const isHanBridgeHalted = (data) =>
  Boolean(data && data.han_bridge && data.han_bridge.session_halt_reason);

// Unlike accumulator (which replaces the trading view entirely), Han
// Bridge is a real directional single-symbol strategy with its own
// positions/PnL -- this renders as an *additional* section below the
// normal trading view, not instead of it.
// `day_entered` means "today's entry decision is finalized", NOT "a
// position was opened" -- it is also set true on a below-threshold
// no-signal day (engine_b_live.rs's maybe_enter) and stays false while
// entries are merely blocked by the kill switch or a risk halt (neither
// of which finalizes the day -- the block could lift before the entry
// deadline). `hasPosition`/`killSwitchActive` come from the same
// status.json document's top-level fields, not from han_bridge itself,
// so callers must pass them alongside (code-review findings on PR #23:
// the first cut conflated day_entered with "holding", which mislabeled
// a no-signal day as "Entered, holding"). The halt check itself reads
// hanBridge.session_halt_reason directly rather than taking a
// `sessionHalted` param sourced from `data.session_halted` -- StatusData
// (main.go) has no such top-level field (session halts are nested under
// pairtrade-specific `session_risk.session_halted`, which engine_b_live
// never populates), so that param was always false in practice
// (code-review finding on PR #23, second round).
const hanBridgeViewModel = (hanBridge, { hasPosition = false, killSwitchActive = false } = {}) => {
  const reasons = Array.isArray(hanBridge.ineligible_reasons)
    ? hanBridge.ineligible_reasons
    : [];
  let today;
  if (reasons.length > 0) {
    today = { label: "Skipped (ineligible)", tone: "warn" };
  } else if (hanBridge.day_exited) {
    today = { label: "Entered & exited", tone: "ok" };
  } else if (hasPosition) {
    today = { label: "Entered, holding", tone: "ok" };
  } else if (hanBridge.day_entered) {
    today = { label: "No signal today", tone: "neutral" };
  } else if (killSwitchActive || Boolean(hanBridge.session_halt_reason)) {
    today = { label: "Blocked (halted)", tone: "warn" };
  } else {
    today = { label: "Not decided yet", tone: "neutral" };
  }
  return {
    pair: `${hanBridge.kr_primary_symbol || "?"} → ${hanBridge.us_primary_symbol || "?"}`,
    today,
    reasons,
    sessionHaltReason: hanBridge.session_halt_reason || null,
  };
};

const renderHanBridgeStatus = (card, hanBridge, extra) => {
  const view = hanBridgeViewModel(hanBridge, extra);
  const pairEl = card.querySelector('[data-field="han-bridge-pair"]');
  if (pairEl) pairEl.textContent = view.pair;
  const todayEl = card.querySelector('[data-field="han-bridge-today"]');
  if (todayEl) {
    todayEl.textContent = view.today.label;
    todayEl.classList.remove("tone-ok", "tone-warn", "tone-neutral");
    todayEl.classList.add(`tone-${view.today.tone}`);
  }
  const reasonsRowEl = card.querySelector('[data-field="han-bridge-reasons-row"]');
  const reasonsEl = card.querySelector('[data-field="han-bridge-reasons"]');
  if (reasonsRowEl && reasonsEl) {
    if (view.reasons.length > 0) {
      reasonsRowEl.hidden = false;
      reasonsEl.textContent = view.reasons.join("; ");
    } else {
      reasonsRowEl.hidden = true;
      reasonsEl.textContent = "";
    }
  }
  const haltRowEl = card.querySelector('[data-field="han-bridge-halt-row"]');
  const haltEl = card.querySelector('[data-field="han-bridge-halt"]');
  if (haltRowEl && haltEl) {
    if (view.sessionHaltReason) {
      haltRowEl.hidden = false;
      haltEl.textContent = view.sessionHaltReason;
    } else {
      haltRowEl.hidden = true;
      haltEl.textContent = "";
    }
  }
};

const isBookStatus = (data) => Boolean(data && data.book);

// The book runtime carries two independent halts on its own nested block
// (session and daily), neither of which is the pairtrade session_risk
// shape, plus an equity-read outage that blocks every opening intent.
// Fleet halt counting and card auto-expand need all three or a stopped
// book silently drops out of both.
const isBookHalted = (data) =>
  Boolean(
    data &&
      data.book &&
      (data.book.session_halted || data.book.daily_halted || data.book.equity_ready === false),
  );

// `signal_status` is the runtime's own account of the current window, and
// its prefix is the only reliable classifier: "applied:<sha>" and
// "partial:<sha>" carry a hash, the rest carry a reason. `last_decision`
// is the *previous* completed decision and can disagree with the window
// in progress, so the two are shown separately rather than merged.
const bookViewModel = (book) => {
  const signal = String(book.signal_status || "");
  const [kind, detail = ""] = signal.split(":");
  let tone = "neutral";
  if (kind === "applied") tone = "ok";
  else if (kind === "partial" || kind === "rejected" || kind === "waiting_flatten") tone = "warn";
  const last = book.last_decision || null;
  // The completed decision carries its own hash, which legitimately
  // differs from the window in progress shown in the Signal row (or is
  // absent there entirely, e.g. waiting_for_file). Show it here so the
  // panel can always answer "which signal produced this book".
  const lastSha = last && typeof last.signal_sha256 === "string" ? last.signal_sha256.slice(0, 12) : null;
  // A rejected or skipped decision carries its reason on the record. Once
  // the Signal row has moved on to the next window that reason is only
  // available here, so it belongs in this string rather than being
  // implied by an outcome word.
  const lastReason = last && typeof last.reject_reason === "string" && last.reject_reason
    ? last.reject_reason
    : null;
  const decision = last
    ? `${last.key} ${last.outcome}${lastSha ? ` · ${lastSha}` : ""}${lastReason ? ` · ${lastReason}` : ""}${last.attempts > 1 ? ` (${last.attempts} attempts)` : ""}`
    : "None yet";
  const money = (v) => (typeof v === "number" && Number.isFinite(v) ? usdCurrency(v) : "-");
  const notes = [];
  if (book.session_halted) notes.push(book.session_halt_reason || "session halt");
  if (book.daily_halted) notes.push("daily loss halt");
  if (book.equity_ready === false) notes.push("venue equity unavailable, opens blocked");
  if (book.pending_residual) notes.push("residual pending");
  if (last && last.flatten_at && !last.flatten_done) notes.push(`flatten of ${last.key} pending`);
  return {
    header: `Book runtime${book.instance_id ? ` · ${book.instance_id}` : ""}`,
    decision,
    signal: detail ? `${kind} ${detail}` : kind || "-",
    signalTone: tone,
    exposure: `gross ${money(book.gross_usd)} · net ${money(book.net_usd)} · equity ${money(book.equity_usd)}`,
    next: book.next_decision_at
      ? `${book.next_decision_key || "?"} @ ${book.next_decision_at}`
      : "Not scheduled",
    note: notes.length > 0 ? notes.join("; ") : null,
  };
};

const renderBookStatus = (card, book) => {
  const view = bookViewModel(book);
  const set = (field, text) => {
    const el = card.querySelector(`[data-field="${field}"]`);
    if (el) el.textContent = text;
  };
  set("book-header", view.header);
  set("book-decision", view.decision);
  set("book-exposure", view.exposure);
  set("book-next", view.next);
  const signalEl = card.querySelector('[data-field="book-signal"]');
  if (signalEl) {
    signalEl.textContent = view.signal;
    signalEl.classList.remove("tone-ok", "tone-warn", "tone-neutral");
    signalEl.classList.add(`tone-${view.signalTone}`);
  }
  const noteRowEl = card.querySelector('[data-field="book-note-row"]');
  const noteEl = card.querySelector('[data-field="book-note"]');
  if (noteRowEl && noteEl) {
    noteRowEl.hidden = view.note === null;
    noteEl.textContent = view.note || "";
  }
};

const isTargetUnhealthy = (target) => {
  const serviceUnhealthy = Boolean(
    target.service_status && target.service_status !== "active",
  );
  const accumulator = isAccumulatorStatus(target.status)
    ? target.status.accumulator
    : null;
  return serviceUnhealthy || Boolean(target.error) || (accumulator !== null && accumulator.healthy !== true)
    || (isBullHolderStatus(target.status) && isBullHolderDegraded(target.status.bull_holder))
    || (isArcusStatus(target.status) && (target.status.arcus.healthy !== true || Boolean(target.status.arcus.risk_halt)))
    || isBookHalted(target.status);
};

const formatHype = (value) => {
  const number = parseNumber(value);
  if (number === null) return "-";
  return `${number.toLocaleString("en-US", { maximumFractionDigits: AMOUNT_DIGITS })} HYPE`;
};

const formatDateWithAge = (value, nowMs = Date.now()) => {
  if (!value) return "Never";
  const timestamp = Date.parse(value);
  if (!Number.isFinite(timestamp)) return "-";
  return `${new Date(timestamp).toLocaleString()} · ${formatAge(nowMs - timestamp)} ago`;
};

// `operations` is the bot's identifier-free MetricsSnapshot projection
// (bot-strategy#908), a sibling of `accumulator` in the status document
// and present only once the bot has completed at least one
// `--dry-run-cycle` — the lightweight read-only `hype-status` observer
// never attaches it. `spent_usdc` is the durable ledger's cumulative
// authoritative USDC actually debited to acquire HYPE (fills only), so
// unrealized PnL is simply the current mark value of held HYPE minus that
// figure. `operations` may be null/undefined; the PnL row stays hidden
// in that case rather than rendering a misleading "$0.00".
const accumulatorViewModel = (accumulator, nowMs = Date.now(), operations = null) => {
  const spentUsdc = operations ? parseNumber(operations.spent_usdc) : null;
  const hypeBalance = parseNumber(accumulator.hype_balance);
  const hypePriceUsdc = parseNumber(accumulator.hype_price_usdc);
  const unrealizedPnlUsdc = spentUsdc !== null && hypeBalance !== null && hypePriceUsdc !== null
    ? hypeBalance * hypePriceUsdc - spentUsdc
    : null;
  return {
    total: formatUsdc(parseNumber(accumulator.total_equity_usdc)),
    usdc: formatUsdc(parseNumber(accumulator.usdc_balance)),
    hype: formatHype(accumulator.hype_balance),
    mark: formatUsdc(hypePriceUsdc),
    lastTrade: formatDateWithAge(accumulator.last_trade_at, nowMs),
    cadence: accumulator.trade_cadence || "-",
    observed: formatDateWithAge(accumulator.balance_observed_at, nowMs),
    unrealizedPnlUsdc,
    unrealizedPnl: unrealizedPnlUsdc === null ? null : formatPnl(unrealizedPnlUsdc),
  };
};

const renderAccumulatorStatus = (card, accumulator, operations) => {
  const view = accumulatorViewModel(accumulator, Date.now(), operations);
  const fields = {
    "accumulator-total": view.total,
    "accumulator-usdc": view.usdc,
    "accumulator-hype": view.hype,
    "accumulator-mark": view.mark,
    "accumulator-last-trade": view.lastTrade,
    "accumulator-cadence": view.cadence,
    "accumulator-observed": view.observed,
  };
  Object.entries(fields).forEach(([field, value]) => {
    const element = card.querySelector(`[data-field="${field}"]`);
    if (element) element.textContent = value;
  });
  const pnlRowEl = card.querySelector('[data-field="accumulator-pnl-row"]');
  const pnlEl = card.querySelector('[data-field="accumulator-pnl"]');
  if (pnlRowEl) pnlRowEl.hidden = view.unrealizedPnl === null;
  if (pnlEl) pnlEl.textContent = view.unrealizedPnl || "";
};

const setupRangeToggle = () => {
  if (!rangeToggleEl) {
    return;
  }
  rangeToggleEl.innerHTML = "";
  RANGE_OPTIONS.forEach((option) => {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "range-btn";
    button.textContent = option.label;
    if (option.id === currentRange) {
      button.classList.add("active");
    }
    button.addEventListener("click", () => {
      if (currentRange === option.id) {
        return;
      }
      currentRange = option.id;
      // Cache is range-independent now (full history is always stored,
      // only the chart filter changes), so we don't clear historyByKey
      // and we don't re-fetch with includeHistory=true. A snapshot
      // refresh is enough to trigger a re-render with the new chart
      // window. bot-strategy#333.
      [...rangeToggleEl.querySelectorAll(".range-btn")].forEach((el) =>
        el.classList.toggle("active", el === button)
      );
      loadStatus(false);
    });
    rangeToggleEl.appendChild(button);
  });
};

const updateHistoryCache = (key, data) => {
  // Cache stores the full unfiltered history; range narrowing happens
  // only at chart-render time. This is what allows CAGR (which needs
  // ≥ MIN_CAGR_DAYS of history) to be computed even when the user
  // is viewing the 1D chart. bot-strategy#333.
  let history = historyByKey.get(key) || [];
  if (Array.isArray(data.equity_history)) {
    history = data.equity_history
      .map((point) => ({
        ts: Number(point.ts),
        equity: Number(point.equity),
      }))
      .filter((point) => Number.isFinite(point.ts) && Number.isFinite(point.equity));
    historyByKey.set(key, history);
    // Recorded by the server, so it can anchor a month-to-date figure.
    // Sticky per key: a later poll without the field appends to this
    // same series rather than downgrading it.
    recordedHistoryKeys.add(key);
    return history;
  }
  const point = snapshotToPoint(data);
  if (point) {
    history = appendHistoryPoint(history, point);
    historyByKey.set(key, history);
  }
  return history;
};

// The benchmark series is cached exactly like the equity series (same
// keys, same timestamps) so that a drawdown or Calmar computed from the
// two is comparing the same window. Like the bull-holder equity history,
// this cache only spans the time this page has been open: the producer
// writes no equity_history file for a local target, so a fresh page has
// no past to compare against and the window-dependent rows say so.
// Identity of the benchmark book: which anchor it was bought at and for
// how much. Two series are only comparable when these agree.
const benchmarkAnchorId = (data) => {
  const benchmark = data && data.bull_holder ? data.bull_holder.benchmark : null;
  if (!benchmark) return null;
  // The whole book, not just its timestamp and size: correcting an
  // anchor price or a leg's spot symbol leaves those unchanged while
  // revaluing every point, and appending the new book to the old series
  // renders that step as return and drawdown (Codex, PR #41).
  const assets = Array.isArray(benchmark.assets)
    ? benchmark.assets.map((a) => `${a.symbol}@${a.anchor_price_usd}`).join(",")
    : "";
  return `${benchmark.anchor_ts}|${benchmark.cost_usd}|${benchmark.cash_usd}|${assets}`;
};

// The two series are compared only over the observations they share.
// Anything else -- an outage that stalls one side, a reset that lands
// between this tick's two cache calls -- silently shifts one window
// against the other, and a drawdown computed across that shift is an
// artifact. Intersecting by timestamp makes the alignment a property of
// the comparison rather than something every cache path has to preserve
// (Codex, PR #41).
const pairedSeries = (history, benchmarkHistory) => {
  const byTs = new Map();
  for (const point of benchmarkHistory || []) byTs.set(point.ts, point.equity);
  const bot = [];
  const benchmark = [];
  for (const point of history || []) {
    if (!byTs.has(point.ts)) continue;
    bot.push(point);
    benchmark.push({ ts: point.ts, equity: byTs.get(point.ts) });
  }
  return [bot, benchmark];
};

const updateBenchmarkCache = (key, data) => {
  const anchor = benchmarkAnchorId(data);
  if (anchor !== null && benchmarkAnchorByKey.get(key) !== anchor) {
    // A different book. Both series restart together rather than the
    // benchmark alone, so the drawdown comparison stays aligned and the
    // old book's curve is not attributed to the new one.
    //
    // This includes the first benchmark a page ever sees: equity points
    // cached while the anchor was unconfigured or its marks unavailable
    // reach back before the benchmark series starts, and comparing a
    // long bot window against a short benchmark one overstates the bot's
    // drawdown (Codex, PR #41).
    historyByKey.delete(key);
    recordedHistoryKeys.delete(key);
    benchmarkByKey.delete(key);
    benchmarkAnchorByKey.set(key, anchor);
  }
  const point = snapshotToBenchmarkPoint(data);
  if (!point) {
    return benchmarkByKey.get(key) || [];
  }
  const history = appendHistoryPoint(benchmarkByKey.get(key) || [], point);
  benchmarkByKey.set(key, history);
  return history;
};

// bull_holder / arcus have their own equity fields nested under their
// sub-object rather than the top-level pnl_total pairtrade-style bots
// report (they're asset-value bots, not PnL-cycle bots). Dispatch on
// the sub-object FIRST: StatusData.PnlTotal (main.go) has no `omitempty`
// and is a plain float64, so it always serializes as `pnl_total: 0` —
// including for bull_holder/arcus payloads that never set it — and
// checking it first would silently win with that zero instead of ever
// reaching the real fallback (Codex review, PR #32). Once a target is
// known to be bull_holder/arcus-shaped, its own field is authoritative
// even when unavailable (null rather than falling through to the
// meaningless pnl_total zero for that shape).
const snapshotEquityValue = (data) => {
  if (data.bull_holder) {
    return Number.isFinite(data.bull_holder.total_equity_usdc) ? Number(data.bull_holder.total_equity_usdc) : null;
  }
  if (data.arcus) {
    return Number.isFinite(data.arcus.equity_usd) ? Number(data.arcus.equity_usd) : null;
  }
  // The accumulator reports a reconciled asset balance, not trading PnL.
  // That balance is exactly what the β bucket aggregates (the bot's job
  // is to hold the exposure), and its top-level pnl_total is a constant
  // zero, which would otherwise win here. bot-strategy#959.
  if (data.accumulator) {
    return Number.isFinite(data.accumulator.total_equity_usdc)
      ? Number(data.accumulator.total_equity_usdc)
      : null;
  }
  // The book runtime reports the same shape: its top-level pnl_total is
  // PnL against the equity reference, while book.equity_usd is the
  // capital. Using pnl_total here would understate the fleet total by
  // the whole reference and build the equity chart from PnL.
  if (data.book) {
    return Number.isFinite(data.book.equity_usd) ? Number(data.book.equity_usd) : null;
  }
  return Number.isFinite(data.pnl_total) ? Number(data.pnl_total) : null;
};

const snapshotToPoint = (data) => {
  if (!data) return null;
  const equity = snapshotEquityValue(data);
  if (equity === null) {
    return null;
  }
  const ts = snapshotPointTs(data);
  return ts === null ? null : { ts, equity };
};

// Current value of the buy & hold benchmark the server derived for this
// target (bull_holder only today). Null when the anchor isn't configured
// or a leg couldn't be priced — the card then renders "-" rather than a
// benchmark that silently dropped a leg.
const benchmarkEquityValue = (data) => {
  const benchmark = data && data.bull_holder ? data.bull_holder.benchmark : null;
  return benchmark && Number.isFinite(benchmark.equity_usd) ? Number(benchmark.equity_usd) : null;
};

const snapshotToBenchmarkPoint = (data) => {
  if (!data) return null;
  const equity = benchmarkEquityValue(data);
  if (equity === null) {
    return null;
  }
  const ts = snapshotPointTs(data);
  return ts === null ? null : { ts, equity };
};

const snapshotPointTs = (data) => {
  // bull_holder's total_equity_usdc is recomputed by the dashboard
  // server from live Hyperliquid/Lighter account queries every server
  // poll cycle (fetchBullHolder in bull_holder.go), decoupled from
  // `data.ts` — the bot's own local status-file heartbeat, which can
  // stay unchanged across many server poll cycles.
  //
  // Using `Date.now()` here (an earlier revision of this fix) is also
  // wrong: the frontend's own 5s poll and the server's `/api/status`
  // handler serve `StatusCache.Get()` for ordinary (no `range`) requests
  // — the same cached snapshot answers several client polls between
  // server poll cycles (main.go's pollLoop runs on PollIntervalSecs,
  // independent of client request rate). Stamping every one of those
  // repeated *identical* snapshots with a fresh wall-clock reading would
  // fabricate distinct-looking history points for equity that never
  // actually changed (Codex review, PR #32, round 2).
  //
  // hyperliquid.observed_at / lighter.observed_at are set fresh only
  // when fetchHLSpot/fetchLighterHolder actually run — i.e. once per
  // real server poll cycle, exactly matching how often total_equity_usdc
  // can actually change. Using the later of the two (equity sums both
  // accounts) means: a genuinely new server-side observation always gets
  // a new ts (so the chart accumulates a real trend), while repeated
  // deliveries of the same cached snapshot get the same ts every time
  // (so appendHistoryPoint's same-ts dedup correctly updates in place
  // instead of fabricating a fake new point). Arcus's equity_usd, by
  // contrast, is computed and timestamped atomically by the Arcus bot
  // itself in one write, so its own ts/updated_at stay trustworthy.
  if (data.bull_holder) {
    const hlObserved = holderNumber(data.bull_holder.hyperliquid?.observed_at);
    const ltObserved = holderNumber(data.bull_holder.lighter?.observed_at);
    const observedSec =
      hlObserved !== null && ltObserved !== null
        ? Math.max(hlObserved, ltObserved)
        : hlObserved !== null
          ? hlObserved
          : ltObserved;
    return observedSec !== null ? observedSec * 1000 : Date.now();
  }
  const tsSeconds = Number.isFinite(data.ts) ? Number(data.ts) * 1000 : null;
  const ts =
    tsSeconds ||
    (data.updated_at ? Date.parse(data.updated_at) : null) ||
    Date.now();
  return Number.isFinite(ts) ? ts : null;
};

const appendHistoryPoint = (history, point) => {
  if (!history.length) {
    return [point];
  }
  const last = history[history.length - 1];
  if (point.ts > last.ts) {
    return [...history, point];
  }
  if (point.ts === last.ts) {
    const updated = history.slice();
    updated[updated.length - 1] = point;
    return updated;
  }
  return history;
};

const filterHistoryByRange = (history) => {
  const option = RANGE_OPTIONS.find((opt) => opt.id === currentRange);
  if (!option || !option.ms) {
    return history;
  }
  const cutoff = Date.now() - option.ms;
  return history.filter((point) => point.ts >= cutoff);
};

const computeStats = (history) => {
  const result = { maxDd: null, winRate: null, numTrades: null, cagr: null };
  if (!history || history.length < 2) return result;

  // Max Drawdown from equity curve
  let peak = -Infinity;
  let maxDd = 0;
  for (const point of history) {
    if (point.equity > peak) peak = point.equity;
    const dd = peak - point.equity;
    if (dd > maxDd) maxDd = dd;
  }
  result.maxDd = maxDd;

  // Win Rate & Num Trades: count up/down moves between consecutive points
  let wins = 0;
  let trades = 0;
  for (let i = 1; i < history.length; i++) {
    const delta = history[i].equity - history[i - 1].equity;
    if (Math.abs(delta) > 0.001) {
      trades++;
      if (delta > 0) wins++;
    }
  }
  result.numTrades = trades;
  result.winRate = trades > 0 ? (wins / trades) * 100 : null;

  // CAGR: annualized return from first to last equity. Only compute once
  // we have at least ~7 days of history -- otherwise the 365/daysElapsed
  // exponent blows up any small gain into absurd numbers (e.g. 0.1 day
  // with +1% becomes ~1e15 %) and the card becomes unreadable.
  const first = history[0];
  const last = history[history.length - 1];
  const daysElapsed = (last.ts - first.ts) / (24 * 60 * 60 * 1000);
  const MIN_CAGR_DAYS = 7;
  if (daysElapsed >= MIN_CAGR_DAYS && first.equity > 0) {
    const totalReturn = (last.equity - first.equity) / first.equity;
    const annualizedReturn = Math.pow(1 + totalReturn, 365 / daysElapsed) - 1;
    result.cagr = annualizedReturn * 100;
  }

  return result;
};

const renderEquityChart = (chartEl, emptyEl, history) => {
  if (!chartEl) {
    return;
  }
  if (!history || history.length < 2) {
    chartEl.innerHTML = "";
    if (emptyEl) {
      emptyEl.hidden = false;
    }
    return;
  }
  if (emptyEl) {
    emptyEl.hidden = true;
  }
  const values = history.map((point) => point.equity);
  const min = Math.min(...values);
  const max = Math.max(...values);
  const start = history[0].ts;
  const end = history[history.length - 1].ts;
  const span = Math.max(1, end - start);
  const height = 40;
  const width = 100;
  const range = max - min;
  const pad =
    range === 0
      ? Math.max(Math.abs(max) * 0.02, 0.0001)
      : Math.max(range * 0.08, 0.0001);
  const minPad = min - pad;
  const maxPad = max + pad;
  const paddedRange = maxPad - minPad || 1;
  const xPad = 2.5;
  const points = history
    .map((point) => {
      const x = xPad + ((point.ts - start) / span) * (width - xPad * 2);
      const y = height - ((point.equity - minPad) / paddedRange) * height;
      return `${x.toFixed(2)},${y.toFixed(2)}`;
    })
    .join(" ");
  chartEl.setAttribute("viewBox", `0 0 ${width} ${height}`);
  chartEl.innerHTML = `<polyline points="${points}"></polyline>`;
};

// Risk progress bars (#231 Phase A5). Renders up to 3 bars per card
// covering the daily DD / session DD / circuit-breaker gates. Each
// bar shows a percentage fill driven by observed-vs-threshold and a
// severity class (`ok` < 50%, `warn` 50–80%, `danger` ≥ 80%) for
// colour. Bars are hidden when the underlying gate is disabled
// (effective threshold ≤ 0) so a clean steady-state has no bars at
// all. The whole panel collapses to hidden when no bar is visible —
// keeps disabled-everywhere cards looking the same as before.
const renderRiskPanel = (card, data) => {
  const panel = card.querySelector('[data-field="risk-panel"]');
  if (!panel) return;
  let anyVisible = false;

  // Daily DD bar.
  //
  // Display uses **equity-based** daily DD: `-pnl_today / session_start_equity * 10000`.
  // This is the same accounting basis as the Session DD bar below (peak →
  // current equity drop), so the two bars are directly comparable.
  //
  // The bot's halt logic (`risk_manager.block_reason` → `DailyDdHalted`)
  // continues to use the gross `daily_risk.daily_pnl_bps` field, which is
  // the sum of clean-cycle realised PnL (post-fees but excluding
  // EmergencyFlattening recovery cost — those record_close with 0.0
  // placeholder per xvenue-arb live.rs:677-693). That's intentional: the
  // halt gate asks "is the trading strategy itself bleeding?", while the
  // dashboard bar asks "what has happened to today's capital?". The two
  // diverge most on xvenue-arb during high-EmergencyFlattening days
  // (pairtrade stays close to equity-equal). Displaying the equity view
  // matches the operator's intuition with `PnL today` shown elsewhere on
  // the card. The halt indicator pill (`daily-dd-halt`, set above) still
  // reflects the bot's halt state correctly.
  const dailyBar = card.querySelector('[data-field="daily-dd-bar"]');
  if (dailyBar) {
    const dr = data.daily_risk;
    const eff = dr ? dr.effective_max_daily_loss_bps : 0;
    const startEq = dr ? dr.session_start_equity : 0;
    if (dr && eff > 0 && startEq > 0) {
      const lossBps =
        typeof data.pnl_today === "number" && data.pnl_today < 0
          ? -(data.pnl_today / startEq) * 10000
          : 0;
      const pct = clampPct((lossBps / eff) * 100);
      setRiskBar(card, "daily-dd", `${lossBps.toFixed(0)} / ${eff.toFixed(0)} bps (${pct.toFixed(0)}%)`, pct);
      dailyBar.hidden = false;
      anyVisible = true;
    } else {
      dailyBar.hidden = true;
    }
  }

  // Session DD bar.
  const sessionBar = card.querySelector('[data-field="session-dd-bar"]');
  if (sessionBar) {
    const sr = data.session_risk;
    const eff = sr ? sr.effective_max_session_loss_bps : 0;
    if (sr && eff > 0) {
      const dd = sr.dd_bps;
      const pct = clampPct((dd / eff) * 100);
      setRiskBar(card, "session-dd", `${dd.toFixed(0)} / ${eff.toFixed(0)} bps (${pct.toFixed(0)}%)`, pct);
      sessionBar.hidden = false;
      anyVisible = true;
    } else {
      sessionBar.hidden = true;
    }
  }

  // Circuit-breaker bar (uses tier1 as the "warn" denominator since
  // tier2 is a higher escalation; the fill represents progress toward
  // the next threshold).
  const circuitBar = card.querySelector('[data-field="circuit-bar"]');
  if (circuitBar) {
    const cb = data.circuit_breaker;
    if (cb && cb.tier1_threshold > 0) {
      const denom = cb.tier2_threshold > 0 ? cb.tier2_threshold : cb.tier1_threshold;
      const pct = clampPct((cb.consecutive_losses / denom) * 100);
      let label = `${cb.consecutive_losses} losses (tier1 ${cb.tier1_threshold} / tier2 ${cb.tier2_threshold})`;
      if (cb.active && cb.cooldown_remaining_secs) {
        label += ` · cooldown ${formatAge(cb.cooldown_remaining_secs * 1000)}`;
      }
      setRiskBar(card, "circuit", label, pct);
      circuitBar.hidden = false;
      anyVisible = true;
    } else {
      circuitBar.hidden = true;
    }
  }

  // Halt history strip (#231 Phase B). Renders a 30-d horizontal
  // axis with one colored dot per past halt event from
  // data.risk_history. Non-halt audit events are ignored.
  if (renderRiskHistory(card, data.risk_history)) {
    anyVisible = true;
  }

  panel.hidden = !anyVisible;
};

const RISK_HISTORY_WINDOW_MS = 30 * 24 * 60 * 60 * 1000;
const HALT_HISTORY_EVENT_TYPES = new Set(["activated", "cleared", "ack"]);

const renderRiskHistory = (card, events) => {
  const container = card.querySelector('[data-field="risk-history"]');
  const strip = card.querySelector('[data-field="risk-history-strip"]');
  if (!container || !strip) return false;
  if (!events || events.length === 0) {
    container.hidden = true;
    strip.innerHTML = "";
    return false;
  }
  const nowMs = Date.now();
  const cutoffMs = nowMs - RISK_HISTORY_WINDOW_MS;
  const dots = events
    // risk_history is also an audit stream for non-halt state changes
    // such as capital_rebaseline. The strip is explicitly halt history,
    // so only actual halt lifecycle transitions belong here (#748).
    .filter(
      (ev) =>
        HALT_HISTORY_EVENT_TYPES.has(ev.event_type) &&
        ev.ts * 1000 >= cutoffMs,
    )
    .map((ev) => {
      const tsMs = ev.ts * 1000;
      const ageFrac = (nowMs - tsMs) / RISK_HISTORY_WINDOW_MS; // 0 = now, 1 = 30d ago
      const leftPct = (1 - ageFrac) * 100;
      const kindClass = `kind-${ev.kind.replace(/_/g, "-")}`;
      const eventClass = `event-${ev.event_type}`;
      const tooltipParts = [
        new Date(tsMs).toLocaleString(),
        `${ev.kind} ${ev.event_type}`,
      ];
      if (ev.reason) tooltipParts.push(`reason: ${ev.reason}`);
      if (ev.detail) {
        const detailEntries = Object.entries(ev.detail)
          .map(([k, v]) => `${k}=${typeof v === "number" ? v.toFixed(2) : v}`)
          .join(", ");
        if (detailEntries) tooltipParts.push(detailEntries);
      }
      const tooltip = tooltipParts.join("\n");
      return `<span class="risk-history-dot ${kindClass} ${eventClass}" style="left:${leftPct.toFixed(2)}%" title="${escapeHtml(tooltip)}"></span>`;
    })
    .join("");
  if (!dots) {
    container.hidden = true;
    strip.innerHTML = "";
    return false;
  }
  strip.innerHTML = dots;
  container.hidden = false;
  return true;
};

const setRiskBar = (card, prefix, text, pct) => {
  const textEl = card.querySelector(`[data-field="${prefix}-text"]`);
  const fillEl = card.querySelector(`[data-field="${prefix}-fill"]`);
  if (textEl) textEl.textContent = text;
  if (fillEl) {
    fillEl.style.width = `${pct}%`;
    fillEl.classList.remove("severity-ok", "severity-warn", "severity-danger");
    let severity = "severity-ok";
    if (pct >= 80) severity = "severity-danger";
    else if (pct >= 50) severity = "severity-warn";
    fillEl.classList.add(severity);
  }
};

const clampPct = (pct) => {
  if (!Number.isFinite(pct) || pct < 0) return 0;
  if (pct > 100) return 100;
  return pct;
};

// Reorder cards within a single region's grid to match `orderedCards`.
// (Replaces the pre-#231 reconcileOrder which assumed cards lived
// directly under #cards.)
const reconcileOrderInGrid = (gridEl, orderedCards) => {
  let node = gridEl.firstElementChild;
  orderedCards.forEach((card) => {
    if (card !== node) {
      gridEl.insertBefore(card, node);
    } else {
      node = node.nextElementSibling;
    }
  });
};

// Bots self-report a bare lowercase venue slug (e.g. "lighter",
// "hyperliquid") — capitalize those for display. A config.yaml `dex`
// override is already a display-ready label and may have intentional
// casing (e.g. "zkLighter", "dYdX"), so leave anything that isn't fully
// lowercase untouched rather than mangling it.
const formatDexLabel = (dex) =>
  dex === dex.toLowerCase() ? dex.charAt(0).toUpperCase() + dex.slice(1) : dex;

// Display precision shared by every money figure on the page (fleet
// total, card headlines, holder/Arcus/book detail rows) and by token
// quantities. Keep these two in one place so panels never drift apart
// again (a "1,301.004651 USDC" headline next to "2483.8 USDC").
const MONEY_DIGITS = 1;
const AMOUNT_DIGITS = 4;

// Fixed decimals with thousands separators: groupedFixed(1301.004651, 1) → "1,301.0".
// `+ 0` folds -0 (e.g. a negated zero max_dd) into 0 so it never renders "-0.0".
const groupedFixed = (number, digits) =>
  (number + 0).toLocaleString("en-US", { minimumFractionDigits: digits, maximumFractionDigits: digits });

const formatPnl = (value) => {
  if (value === undefined || value === null || Number.isNaN(value)) {
    return "-";
  }
  const number = Number(value);
  const sign = number > 0 ? "+" : "";
  return `${sign}${groupedFixed(number, MONEY_DIGITS)}`;
};

const formatNumber = (value) => {
  if (value === undefined || value === null || Number.isNaN(value)) {
    return "-";
  }
  return groupedFixed(Number(value), MONEY_DIGITS);
};

// Signed money with its unit: "+100.0 USDC". formatPnl deliberately
// omits the unit for the compact key/value stats, but a benchmark
// comparison is read next to plain equity figures and must not be
// mistakable for a percentage or a count.
const formatSignedUsdc = (value) => {
  if (value === undefined || value === null || Number.isNaN(value)) {
    return "-";
  }
  const number = Number(value);
  const sign = number > 0 ? "+" : "";
  return `${sign}${groupedFixed(number, MONEY_DIGITS)} USDC`;
};
const formatUsdc = (value) => {
  if (value === undefined || value === null || Number.isNaN(value)) {
    return "-";
  }
  return `${groupedFixed(Number(value), MONEY_DIGITS)} USDC`;
};

const parseNumber = (value) => {
  const number = Number(value);
  return Number.isFinite(number) ? number : null;
};

const applySignedClass = (el, value) => {
  if (!el) {
    return;
  }
  el.classList.remove("positive", "negative");
  if (!Number.isFinite(value)) {
    return;
  }
  if (value > 0) {
    el.classList.add("positive");
  } else if (value < 0) {
    el.classList.add("negative");
  }
};

const formatStarted = (iso) => {
  if (!iso) {
    return "-";
  }
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) {
    return "-";
  }
  const ageMs = Date.now() - date.getTime();
  return ageMs >= 0 ? `${formatAge(ageMs)} ago` : "-";
};

const formatAge = (ms) => {
  const secs = Math.max(0, Math.floor(ms / 1000));
  if (secs < 60) {
    return `${secs}s`;
  }
  const mins = Math.floor(secs / 60);
  if (mins < 60) {
    return `${mins}m`;
  }
  const hours = Math.floor(mins / 60);
  const remMins = mins - hours * 60;
  return remMins === 0 ? `${hours}h` : `${hours}h${remMins}m`;
};

const isStale = (updatedAt, staleAfterSecs = 180) => {
  if (!updatedAt) {
    return true;
  }
  const diffSecs = (Date.now() - updatedAt.getTime()) / 1000;
  const limit = Number.isFinite(staleAfterSecs) && staleAfterSecs > 0 ? staleAfterSecs : 180;
  return !Number.isFinite(diffSecs) || diffSecs < -30 || diffSecs > limit;
};

const formatPositionSize = (value) => {
  if (value === null || value === undefined || value === "") return "";
  const num = Number(value);
  if (!Number.isFinite(num)) return String(value);
  return num.toFixed(4);
};

const escapeHtml = (value) => {
  return String(value)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/\"/g, "&quot;")
    .replace(/'/g, "&#39;");
};

setupRangeToggle();
loadStatus(true);
setInterval(() => loadStatus(false), POLL_MS);
setInterval(loadStatus, POLL_MS);
