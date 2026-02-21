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
let currentRange = "all";

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
      </div>
      <div class="row"><span>Instance</span><strong data-field="instance"></strong></div>
      <div class="row"><span>Service</span><strong data-field="service"></strong></div>
      <div class="row"><span>Last update</span><strong data-field="age"></strong></div>
      <div class="kv">
        <div>Positions <span data-field="positions"></span></div>
        <div>PnL today <span data-field="pnl-today"></span></div>
        <div>PnL total <span data-field="pnl-session"></span></div>
        <div>Equity total <span data-field="pnl-total"></span></div>
      </div>
      <div class="chart">
        <div class="chart-title">Equity trend</div>
        <svg class="sparkline" data-field="equity-chart" viewBox="0 0 100 40" preserveAspectRatio="none"></svg>
        <div class="chart-empty" data-field="equity-empty" hidden>No history yet</div>
      </div>
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
  const positionCount = Number.isFinite(data.position_count) ? data.position_count : 0;
  const positions = Array.isArray(data.positions) ? data.positions : [];
  const ageText = updatedAt ? `${formatAge(Date.now() - updatedAt.getTime())} ago` : "unknown";

  card.classList.toggle("stale", stale);
  card.style.animationDelay = `${index * 0.04}s`;

  const nameEl = card.querySelector('[data-field="name"]');
  const statusEl = card.querySelector('[data-field="status"]');
  const instanceEl = card.querySelector('[data-field="instance"]');
  const serviceEl = card.querySelector('[data-field="service"]');
  const ageEl = card.querySelector('[data-field="age"]');
  const positionsEl = card.querySelector('[data-field="positions"]');
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
  instanceEl.textContent = target.instance_id || "-";
  serviceEl.textContent = target.service || "-";
  ageEl.textContent = ageText;
  positionsEl.textContent = positionCount;
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
  return `${hours}h`;
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
