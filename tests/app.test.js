const assert = require("node:assert/strict");
const fs = require("node:fs");
const test = require("node:test");
const vm = require("node:vm");

const source = `${fs.readFileSync(`${__dirname}/../web/app.js`, "utf8")}
globalThis.__test = { renderArcusStatus, isArcusStatus, isStale, renderRiskHistory, isAccumulatorStatus, isTargetUnhealthy, accumulatorViewModel, isHanBridgeStatus, hanBridgeViewModel, isHanBridgeHalted, bullHolderViewModel, renderBullHolderStatus, holderMoney, updateFleetSummary, snapshotToPoint, renderHolderSummary, renderArcusSummary, holderLastTradeText, arcusLastTradeText };`;
const fleetFields = new Map();
const fleet = { querySelector(selector) {
  if (!fleetFields.has(selector)) fleetFields.set(selector, { textContent: "", closest() { return null; }, classList: { toggle() {}, add() {}, remove() {} } });
  return fleetFields.get(selector);
} };
const context = {
  document: { getElementById: (id) => id === "fleet-summary" ? fleet : null },
  fetch: () => new Promise(() => {}),
  setInterval: () => 0,
};

test("fleet includes holder halt and kill switch without counting simulated capital", () => {
  context.__test.updateFleetSummary([
    { service_status: "active", status: { pnl_total: 100, pnl_today: 5, position_count: 2 } },
    { service_status: "active", kill_switch_active: true, status: { pnl_total: 9000, pnl_today: 1000, position_count: 4, bull_holder: { halted: true } } },
  ]);
  const value = (name) => fleetFields.get(`[data-field="${name}"]`).textContent;
  assert.equal(value("fleet-halts"), "1");
  assert.equal(value("fleet-kill-switches"), "1");
  assert.equal(value("fleet-equity-total"), "100.0 USDC");
  assert.equal(value("fleet-positions-total"), "1");
});

test("bull holder uses daily close, distinguishes unknown peaks and pending ADD", () => {
  const fixture = JSON.parse(fs.readFileSync(`${__dirname}/fixtures/bull-holder-status.json`, "utf8"));
  const model = context.__test.bullHolderViewModel({ ...fixture, pending: { ARM: false, ADD: true }, pending_add: 2 });
  assert.equal(model.legs[0].drop, 100 * (1 - 80000 / 100000));
  assert.equal(model.legs[1].drop, null);
  assert.equal(model.pending, "ADD × 2");
  assert.equal(model.total, "—");
  assert.equal(context.__test.holderMoney(0), "0.00 USDC");
  for (const missing of [null, undefined, "", " ", false]) {
    assert.equal(context.__test.holderMoney(missing), "—");
  }
  assert.equal(context.__test.bullHolderViewModel({ total_equity_usdc: null, legs: {} }).total, "—");
  assert.equal(context.__test.holderMoney(0.003691), "0.003691 USDC");
});

