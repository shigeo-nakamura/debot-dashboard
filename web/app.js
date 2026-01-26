const cardsEl = document.getElementById("cards");
const lastUpdatedEl = document.getElementById("last-updated");
const pollIntervalEl = document.getElementById("poll-interval");

const POLL_MS = 5000;

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

  cardsEl.innerHTML = data.targets.map((target, index) => cardHtml(target, pollSecs, index)).join("");
};

const renderError = (err) => {
  lastUpdatedEl.textContent = "Waiting for data";
  pollIntervalEl.textContent = "";
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

const cardHtml = (target, pollSecs, index) => {
  const status = target.service_status || "unknown";
  const statusClass = status === "active" ? "active" : status === "inactive" ? "inactive" : "unknown";
  const data = target.status || {};
  const updatedAt = data.updated_at ? new Date(data.updated_at) : null;
  const stale = isStale(updatedAt, pollSecs);
  const pnlToday = formatPnl(data.pnl_today);
  const pnlTotal = formatPnl(data.pnl_total);
  const positionCount = Number.isFinite(data.position_count) ? data.position_count : 0;
  const positions = Array.isArray(data.positions) ? data.positions : [];

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

  const ageText = updatedAt ? `${formatAge(Date.now() - updatedAt.getTime())} ago` : "unknown";

  return `
    <article class="card ${stale ? "stale" : ""}" style="animation-delay:${index * 0.04}s">
      <div class="card-header">
        <h2 class="card-title">${escapeHtml(target.name || target.service || "debot")}</h2>
        <span class="status-pill ${statusClass}">${escapeHtml(status)}</span>
      </div>
      <div class="row"><span>Instance</span><strong>${escapeHtml(target.instance_id || "-")}</strong></div>
      <div class="row"><span>Service</span><strong>${escapeHtml(target.service || "-")}</strong></div>
      <div class="row"><span>Last update</span><strong>${ageText}</strong></div>
      <div class="kv">
        <div>Positions <span>${positionCount}</span></div>
        <div>PnL today <span>${pnlToday}</span></div>
        <div>PnL total <span>${pnlTotal}</span></div>
      </div>
      <div class="positions">
        ${positionsHtml}
      </div>
      ${target.error ? `<div class="error">${escapeHtml(target.error)}</div>` : ""}
    </article>
  `;
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
