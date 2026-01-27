const cardsEl = document.getElementById("cards");
const lastUpdatedEl = document.getElementById("last-updated");
const pollIntervalEl = document.getElementById("poll-interval");

const POLL_MS = 5000;
const cardMap = new Map();
let hasRendered = false;

const loadStatus = async () => {
  try {
    const response = await fetch("/api/status", { cache: "no-store" });
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
  pollIntervalEl.textContent = `Poll ${pollSecs}s`;

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
    updateCard(card, target, pollSecs, index);
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
  pollIntervalEl.textContent = "";
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
        <div>PnL total <span data-field="pnl-total"></span></div>
      </div>
      <div class="positions" data-field="positions-list"></div>
      <div class="error" data-field="error" hidden></div>
    `;
  return card;
};

const updateCard = (card, target, pollSecs, index) => {
  const status = target.service_status || "unknown";
  const statusClass = status === "active" ? "active" : status === "inactive" ? "inactive" : "unknown";
  const data = target.status || {};
  const updatedAt = data.updated_at ? new Date(data.updated_at) : null;
  const stale = isStale(updatedAt, pollSecs);
  const pnlToday = formatPnl(data.pnl_today);
  const pnlTotal = formatPnl(data.pnl_total);
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
  const pnlTotalEl = card.querySelector('[data-field="pnl-total"]');
  const positionsListEl = card.querySelector('[data-field="positions-list"]');
  const errorEl = card.querySelector('[data-field="error"]');

  nameEl.textContent = target.name || target.service || "debot";
  statusEl.textContent = status;
  statusEl.className = `status-pill ${statusClass}`;
  instanceEl.textContent = target.instance_id || "-";
  serviceEl.textContent = target.service || "-";
  ageEl.textContent = ageText;
  positionsEl.textContent = positionCount;
  pnlTodayEl.textContent = pnlToday;
  pnlTotalEl.textContent = pnlTotal;

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
  return `${sign}${number.toFixed(4)}`;
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

loadStatus();
setInterval(loadStatus, POLL_MS);