test("bull holder render is read-only and separates simulation from actual holdings", () => {
  const node = () => ({ children: [], textContent: "", appendChild(n) { this.children.push(n); }, replaceChildren() { this.children = []; }, setAttribute() {} });
  const tags = [];
  context.document.createElement = (tag) => { tags.push(tag); return node(); };
  const root = node();
  const fixture = JSON.parse(fs.readFileSync(`${__dirname}/fixtures/bull-holder-status.json`, "utf8"));
  fixture.hyperliquid = { equity_usdc: 100, usdc: 100, observed_at: 1788600000, holdings: [] };
  fixture.lighter = { error: "Unavailable" };
  fixture.total_equity_usdc = null;
  fixture.investment = { config_fp: "4c67969bf10a", equity_usd: 1000, spot_fraction: 0.9, perp_fraction: 0.45 };
  fixture.unrealized_pnl_usdc = -12;
  context.__test.renderBullHolderStatus(root, fixture, true);
  const text = (n) => n.textContent + " " + n.children.map(text).join(" ");
  assert.match(text(root), /Strategy holdings · simulated/);
  assert.match(text(root), /Actual account assets/);
  assert.match(text(root), /20.00%/);
  assert.match(text(root), /Unavailable/);
  assert.match(text(root), /Combined monitored equity\s+—/);
  assert.match(text(root), /Configured capital \(USD\)\s+1,000/);
  assert.match(text(root), /Hyperliquid spot allocation \(USD\)\s+900/);
  assert.match(text(root), /Lighter perp notional target \(USD\)\s+450/);
  assert.match(text(root), /Combined unrealized PnL · estimate\s+-12.00 USDC/);
  assert.equal(tags.includes("button"), false);
  context.__test.renderBullHolderStatus(root, { pending: null, kill_switch: false }, false);
  assert.match(text(root), /Configured capital \(USD\)\s+—/);
  assert.match(text(root), /Combined unrealized PnL · estimate\s+—/);
  assert.match(text(root), /KILL_SWITCH\s+Unknown · monitoring unavailable/);
  for (const known of [{ pending: { KILL_SWITCH: false } }, { mode: "Off", kill_switch: false }]) {
    context.__test.renderBullHolderStatus(root, known, true);
    assert.match(text(root), /KILL_SWITCH\s+Not engaged/);
  }
  context.__test.renderBullHolderStatus(root, { pending: { KILL_SWITCH: true } }, true);
  assert.match(text(root), /KILL_SWITCH\s+Engaged/);
  assert.equal(context.__test.isTargetUnhealthy({ service_status: "active", status: { bull_holder: fixture } }), true);
  fixture.lighter = {};
  fixture.halted = true;
  assert.equal(context.__test.isTargetUnhealthy({ service_status: "active", status: { bull_holder: fixture } }), true);
});
vm.runInNewContext(source, context);

const accumulatorFixture = JSON.parse(
  fs.readFileSync(
    `${__dirname}/fixtures/hype-accumulator-status-v1.json`,
    "utf8",
  ),
);

const hanBridgeFixture = JSON.parse(
  fs.readFileSync(`${__dirname}/fixtures/han-bridge-status-v1.json`, "utf8"),
);

const makeCard = () => {
  const container = { hidden: true };
  const strip = { innerHTML: "" };
  return {
    container,
    strip,
    card: {
      querySelector: (selector) =>
        selector === '[data-field="risk-history"]' ? container : strip,
    },
  };
};

test("halt history excludes capital rebaseline audit events", () => {
  const now = Math.floor(Date.now() / 1000);
  const view = makeCard();

  const rendered = context.__test.renderRiskHistory(view.card, [
    {
      ts: now - 60,
      kind: "session_dd",
      event_type: "capital_rebaseline",
      reason: "deposit",
    },
    {
      ts: now - 30,
      kind: "session_dd",
      event_type: "activated",
      reason: "session_dd_50bps_lev10.0",
    },
    {
      ts: now - 10,
      kind: "session_dd",
      event_type: "ack",
    },
  ]);

  assert.equal(rendered, true);
  assert.equal(view.container.hidden, false);
  assert.equal((view.strip.innerHTML.match(/risk-history-dot/g) || []).length, 2);
  assert.doesNotMatch(view.strip.innerHTML, /capital_rebaseline/);
  assert.match(view.strip.innerHTML, /event-activated/);
  assert.match(view.strip.innerHTML, /event-ack/);
});

test("halt history stays hidden when only non-halt audit events exist", () => {
  const view = makeCard();

  const rendered = context.__test.renderRiskHistory(view.card, [
    {
      ts: Math.floor(Date.now() / 1000),
      kind: "session_dd",
      event_type: "capital_rebaseline",
      reason: "withdrawal",
    },
  ]);

  assert.equal(rendered, false);
  assert.equal(view.container.hidden, true);
  assert.equal(view.strip.innerHTML, "");
});

