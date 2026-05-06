const cardsEl = document.getElementById("cards");
const fleetSummaryEl = document.getElementById("fleet-summary");
const lastUpdatedEl = document.getElementById("last-updated");
const pollIntervalEl = document.getElementById("poll-interval");
const rangeToggleEl = document.getElementById("range-toggle");

const POLL_MS = 5000;
const cardMap = new Map();
const historyByKey = new Map();
const regionMap = new Map(); // region key (AWS code) -> { container, grid }
let hasRendered = false;

// AWS region → human-readable label for the region group header. Falls
// back to the raw region string when a code isn't in the map.
const REGION_LABELS = {
  "eu-central-1": "Frankfurt",
  "ap-northeast-1": "Tokyo",
  "us-east-1": "N. Virginia",
  "us-west-2": "Oregon",
};

// Stable display order so Frankfurt always appears above Tokyo
// regardless of config.yaml ordering. Unknown regions sort to the
// bottom alphabetically.
const REGION_ORDER = ["eu-central-1", "ap-northeast-1", "us-east-1", "us-west-2"];

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

  // Group targets by AWS region. Each region gets its own .region-group
  // container with a header + grid; cards are routed into the right
  // grid by `target.region`. Targets with no region fall under
  // "unknown". (bot-strategy#231 redesign Phase A2)
  const seenKeys = new Set();
  const seenRegions = new Set();
  const cardsByRegion = new Map();

  data.targets.forEach((target, index) => {
    const region = target.region || "unknown";
    seenRegions.add(region);
    if (!cardsByRegion.has(region)) cardsByRegion.set(region, []);
    cardsByRegion.get(region).push({ target, index });
  });

  // Render each region's cards into its grid; create the group on
  // first sight, reuse on subsequent ticks.
  for (const region of cardsByRegion.keys()) {
    const group = getOrCreateRegionGroup(region);
    const orderedCards = [];
    const items = cardsByRegion.get(region);
    items.forEach(({ target, index }) => {
      const key = keyForTarget(target, index);
      let card = cardMap.get(key);
      if (!card) {
        card = createCard(key);
        cardMap.set(key, card);
        group.grid.appendChild(card);
      } else if (card.parentElement !== group.grid) {
        // Region change for this target — move the card.
        group.grid.appendChild(card);
      }
      updateCard(card, target, pollSecs, index, key);
      seenKeys.add(key);
      orderedCards.push(card);
    });
    reconcileOrderInGrid(group.grid, orderedCards);
    const countEl = group.container.querySelector('[data-field="region-count"]');
    if (countEl) {
      countEl.textContent = `${items.length} ${items.length === 1 ? "target" : "targets"}`;
    }
  }

  // Drop cards whose target disappeared between ticks.
  for (const [key, card] of cardMap.entries()) {
    if (!seenKeys.has(key)) {
      card.remove();
      cardMap.delete(key);
    }
  }

  // Drop region groups that no longer have any targets, then reorder
  // the remaining groups according to REGION_ORDER.
  for (const [region, group] of regionMap.entries()) {
    if (!seenRegions.has(region)) {
      group.container.remove();
      regionMap.delete(region);
    }
  }
  reconcileRegionOrder();

  // Fleet summary: aggregates over all targets, regardless of region.
  // (bot-strategy#231 redesign Phase A1)
  updateFleetSummary(data.targets);

  if (!hasRendered) {
    hasRendered = true;
    cardsEl.classList.add("live");
  }
};

const getOrCreateRegionGroup = (region) => {
  if (regionMap.has(region)) return regionMap.get(region);
  const container = document.createElement("section");
  container.className = "region-group";
  container.dataset.region = region;
  const label = REGION_LABELS[region] || region;
  container.innerHTML = `
    <header class="region-header">
      <h2 class="region-name">${escapeHtml(label)}</h2>
      <span class="region-code">${escapeHtml(region)}</span>
      <span class="region-count" data-field="region-count">0 targets</span>
    </header>
    <div class="grid region-grid"></div>
  `;
  cardsEl.appendChild(container);
  const group = {
    container,
    grid: container.querySelector(".grid"),
    region,
  };
  regionMap.set(region, group);
  return group;
};

