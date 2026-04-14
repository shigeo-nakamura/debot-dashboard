const cardsEl = document.getElementById("cards");
const lastUpdatedEl = document.getElementById("last-updated");
const pollIntervalEl = document.getElementById("poll-interval");
const rangeToggleEl = document.getElementById("range-toggle");

const POLL_MS = 5000;
const cardMap = new Map();
const historyByKey = new Map();
const fullHistoryByKey = new Map();
let hasRendered = false;

const RANGE_OPTIONS = [
  { id: "1d", label: "1D", ms: 24 * 60 * 60 * 1000 },
  { id: "1w", label: "1W", ms: 7 * 24 * 60 * 60 * 1000 },
  { id: "1m", label: "1M", ms: 30 * 24 * 60 * 60 * 1000 },
  { id: "all", label: "ALL", ms: null },
];
let currentRange = "1d";

const loadStatus = async (includeHistory = false) => {
  try {
    const url = includeHistory ? `/api/status?range=${currentRange}` : "/api/status";
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

  const seenKeys = new Set();
  const orderedCards = [];

  data.targets.forEach((target, index) => {
    const key = keyForTarget(target, index);
    let card = cardMap.get(key);
    if (!card) {
      card = createCard(key);
      cardMap.set(key, card);
      cardsEl.appendChild(card);
    }
    updateCard(card, target, pollSecs, index, key);
    seenKeys.add(key);
    orderedCards.push(card);
  });

  for (const [key, card] of cardMap.entries()) {
    if (!seenKeys.has(key)) {
      card.remove();
      cardMap.delete(key);
    }
  }

  reconcileOrder(orderedCards);

  if (!hasRendered) {
    hasRendered = true;
    cardsEl.classList.add("live");
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
      <div class="card-header">
        <h2 class="card-title" data-field="name"></h2>
        <span class="status-pill" data-field="status"></span>
        <span class="status-pill maintenance" data-field="maintenance" hidden></span>
        <span class="status-pill errors" data-field="errors" hidden></span>
      </div>
      <div class="row"><span>Instance</span><strong data-field="instance"></strong></div>
      <div class="row"><span>Service</span><strong data-field="service"></strong></div>
      <div class="row"><span>Last update</span><strong data-field="age"></strong></div>
      <div class="row shutdown-row" data-field="shutdown-row" hidden><span>Shutdown</span><strong data-field="shutdown-eta"></strong></div>
      <div class="kv">
        <div>PnL today <span data-field="pnl-today"></span></div>
        <div>PnL total <span data-field="pnl-session"></span></div>
        <div>Equity total <span data-field="pnl-total"></span></div>
      </div>
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
    `;
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
  const serviceEl = card.querySelector('[data-field="service"]');
  const ageEl = card.querySelector('[data-field="age"]');
  const pnlTodayEl = card.querySelector('[data-field="pnl-today"]');
  const pnlSessionEl = card.querySelector('[data-field="pnl-session"]');
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
      const e5 = es.error_count_5m || 0;
      const w5 = es.warn_count_5m || 0;
      errorsEl.textContent = `5m: ${e5}E/${w5}W`;
      errorsEl.classList.toggle("has-error", e5 > 0);
      errorsEl.classList.toggle("has-warn", e5 === 0 && w5 > 0);
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
  serviceEl.textContent = target.service || "-";
  ageEl.textContent = ageText;
  pnlTodayEl.textContent = pnlToday;
  pnlTotalEl.textContent = pnlTotal;
  applySignedClass(pnlTodayEl, pnlTodayValue);
  applySignedClass(pnlTotalEl, pnlTotalValue);

  const history = updateHistoryCache(key, data);
  const sessionPnlValue = computeSessionPnl(
    target.service_started_at,
    fullHistoryByKey.get(key) || history,
    pnlTotalValue
  );
  pnlSessionEl.textContent = formatPnl(sessionPnlValue);
  applySignedClass(pnlSessionEl, sessionPnlValue);
  renderEquityChart(chartEl, chartEmptyEl, history);

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
            <span>${escapeHtml(pos.side || "")}</span>
            <span>${escapeHtml(pos.size || "")}</span>
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
      historyByKey.clear();
      [...rangeToggleEl.querySelectorAll(".range-btn")].forEach((el) =>
        el.classList.toggle("active", el === button)
      );
      loadStatus(true);
    });
    rangeToggleEl.appendChild(button);
  });
};

const updateHistoryCache = (key, data) => {
  let history = historyByKey.get(key) || [];
  if (Array.isArray(data.equity_history)) {
    history = data.equity_history
      .map((point) => ({
        ts: Number(point.ts),
        equity: Number(point.equity),
      }))
      .filter((point) => Number.isFinite(point.ts) && Number.isFinite(point.equity));
    if (currentRange === "all") {
      fullHistoryByKey.set(key, history);
    }
    history = filterHistoryByRange(history);
    historyByKey.set(key, history);
    return history;
  }
  const point = snapshotToPoint(data);
  if (point) {
    history = appendHistoryPoint(history, point);
    history = filterHistoryByRange(history);
    historyByKey.set(key, history);
    const fullHistory = fullHistoryByKey.get(key);
    if (Array.isArray(fullHistory)) {
      fullHistoryByKey.set(key, appendHistoryPoint(fullHistory, point));
    } else if (currentRange === "all") {
      fullHistoryByKey.set(key, history);
    }
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

const reconcileOrder = (orderedCards) => {
  let node = cardsEl.firstElementChild;
  orderedCards.forEach((card) => {
    if (card !== node) {
      cardsEl.insertBefore(card, node);
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

const computeSessionPnl = (serviceStartedAt, fullHistory, currentEquity) => {
  if (!Number.isFinite(currentEquity)) {
    return null;
  }
  if (!serviceStartedAt) {
    return null;
  }
  const startedAtMs = Date.parse(serviceStartedAt);
  if (!Number.isFinite(startedAtMs)) {
    return null;
  }
  if (!Array.isArray(fullHistory) || fullHistory.length === 0) {
    return null;
  }
  const baseline = findBaselineEquity(fullHistory, startedAtMs);
  if (!Number.isFinite(baseline)) {
    return null;
  }
  return currentEquity - baseline;
};

const findBaselineEquity = (history, startedAtMs) => {
  let baseline = null;
  for (const point of history) {
    if (!Number.isFinite(point.ts) || !Number.isFinite(point.equity)) {
      continue;
    }
    if (point.ts >= startedAtMs) {
      baseline = point.equity;
      break;
    }
  }
  if (baseline === null) {
    const last = history[history.length - 1];
    baseline = last && Number.isFinite(last.equity) ? last.equity : null;
  }
  return baseline;
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