test("producer fixture reports balances, activity age, and cadence", () => {
  const accumulator = accumulatorFixture.accumulator;
  const model = context.__test.accumulatorViewModel(
    accumulator,
    Date.parse(accumulator.balance_observed_at),
  );

  assert.equal(accumulatorFixture.schema_version, 1);
  assert.equal(accumulatorFixture.dry_run, true);
  assert.equal(context.__test.isAccumulatorStatus({ accumulator }), true);
  assert.equal(context.__test.isAccumulatorStatus({ pnl_total: 100 }), false);
  assert.equal(model.total, "125.0 USDC");
  assert.equal(model.usdc, "25.0 USDC");
  assert.equal(model.hype, "2.5 HYPE");
  assert.equal(model.mark, "40.0 USDC");
  assert.match(model.lastTrade, /24h ago$/);
  assert.equal(model.cadence, "Mon/Wed/Fri at 12:00 UTC");
  assert.match(model.observed, /0s ago$/);
});

test("accumulator view model derives unrealized PnL from operations.spent_usdc", () => {
  const accumulator = accumulatorFixture.accumulator;
  const nowMs = Date.parse(accumulator.balance_observed_at);

  const withoutOperations = context.__test.accumulatorViewModel(
    accumulator,
    nowMs,
  );
  assert.equal(withoutOperations.unrealizedPnl, null);

  // hype_balance (2.5) * hype_price_usdc (40.0) = 100.0 mark value;
  // spent 80.0 to acquire it => +20.0 unrealized.
  const gaining = context.__test.accumulatorViewModel(accumulator, nowMs, {
    spent_usdc: 80.0,
  });
  assert.equal(gaining.unrealizedPnlUsdc, 20.0);
  assert.equal(gaining.unrealizedPnl, "+20.0");

  // Spent more than the current mark value => unrealized loss.
  const losing = context.__test.accumulatorViewModel(accumulator, nowMs, {
    spent_usdc: 150.0,
  });
  assert.equal(losing.unrealizedPnlUsdc, -50.0);
});

test("fleet health counts a fresh degraded accumulator once", () => {
  assert.equal(
    context.__test.isTargetUnhealthy({
      service_status: "active",
      status: { accumulator: { healthy: false } },
    }),
    true,
  );
  assert.equal(
    context.__test.isTargetUnhealthy({
      service_status: "active",
      status: { accumulator: { healthy: true } },
    }),
    false,
  );
  assert.equal(
    context.__test.isTargetUnhealthy({
      service_status: "stale",
      status: { accumulator: { healthy: false } },
    }),
    true,
  );
});

test("han_bridge fixture reports pair and a no-signal today (day_entered without a position)", () => {
  const hanBridge = hanBridgeFixture.han_bridge;

  assert.equal(context.__test.isHanBridgeStatus({ han_bridge: hanBridge }), true);
  assert.equal(context.__test.isHanBridgeStatus({ pnl_total: 100 }), false);

  // The captured fixture is day_entered=true / has_position=false: a
  // below-threshold no-signal day, not an actual trade -- day_entered
  // means "today's decision is finalized", not "holding a position"
  // (code-review finding on PR #23).
  assert.equal(hanBridgeFixture.has_position, false);
  const model = context.__test.hanBridgeViewModel(hanBridge, { hasPosition: false });
  assert.equal(model.pair, "SKHY → SNDK");
  assert.equal(model.today.label, "No signal today");
  assert.equal(model.today.tone, "neutral");
  assert.deepEqual(model.reasons, []);
  assert.equal(model.sessionHaltReason, null);
});

test("han_bridge view model labels a real position as entered/holding only when hasPosition is true", () => {
  const hanBridge = {
    kr_primary_symbol: "SKHY",
    us_primary_symbol: "SNDK",
    day_entered: true,
    day_exited: false,
    ineligible_reasons: [],
  };
  assert.equal(
    context.__test.hanBridgeViewModel(hanBridge, { hasPosition: true }).today.label,
    "Entered, holding",
  );
  assert.equal(
    context.__test.hanBridgeViewModel(hanBridge, { hasPosition: false }).today.label,
    "No signal today",
  );
});