const reconcileRegionOrder = () => {
  const sortedRegions = Array.from(regionMap.keys()).sort((a, b) => {
    const ai = REGION_ORDER.indexOf(a);
    const bi = REGION_ORDER.indexOf(b);
    if (ai === -1 && bi === -1) return a.localeCompare(b);
    if (ai === -1) return 1;
    if (bi === -1) return -1;
    return ai - bi;
  });
  let node = cardsEl.firstElementChild;
  sortedRegions.forEach((region) => {
    const group = regionMap.get(region);
    if (group.container !== node) {
      cardsEl.insertBefore(group.container, node);
    } else {
      node = node.nextElementSibling;
    }
  });
};

// Equity at-or-just-before the given timestamp from a sorted history.
// Returns the latest point with ts < anchorMs (preferred baseline), or
// the earliest point in the array when the bot's history starts after
// the anchor (e.g. bot was provisioned mid-month). Returns null when
// the cache is empty.
const baselineEquityAt = (history, anchorMs) => {
  if (!history || history.length === 0) return null;
  let i = 0;
  while (i < history.length && history[i].ts < anchorMs) i++;
  if (i === 0) {
    return history[0].equity;
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
  let pnlToday = 0;
  let pnlMonth = 0;
  let pnlMonthAvail = false;
  let equityTotal = 0;
  let halts = 0;
  let killSwitches = 0;
  let servicesDown = 0;
  let halts24h = 0;
  const cutoff24hSec = Math.floor(Date.now() / 1000) - 86400;
  const monthStartMs = currentUtcMonthStartMs();
  targets.forEach((target, index) => {
    const data = target.status;
    if (data) {
      if (typeof data.pnl_today === "number") pnlToday += data.pnl_today;
      if (typeof data.pnl_total === "number") equityTotal += data.pnl_total;
      if (typeof data.pnl_total === "number") {
        const key = keyForTarget(target, index);
        const history = historyByKey.get(key);
        const baseline = baselineEquityAt(history, monthStartMs);
        if (baseline !== null) {
          pnlMonth += data.pnl_total - baseline;
          pnlMonthAvail = true;
        }
      }
      if (data.session_risk && data.session_risk.session_halted === true) halts += 1;
      if (data.daily_risk && data.daily_risk.risk_halted === true) halts += 1;
      if (data.circuit_breaker && data.circuit_breaker.active === true) halts += 1;
      if (Array.isArray(data.risk_history)) {
        for (const ev of data.risk_history) {
          if (ev.event_type === "activated" && ev.ts >= cutoff24hSec) {
            halts24h += 1;
          }
        }
      }
    }
    if (target.kill_switch_active === true) killSwitches += 1;
    if (target.service_status && target.service_status !== "active") servicesDown += 1;
  });
  setField("fleet-total", `${targets.length}`);
  setField("fleet-pnl-today", formatPnl(pnlToday));
  applySignedClass(
    fleetSummaryEl.querySelector('[data-field="fleet-pnl-today"]'),
    pnlToday,
  );
  if (pnlMonthAvail) {
    setField("fleet-pnl-month", formatPnl(pnlMonth));
    applySignedClass(
      fleetSummaryEl.querySelector('[data-field="fleet-pnl-month"]'),
      pnlMonth,
    );
  } else {
    setField("fleet-pnl-month", "-");
    applySignedClass(
      fleetSummaryEl.querySelector('[data-field="fleet-pnl-month"]'),
      null,
    );
  }
  setField("fleet-equity-total", formatUsdc(equityTotal));
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
        <span class="status-pill" data-field="status"></span>
        <span class="status-pill maintenance" data-field="maintenance" hidden></span>
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
      <div class="row"><span>Instance</span><strong data-field="instance"></strong></div>
      <div class="row"><span>AWS Region</span><strong data-field="region"></strong></div>
      <div class="row"><span>Service</span><strong data-field="service"></strong></div>
      <div class="row"><span>Started</span><strong data-field="started"></strong></div>
      <div class="row"><span>Last update</span><strong data-field="age"></strong></div>
      <div class="row shutdown-row" data-field="shutdown-row" hidden><span>Shutdown</span><strong data-field="shutdown-eta"></strong></div>
      <div class="kv">
        <div>PnL today <span data-field="pnl-today"></span></div>
        <div>Equity total <span data-field="pnl-total"></span></div>
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
      <div class="backtest-alert" data-field="backtest-alert" hidden></div>
      <div class="positions" data-field="positions-list"></div>
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
  const statusClass = status === "active" ? "active" : status === "inactive" ? "inactive" : "unknown";
  const data = target.status || {};
  const updatedAt = data.updated_at ? new Date(data.updated_at) : null;
  const stale = isStale(updatedAt, pollSecs);
  const pnlTodayValue = parseNumber(data.pnl_today);
  const pnlTotalValue = parseNumber(data.pnl_total);
  const pnlToday = formatPnl(pnlTodayValue);
  const pnlTotal = formatUsdc(pnlTotalValue);
  const positions = Array.isArray(data.positions) ? data.positions : [];
  const ageText = updatedAt ? `${formatAge(Date.now() - updatedAt.getTime())} ago` : "unknown";

  card.classList.toggle("stale", stale);
  card.style.animationDelay = `${index * 0.04}s`;

  const nameEl = card.querySelector('[data-field="name"]');
  const statusEl = card.querySelector('[data-field="status"]');
  const instanceEl = card.querySelector('[data-field="instance"]');
  const regionEl = card.querySelector('[data-field="region"]');
  const serviceEl = card.querySelector('[data-field="service"]');
  const ageEl = card.querySelector('[data-field="age"]');
  const pnlTodayEl = card.querySelector('[data-field="pnl-today"]');
  const pnlTotalEl = card.querySelector('[data-field="pnl-total"]');
  const positionsListEl = card.querySelector('[data-field="positions-list"]');
  const errorEl = card.querySelector('[data-field="error"]');
  const chartEl = card.querySelector('[data-field="equity-chart"]');
  const chartEmptyEl = card.querySelector('[data-field="equity-empty"]');

  nameEl.textContent = target.name || target.service || "debot";
  statusEl.textContent = status;
  statusEl.className = `status-pill ${statusClass}`;

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
  // 10/day per bot-strategy#47 — above that, the pill turns red and the
  // error-watch workflow auto-creates an issue.
  const wsResetEl = card.querySelector('[data-field="ws-reset"]');
  if (wsResetEl) {
    const count = target.ws_reset_24h;
    if (count !== undefined && count !== null) {
      wsResetEl.textContent = `WS: ${count}/24h`;
      wsResetEl.classList.toggle("has-error", count > 10);
      wsResetEl.classList.toggle("has-warn", count > 0 && count <= 10);
      wsResetEl.title = `Connection reset events in last 24h (alert threshold: >10)`;
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
    (data.circuit_breaker && data.circuit_breaker.active === true);
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

  instanceEl.textContent = target.instance_id || "-";
  regionEl.textContent = target.region || "-";
  serviceEl.textContent = target.service || "-";
  const startedEl = card.querySelector('[data-field="started"]');
  if (startedEl) {
    startedEl.textContent = formatStarted(target.service_started_at);
  }
  ageEl.textContent = ageText;
  pnlTodayEl.textContent = pnlToday;
  pnlTotalEl.textContent = pnlTotal;
  applySignedClass(pnlTodayEl, pnlTodayValue);
  applySignedClass(pnlTotalEl, pnlTotalValue);

  const history = updateHistoryCache(key, data);
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
      const cagr = equityStats.cagr;
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

  // Backtest alert banner
  const alertEl = card.querySelector('[data-field="backtest-alert"]');
  const alert = data.backtest_alert;
  if (alert && alert.alert_level && alert.alert_level !== "OK") {
    alertEl.hidden = false;
    const level = alert.alert_level === "DANGER" ? "danger" : "warning";
    const ago = alert.updated_at ? formatAge(Date.now() - new Date(alert.updated_at).getTime()) : "?";
    alertEl.className = `backtest-alert backtest-${level}`;
    alertEl.innerHTML = `<strong>${escapeHtml(alert.alert_level)}</strong> ${escapeHtml(alert.alert_msg || "")} <span class="alert-age">(${ago} ago)</span>`;
  } else if (alert && alert.alert_level === "OK") {
    alertEl.hidden = false;
    alertEl.className = "backtest-alert backtest-ok";
    alertEl.textContent = alert.alert_msg || "Backtest OK";
  } else {
    alertEl.hidden = true;
  }

  if (target.error) {
    errorEl.hidden = false;
    errorEl.textContent = target.error;
  } else {
    errorEl.hidden = true;
    errorEl.textContent = "";
  }
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
    return history;
  }
  const point = snapshotToPoint(data);
  if (point) {
    history = appendHistoryPoint(history, point);
    historyByKey.set(key, history);
  }
  return history;
};

const snapshotToPoint = (data) => {
  if (!data || !Number.isFinite(data.pnl_total)) {
    return null;
  }
  const tsSeconds = Number.isFinite(data.ts) ? Number(data.ts) * 1000 : null;
  const ts =
    tsSeconds ||
    (data.updated_at ? Date.parse(data.updated_at) : null) ||
    Date.now();
  if (!Number.isFinite(ts)) {
    return null;
  }
  return { ts, equity: Number(data.pnl_total) };
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
  const dailyBar = card.querySelector('[data-field="daily-dd-bar"]');
  if (dailyBar) {
    const dr = data.daily_risk;
    const eff = dr ? dr.effective_max_daily_loss_bps : 0;
    if (dr && eff > 0) {
      const lossBps = dr.daily_pnl_bps < 0 ? -dr.daily_pnl_bps : 0;
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
  // data.risk_history. Empty for old bots that haven't picked up the
  // pairtrade@9755490 binary yet — gracefully hidden in that case.
  renderRiskHistory(card, data.risk_history);
  if (data.risk_history && data.risk_history.length > 0) {
    anyVisible = true;
  }

  panel.hidden = !anyVisible;
};

const RISK_HISTORY_WINDOW_MS = 30 * 24 * 60 * 60 * 1000;

const renderRiskHistory = (card, events) => {
  const container = card.querySelector('[data-field="risk-history"]');
  const strip = card.querySelector('[data-field="risk-history-strip"]');
  if (!container || !strip) return;
  if (!events || events.length === 0) {
    container.hidden = true;
    strip.innerHTML = "";
    return;
  }
  const nowMs = Date.now();
  const cutoffMs = nowMs - RISK_HISTORY_WINDOW_MS;
  const dots = events
    .filter((ev) => ev.ts * 1000 >= cutoffMs)
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
    return;
  }
  strip.innerHTML = dots;
  container.hidden = false;
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

const formatPnl = (value) => {
  if (value === undefined || value === null || Number.isNaN(value)) {
    return "-";
  }
  const number = Number(value);
  const sign = number > 0 ? "+" : "";
  return `${sign}${number.toFixed(1)}`;
};

const formatNumber = (value) => {
  if (value === undefined || value === null || Number.isNaN(value)) {
    return "-";
  }
  const number = Number(value);
  return number.toFixed(1);
};

const formatUsdc = (value) => {
  if (value === undefined || value === null || Number.isNaN(value)) {
    return "-";
  }
  const number = Number(value);
  return `${number.toFixed(1)} USDC`;
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

const isStale = (updatedAt, pollSecs) => {
  if (!updatedAt) {
    return true;
  }
  const diffSecs = (Date.now() - updatedAt.getTime()) / 1000;
  return diffSecs > pollSecs * 3;
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