test("han_bridge view model surfaces ineligible reasons and reflects skip over entry", () => {
  const model = context.__test.hanBridgeViewModel(
    {
      kr_primary_symbol: "SKHY",
      us_primary_symbol: "SNDK",
      day_entered: true,
      day_exited: false,
      ineligible_reasons: ["kr_primary=SKHY:force_reduce_only"],
      session_halt_reason: "max_session_loss_bps exceeded",
    },
    { hasPosition: false },
  );
  assert.equal(model.today.label, "Skipped (ineligible)");
  assert.equal(model.today.tone, "warn");
  assert.deepEqual(model.reasons, ["kr_primary=SKHY:force_reduce_only"]);
  assert.equal(model.sessionHaltReason, "max_session_loss_bps exceeded");
});

test("han_bridge view model distinguishes not-decided, kill-switch-blocked, and entered-and-exited", () => {
  const notDecided = context.__test.hanBridgeViewModel({
    kr_primary_symbol: "SKHY",
    us_primary_symbol: "SNDK",
    day_entered: false,
    day_exited: false,
    ineligible_reasons: [],
  });
  assert.equal(notDecided.today.label, "Not decided yet");
  assert.equal(notDecided.today.tone, "neutral");

  const blocked = context.__test.hanBridgeViewModel(
    {
      kr_primary_symbol: "SKHY",
      us_primary_symbol: "SNDK",
      day_entered: false,
      day_exited: false,
      ineligible_reasons: [],
    },
    { killSwitchActive: true },
  );
  assert.equal(blocked.today.label, "Blocked (halted)");
  assert.equal(blocked.today.tone, "warn");

  // Regression for PR #23 round 2 self-review: the halt check must read
  // hanBridge.session_halt_reason directly, not a `sessionHalted` param
  // sourced from data.session_halted -- StatusData (main.go) has no
  // such top-level field, so that param was always false. A session
  // DD halt with the kill switch NOT engaged must still show "Blocked".
  const haltedNoKillSwitch = context.__test.hanBridgeViewModel({
    kr_primary_symbol: "SKHY",
    us_primary_symbol: "SNDK",
    day_entered: false,
    day_exited: false,
    ineligible_reasons: [],
    session_halt_reason: "max_session_loss_bps exceeded",
  });
  assert.equal(haltedNoKillSwitch.today.label, "Blocked (halted)");
  assert.equal(haltedNoKillSwitch.today.tone, "warn");

  const exited = context.__test.hanBridgeViewModel(
    {
      kr_primary_symbol: "SKHY",
      us_primary_symbol: "SNDK",
      day_entered: true,
      day_exited: true,
      ineligible_reasons: [],
    },
    { hasPosition: false },
  );
  assert.equal(exited.today.label, "Entered & exited");
  assert.equal(exited.today.tone, "ok");
});

test("isHanBridgeHalted reflects the nested session_halt_reason, not the top-level fields", () => {
  assert.equal(
    context.__test.isHanBridgeHalted({ han_bridge: { session_halt_reason: "max_session_loss_bps exceeded" } }),
    true,
  );
  assert.equal(
    context.__test.isHanBridgeHalted({ han_bridge: { session_halt_reason: null } }),
    false,
  );
  assert.equal(context.__test.isHanBridgeHalted({ pnl_total: 100 }), false);
});

test("Arcus fleet tracks halt and health without treating inventory as trading PnL", () => {
  context.__test.updateFleetSummary([
    { service_status: "active", status: { pnl_total: 100, pnl_today: 5, position_count: 1 } },
    { service_status: "active", status: { pnl_total: 3000, pnl_today: 200, position_count: 2, arcus: { healthy: false, risk_halt: { kind: "daily_loss" } } } },
  ]);
  const value = (name) => fleetFields.get(`[data-field="${name}"]`).textContent;
  assert.equal(value("fleet-equity-total"), "100.0 USDC");
  assert.equal(value("fleet-halts"), "1");
  assert.equal(context.__test.isTargetUnhealthy({ service_status: "active", status: { arcus: { healthy: true } } }), false);
  assert.equal(context.__test.isTargetUnhealthy({ service_status: "active", status: { arcus: { healthy: false } } }), true);
});

test("freshness uses target cadence, rejects unknown and future clocks", () => {
  const old = new Date(Date.now() - 900000);
  assert.equal(context.__test.isStale(old, 1920), false);
  assert.equal(context.__test.isStale(old), true);
  assert.equal(context.__test.isStale(new Date(Date.now() - 90000)), false);
  assert.equal(context.__test.isStale(new Date("invalid"), 1920), true);
  assert.equal(context.__test.isStale(null, 1920), true);
  assert.equal(context.__test.isStale(new Date(Date.now() + 60000), 1920), true);
});

test("Arcus render separates failed tick, pending decision, strategy risk and gas observation", () => {
  const node = () => ({ children: [], textContent: "", appendChild(n) { this.children.push(n); }, replaceChildren() { this.children = []; } });
  const tags = [];
  context.document.createElement = (tag) => { tags.push(tag); return node(); };
  const root = node();
  const fixture = { pair: "SPY/QQQ", mode: "live", sequence: 1915, healthy: false, tick_outcome: "failed", service_result: "exit-code", exit_code: 1, decision: "observe", hold_code: "route_unavailable", decision_pending: true, daily_loss_usd: null, cumulative_loss_usd: 0, inventory_drawdown_usd: 12, risk_halt: { kind: "daily_loss", loss_usd: 21, limit_usd: 20 }, gas_balance_eth: .001, gas_observed_at: "2026-09-05T14:59:58Z", health_reasons: ["<script>alert(1)</script>"] };
  context.__test.renderArcusStatus(root, fixture);
  const text = (n) => n.textContent + " " + n.children.map(text).join(" ");
  const content = text(root);
  assert.match(content, /Last tick\s+failed/);
  assert.match(content, /route_unavailable · pending event commit/);
  const rowValue = (label) => root.children.find((n) => n.children[0]?.textContent === label)?.children[1]?.textContent;
  assert.equal(rowValue("Daily strategy loss · unknown day UTC"), "— / — limit");
  assert.match(content, /Cumulative strategy loss\s+\$0.00/);
  assert.match(content, /Starting basket drawdown\s+\$12.00/);
  assert.match(content, /Gas · last reconciled snapshot\s+0.001 ETH/);
  assert.match(content, /Gas observed/);
  assert.match(content, /Risk halt\s+daily_loss/);
  assert.match(content, /Daily execution budget.*UTC\s+— \/ —/);
  assert.equal(tags.includes("button"), false);
  assert.equal(tags.includes("script"), false);
  context.__test.renderArcusStatus(root, { sequence: 0 });
  assert.match(text(root), /Risk halt\s+Unknown/);
  assert.doesNotMatch(text(root), /route_unavailable/);
  for (const unknown of [null, undefined, "", " ", false]) {
    context.__test.renderArcusStatus(root, { z_score: unknown, equity_usd: unknown, daily_loss_usd: unknown, daily_loss_limit_usd: unknown, cumulative_loss_usd: 0 });
    assert.equal(rowValue("Signal z"), "—");
    assert.equal(rowValue("Inventory equity"), "—");
    assert.equal(rowValue("Daily strategy loss · unknown day UTC"), "— / — limit");
    assert.equal(rowValue("Cumulative strategy loss"), "$0.00 / — limit");
  }
});

test("Arcus render draws loss/limit gauges when both value and limit are known", () => {
  const node = () => ({ children: [], textContent: "", className: "", appendChild(n) { this.children.push(n); }, replaceChildren() { this.children = []; } });
  context.document.createElement = (tag) => node();
  const root = node();
  context.__test.renderArcusStatus(root, {
    daily_loss_usd: 15,
    daily_loss_limit_usd: 20,
    cumulative_loss_usd: 90,
    cumulative_loss_limit_usd: 100,
  });
  const bars = root.children.filter((n) => n.className === "risk-bar");
  assert.equal(bars.length, 2);
  const barText = (bar) => bar.children.map((n) => n.children.map((c) => c.textContent).join(" ")).join(" ");
  assert.match(barText(bars[0]), /Daily loss/);
  assert.match(barText(bars[0]), /75%/);
  assert.match(barText(bars[1]), /Cumulative loss/);
  assert.match(barText(bars[1]), /90%/);
  const fillClass = (bar) => bar.children[1].children[0].className;
  assert.match(fillClass(bars[0]), /severity-warn/);
  assert.match(fillClass(bars[1]), /severity-danger/);

  // No bar at all when the limit is missing/zero — mirrors the
  // existing risk-panel bars, which hide rather than divide by zero.
  const root2 = node();
  context.__test.renderArcusStatus(root2, { daily_loss_usd: 15, cumulative_loss_usd: 5, cumulative_loss_limit_usd: 0 });
  assert.equal(root2.children.filter((n) => n.className === "risk-bar").length, 0);
});

test("snapshotToPoint extracts equity from bull_holder/arcus when pnl_total is absent", () => {
  const isoNow = new Date().toISOString();
  assert.equal(context.__test.snapshotToPoint({ pnl_total: 42, updated_at: isoNow }).equity, 42);
  assert.equal(
    context.__test.snapshotToPoint({ bull_holder: { total_equity_usdc: 555 }, updated_at: isoNow }).equity,
    555,
  );
  assert.equal(
    context.__test.snapshotToPoint({ arcus: { equity_usd: 999 }, updated_at: isoNow }).equity,
    999,
  );
  // main.go's StatusData.PnlTotal has no `omitempty` and is a plain
  // float64, so bull_holder/arcus payloads always carry a spurious
  // `pnl_total: 0` too. Regression for Codex review on PR #32: the
  // sub-object must win over that zero, never the other way around.
  assert.equal(
    context.__test.snapshotToPoint({ pnl_total: 0, bull_holder: { total_equity_usdc: 555 }, updated_at: isoNow }).equity,
    555,
  );
  assert.equal(
    context.__test.snapshotToPoint({ pnl_total: 0, arcus: { equity_usd: 999 }, updated_at: isoNow }).equity,
    999,
  );
  // A bull_holder/arcus-shaped target with its own field unavailable
  // reports "no sample" rather than falling through to that meaningless
  // pnl_total zero for its shape.
  assert.equal(context.__test.snapshotToPoint({ pnl_total: 0, bull_holder: { total_equity_usdc: null } }), null);
  assert.equal(context.__test.snapshotToPoint({ bull_holder: { total_equity_usdc: null } }), null);
  assert.equal(context.__test.snapshotToPoint({}), null);
  assert.equal(context.__test.snapshotToPoint(null), null);
});

test("snapshotToPoint stamps bull_holder samples with poll time, not the bot's stale local ts", () => {
  // Regression for Codex review on PR #32: bull_holder's total_equity_usdc
  // is recomputed by the dashboard server from live account queries on
  // every poll (fetchBullHolder), independent of `ts` (the bot's own
  // local status-file heartbeat, which can stay unchanged across many
  // dashboard polls). Using `ts` here would make every distinct equity
  // reading collapse into the same history point via appendHistoryPoint's
  // same-ts overwrite, so the chart would never show a trend.
  const before = Date.now();
  const point = context.__test.snapshotToPoint({ ts: 1700000000, bull_holder: { total_equity_usdc: 100 } });
  const after = Date.now();
  assert.ok(point.ts >= before && point.ts <= after, `expected ts ~now, got ${point.ts}`);

  // Arcus, by contrast, writes equity_usd and ts atomically in one bot
  // write, so its own ts stays trustworthy and must NOT be overridden.
  const arcusPoint = context.__test.snapshotToPoint({ ts: 1700000000, arcus: { equity_usd: 100 } });
  assert.equal(arcusPoint.ts, 1700000000 * 1000);
});

test("holderLastTradeText and arcusLastTradeText answer how/when the bot last traded", () => {
  assert.equal(context.__test.holderLastTradeText({}), "No tranches yet");
  assert.match(
    context.__test.holderLastTradeText({ armed_at: Math.floor(Date.now() / 1000) - 3600 }),
    /^Armed 1h ago/,
  );
  assert.equal(
    context.__test.holderLastTradeText({ last_tranche_date: "2026-09-05", tranches_done: 2, tranches_remaining: 3 }),
    "Last tranche 2026-09-05 UTC · 2 done, 3 left",
  );
  assert.match(
    context.__test.holderLastTradeText({ exited_at: Math.floor(Date.now() / 1000) - 60 }),
    /^Exited /,
  );

  assert.equal(context.__test.arcusLastTradeText({}), "Awaiting first tick");
  assert.equal(context.__test.arcusLastTradeText({ sequence: 5 }), "No swap observed yet");
  assert.match(
    context.__test.arcusLastTradeText({ last_swap_at: new Date(Date.now() - 5000).toISOString() }),
    /^Last swap 5s ago$/,
  );
});

const makeSummaryCard = () => {
  const fields = new Map();
  const field = () => ({ textContent: "", className: "", open: false, hidden: false, innerHTML: "", setAttribute() {} });
  return { querySelector(selector) {
    if (!fields.has(selector)) fields.set(selector, field());
    return fields.get(selector);
  } };
};

test("renderHolderSummary shows the equity/mode/last-trade headline and opens details when degraded", () => {
  const card = makeSummaryCard();
  context.__test.renderHolderSummary(
    card,
    { mode: "On", total_equity_usdc: 1234.5, last_tranche_date: "2026-09-05", tranches_done: 2, tranches_remaining: 3 },
    [],
  );
  assert.equal(card.querySelector('[data-field="holder-equity"]').textContent, "1,234.50 USDC");
  assert.equal(card.querySelector('[data-field="holder-mode-pill"]').textContent, "On");
  assert.equal(card.querySelector('[data-field="holder-mode-pill"]').className, "status-pill active");
  assert.match(card.querySelector('[data-field="holder-last-trade"]').textContent, /Last tranche 2026-09-05 UTC/);
  assert.equal(card.querySelector('[data-field="holder-details"]').open, false);

  context.__test.renderHolderSummary(card, { mode: "On", halted: true, halt_reason: "RISK_ACK required" }, []);
  assert.equal(card.querySelector('[data-field="holder-details"]').open, true);
});

test("renderArcusSummary shows the inventory-equity headline and opens details on risk halt", () => {
  const card = makeSummaryCard();
  context.__test.renderArcusSummary(
    card,
    { mode: "live", equity_usd: 500, healthy: true, last_swap_at: new Date(Date.now() - 60000).toISOString() },
    [],
  );
  assert.equal(card.querySelector('[data-field="arcus-equity"]').textContent, "$500.00");
  assert.equal(card.querySelector('[data-field="arcus-mode-pill"]').className, "status-pill active");
  assert.match(card.querySelector('[data-field="arcus-last-trade"]').textContent, /^Last swap 1m ago$/);
  assert.equal(card.querySelector('[data-field="arcus-details"]').open, false);

  context.__test.renderArcusSummary(card, { mode: "live", healthy: false, risk_halt: { kind: "daily_loss" } }, []);
  assert.equal(card.querySelector('[data-field="arcus-details"]').open, true);
});
