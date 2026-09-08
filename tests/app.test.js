const assert = require("node:assert/strict");
const fs = require("node:fs");
const test = require("node:test");
const vm = require("node:vm");

const source = `${fs.readFileSync(`${__dirname}/../web/app.js`, "utf8")}
globalThis.__test = { renderArcusStatus, isArcusStatus, isStale, renderRiskHistory, isAccumulatorStatus, isTargetUnhealthy, accumulatorViewModel, isHanBridgeStatus, hanBridgeViewModel, isHanBridgeHalted, bullHolderViewModel, renderBullHolderStatus, holderMoney, updateFleetSummary, snapshotToPoint, renderHolderSummary, renderArcusSummary, holderLastTradeText, arcusLastTradeText, isBookStatus, isBookHalted, bookViewModel, snapshotEquityValue, formatPnl, formatUsdc, usdCurrency, formatHype, bucketOf, bucketAggregateStats, BUCKET_LABELS, BUCKET_ORDER, updateHistoryCache, baselineEquityAt, keyForTarget, benchmarkEquityValue, pairedSeries, updateBenchmarkCache, benchmarkByKey, historyByKey, formatSignedUsdc, maxDrawdownPct, calmarRatio, renderHolderBenchmark, snapshotToBenchmarkPoint, renderSubsidyPanel, subsidyCostFallback, costPerUnit, subsidyAggregateStats, renderGatePanel, gateHealthText, blindAlphaCandidate, alphaAggregateStats };`;
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
  assert.equal(value("fleet-positions-total"), "1");
});

test("bull holder uses daily close, distinguishes unknown peaks and pending ADD", () => {
  const fixture = JSON.parse(fs.readFileSync(`${__dirname}/fixtures/bull-holder-status.json`, "utf8"));
  const model = context.__test.bullHolderViewModel({ ...fixture, pending: { ARM: false, ADD: true }, pending_add: 2 });
  assert.equal(model.legs[0].drop, 100 * (1 - 80000 / 100000));
  assert.equal(model.legs[1].drop, null);
  assert.equal(model.pending, "ADD × 2");
  assert.equal(model.total, "—");
  assert.equal(context.__test.holderMoney(0), "0.0 USDC");
  for (const missing of [null, undefined, "", " ", false]) {
    assert.equal(context.__test.holderMoney(missing), "—");
  }
  assert.equal(context.__test.bullHolderViewModel({ total_equity_usdc: null, legs: {} }).total, "—");
  assert.equal(context.__test.holderMoney(1301.004651), "1,301.0 USDC");
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
  assert.match(text(root), /Combined unrealized PnL · estimate\s+-12.0 USDC/);
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

const bookFixture = JSON.parse(
  fs.readFileSync(`${__dirname}/fixtures/book-runtime-status-v1.json`, "utf8"),
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
  assert.match(content, /Cumulative strategy loss\s+\$0.0/);
  assert.match(content, /Starting basket drawdown\s+\$12.0/);
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
    assert.equal(rowValue("Cumulative strategy loss"), "$0.0 / — limit");
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

test("snapshotToPoint stamps bull_holder samples with account observation time, not the bot's stale local ts or a fabricated Date.now()", () => {
  // Regression for Codex review on PR #32 (round 1): bull_holder's
  // total_equity_usdc is recomputed by the dashboard server from live
  // account queries on every server poll cycle (fetchBullHolder),
  // independent of `ts` (the bot's own local status-file heartbeat,
  // which can stay unchanged across many poll cycles). Using `ts` here
  // would make every distinct equity reading collapse into the same
  // history point via appendHistoryPoint's same-ts overwrite.
  const point = context.__test.snapshotToPoint({
    ts: 1700000000,
    bull_holder: { total_equity_usdc: 100, hyperliquid: { observed_at: 1800000000 }, lighter: { observed_at: 1800000060 } },
  });
  // Later of the two account observations (equity sums both accounts).
  assert.equal(point.ts, 1800000060 * 1000);

  // Round 2: Date.now() (an earlier revision of this fix) is also wrong
  // -- /api/status without `range` serves StatusCache.Get() (main.go),
  // so several client polls between server poll cycles receive the
  // exact same cached snapshot. Stamping each with a fresh wall-clock
  // reading would fabricate distinct-looking points for equity that
  // never changed. Feeding the same cached observed_at twice must
  // produce the same ts both times, not two different Date.now() calls.
  const cachedPayload = { bull_holder: { total_equity_usdc: 100, hyperliquid: { observed_at: 1800000000 }, lighter: { observed_at: 1800000000 } } };
  const first = context.__test.snapshotToPoint(cachedPayload);
  const second = context.__test.snapshotToPoint(cachedPayload);
  assert.equal(first.ts, second.ts);
  assert.equal(first.ts, 1800000000 * 1000);

  // Only one account observed (the other errored/unavailable) -- use it.
  assert.equal(
    context.__test.snapshotToPoint({ bull_holder: { total_equity_usdc: 100, lighter: { observed_at: 1800000000 } } }).ts,
    1800000000 * 1000,
  );

  // Neither account has an observation yet -- fall back to now rather
  // than crashing or dropping the sample.
  const before = Date.now();
  const fallback = context.__test.snapshotToPoint({ bull_holder: { total_equity_usdc: 100 } });
  const after = Date.now();
  assert.ok(fallback.ts >= before && fallback.ts <= after, `expected ts ~now, got ${fallback.ts}`);

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

test("renderHolderSummary shows the equity/mode/last-trade headline and opens details when degraded or stale", () => {
  const card = makeSummaryCard();
  context.__test.renderHolderSummary(
    card,
    { mode: "On", total_equity_usdc: 1234.5, last_tranche_date: "2026-09-05", tranches_done: 2, tranches_remaining: 3 },
    [],
    "active",
  );
  assert.equal(card.querySelector('[data-field="holder-equity"]').textContent, "1,234.5 USDC");
  assert.equal(card.querySelector('[data-field="holder-mode-pill"]').textContent, "On");
  assert.equal(card.querySelector('[data-field="holder-mode-pill"]').className, "status-pill active");
  assert.match(card.querySelector('[data-field="holder-last-trade"]').textContent, /Last tranche 2026-09-05 UTC/);
  assert.equal(card.querySelector('[data-field="holder-details"]').open, false);

  context.__test.renderHolderSummary(card, { mode: "On", halted: true, halt_reason: "RISK_ACK required" }, [], "active");
  assert.equal(card.querySelector('[data-field="holder-details"]').open, true);

  // Regression for Codex review on PR #32 (raised for Arcus, applies
  // equally to bull-holder): fetchBullHolder's ServiceStatus goes "stale"
  // purely from local status-file age, independent of halted/*.error, so
  // a hung producer must still force details open even when nothing else
  // reports degraded.
  const staleCard = makeSummaryCard();
  context.__test.renderHolderSummary(staleCard, { mode: "On", total_equity_usdc: 1000 }, [], "stale");
  assert.equal(staleCard.querySelector('[data-field="holder-details"]').open, true);

  // Regression for Codex review on PR #32: an engaged (or pending)
  // KILL_SWITCH is "trouble" the outer card already auto-expands for
  // (updateCard's inTrouble reads target.kill_switch_active, which
  // b.kill_switch mirrors) -- the inner details must open too, or the
  // KILL_SWITCH/operator-request rows stay hidden while the card pops
  // open around them.
  const engagedCard = makeSummaryCard();
  context.__test.renderHolderSummary(engagedCard, { mode: "On", total_equity_usdc: 1000, kill_switch: true }, [], "active");
  assert.equal(engagedCard.querySelector('[data-field="holder-details"]').open, true);

  const pendingCard = makeSummaryCard();
  context.__test.renderHolderSummary(
    pendingCard,
    { mode: "On", total_equity_usdc: 1000, pending: { KILL_SWITCH: true } },
    [],
    "active",
  );
  assert.equal(pendingCard.querySelector('[data-field="holder-details"]').open, true);
});

test("renderArcusSummary shows the inventory-equity headline and opens details on risk halt or staleness", () => {
  const card = makeSummaryCard();
  context.__test.renderArcusSummary(
    card,
    { mode: "live", equity_usd: 500, healthy: true, last_swap_at: new Date(Date.now() - 60000).toISOString() },
    [],
    "active",
  );
  assert.equal(card.querySelector('[data-field="arcus-equity"]').textContent, "$500.0");
  assert.equal(card.querySelector('[data-field="arcus-mode-pill"]').className, "status-pill active");
  assert.match(card.querySelector('[data-field="arcus-last-trade"]').textContent, /^Last swap 1m ago$/);
  assert.equal(card.querySelector('[data-field="arcus-details"]').open, false);

  context.__test.renderArcusSummary(card, { mode: "live", healthy: false, risk_halt: { kind: "daily_loss" } }, [], "active");
  assert.equal(card.querySelector('[data-field="arcus-details"]').open, true);

  // Regression for Codex review on PR #32: Status.ServiceStatus
  // (arcusstatus/status.go) ages the tick/observation/heartbeat clocks
  // independently of `healthy` -- a stale exporter can still carry a
  // frozen healthy=true payload, so `healthy`/`risk_halt` alone would
  // never open details on a hang. Must also check the target's own
  // service_status.
  const staleCard = makeSummaryCard();
  context.__test.renderArcusSummary(staleCard, { mode: "live", healthy: true, risk_halt: null }, [], "stale");
  assert.equal(staleCard.querySelector('[data-field="arcus-details"]').open, true);
});

test("book fixture renders the applied decision, its signal hash and the book's exposure", () => {
  const book = bookFixture.book;
  assert.equal(context.__test.isBookStatus(bookFixture), true);
  assert.equal(context.__test.isBookStatus({}), false);
  assert.equal(context.__test.isBookHalted(bookFixture), false);
  const view = context.__test.bookViewModel(book);
  assert.equal(view.decision, "2026-07-08 applied · d73e8b6f6beb");
  assert.equal(view.signal, "applied d73e8b6f6beb");
  assert.equal(view.signalTone, "ok");
  assert.match(view.exposure, /^gross \$999\.\d+ · net \$9\.\d+ · equity \$1,009\.\d+$/);
  // Book figures are USD, not the holder's USDC formatting.
  assert.ok(!view.exposure.includes("USDC"));
  assert.equal(view.next, "2026-07-13 @ 2026-07-13T00:30:00Z");
  // Nothing wrong with this book, so no note row.
  assert.equal(view.note, null);
});

test("book halts, a blocked-equity outage and a pending residual all surface", () => {
  const halted = {
    ...bookFixture.book,
    session_halted: true,
    session_halt_reason: "session loss $160.00 > limit $150.00",
  };
  assert.equal(context.__test.isBookHalted({ book: halted }), true);
  assert.match(context.__test.bookViewModel(halted).note, /session loss/);

  const daily = { ...bookFixture.book, daily_halted: true };
  assert.equal(context.__test.isBookHalted({ book: daily }), true);
  assert.equal(context.__test.bookViewModel(daily).note, "daily loss halt");

  // A live equity outage blocks every opening intent, so it must count as
  // trouble even though neither halt flag is set.
  const noEquity = { ...bookFixture.book, equity_ready: false };
  assert.equal(context.__test.isBookHalted({ book: noEquity }), true);
  assert.match(context.__test.bookViewModel(noEquity).note, /equity unavailable/);

  const residual = { ...bookFixture.book, pending_residual: true, signal_status: "partial:abc123def456" };
  // A residual is not a halt: the runtime is still working the window.
  assert.equal(context.__test.isBookHalted({ book: residual }), false);
  const view = context.__test.bookViewModel(residual);
  assert.equal(view.signalTone, "warn");
  assert.equal(view.note, "residual pending");
});

test("book equity comes from book.equity_usd, not the top-level pnl_total", () => {
  // pnl_total on this fixture is ~$9.93 (PnL against the reference) while
  // the capital is ~$1009.93; the fleet total and the equity chart must
  // use the latter.
  assert.ok(bookFixture.pnl_total < 100);
  assert.equal(
    context.__test.snapshotToPoint(bookFixture).equity,
    bookFixture.book.equity_usd,
  );
});

test("month-to-date uses the same measure on both sides for a book target", () => {
  // Two samples of the same book: the baseline cached at month start and
  // the current one are both book.equity_usd, so MTD is their difference
  // (+$10), not pnl_total minus equity (~-$1000).
  const earlier = JSON.parse(JSON.stringify(bookFixture));
  earlier.book.equity_usd = bookFixture.book.equity_usd - 10;
  assert.equal(
    context.__test.snapshotEquityValue(bookFixture) -
      context.__test.snapshotEquityValue(earlier),
    10,
  );
  // The card's prominent "Equity total" is capital, not PnL.
  assert.equal(context.__test.snapshotEquityValue(bookFixture), bookFixture.book.equity_usd);
  assert.ok(bookFixture.pnl_total < 100);
});

test("a book's legs are counted whole while pairtrade's are still halved", () => {
  context.__test.updateFleetSummary([
    // One pairtrade target: 2 legs = 1 pair.
    { service_status: "active", status: { pnl_total: 100, pnl_today: 5, position_count: 2 } },
    // One book target: 4 single-symbol legs, counted whole.
    { service_status: "active", status: bookFixture },
  ]);
  const value = (name) => fleetFields.get(`[data-field="${name}"]`).textContent;
  assert.equal(bookFixture.position_count, 4);
  assert.equal(value("fleet-positions-total"), "5");
});

test("a book with no decision yet and an unfinished flatten reads correctly", () => {
  const fresh = { ...bookFixture.book, last_decision: null, signal_status: "waiting_for_file" };
  const view = context.__test.bookViewModel(fresh);
  assert.equal(view.decision, "None yet");
  assert.equal(view.signal, "waiting_for_file");
  assert.equal(view.signalTone, "neutral");

  // A rejected decision keeps its reason visible even after the Signal
  // row has moved on to the next window.
  const rejected = {
    ...bookFixture.book,
    signal_status: "waiting_for_file",
    last_decision: {
      ...bookFixture.book.last_decision,
      outcome: "rejected",
      signal_sha256: null,
      reject_reason: "unknown_symbol",
    },
  };
  assert.equal(
    context.__test.bookViewModel(rejected).decision,
    "2026-07-08 rejected · unknown_symbol",
  );

  // A decision whose signal hash is absent still renders its key/outcome.
  const noSha = { ...bookFixture.book, last_decision: { ...bookFixture.book.last_decision, signal_sha256: null } };
  assert.equal(context.__test.bookViewModel(noSha).decision, "2026-07-08 applied");

  const owed = {
    ...bookFixture.book,
    last_decision: { ...bookFixture.book.last_decision, flatten_at: 1783497600, flatten_done: false },
  };
  assert.match(context.__test.bookViewModel(owed).note, /flatten of 2026-07-08 pending/);
});

test("book target counts as unhealthy while halted", () => {
  assert.equal(
    context.__test.isTargetUnhealthy({ service_status: "active", status: bookFixture }),
    false,
  );
  assert.equal(
    context.__test.isTargetUnhealthy({
      service_status: "active",
      status: { book: { ...bookFixture.book, session_halted: true } },
    }),
    true,
  );
});

test("money and quantity formatters share one precision across panels", () => {
  const f = context.__test;
  // Every money figure: 1 decimal, thousands grouped, no -0.
  assert.equal(f.formatUsdc(1301.004651), "1,301.0 USDC");
  assert.equal(f.holderMoney(1301.004651), "1,301.0 USDC");
  assert.equal(f.usdCurrency(1301.004651), "$1,301.0");
  assert.equal(f.formatPnl(1234.56), "+1,234.6");
  assert.equal(f.formatPnl(-0), "0.0");
  assert.equal(f.formatPnl(null), "-");
  // Token quantities: up to 4 decimals, trailing zeros dropped.
  assert.equal(f.formatHype(27.123456), "27.1235 HYPE");
  assert.equal(f.formatHype(2.5), "2.5 HYPE");
});

test("bucket classification falls back to unclassified instead of inventing a group", () => {
  assert.equal(context.__test.bucketOf({ bucket: "beta" }), "beta");
  assert.equal(context.__test.bucketOf({ bucket: " subsidy " }), "subsidy");
  // Older server (no field), a value this frontend doesn't know, and a
  // target object with nothing at all all land in the same visible group
  // rather than silently disappearing.
  assert.equal(context.__test.bucketOf({}), "unclassified");
  assert.equal(context.__test.bucketOf({ bucket: "carry" }), "unclassified");
  assert.equal(context.__test.bucketOf(null), "unclassified");
  // Every bucket the API can send has a label and a place in the order.
  for (const bucket of context.__test.BUCKET_ORDER) {
    assert.ok(context.__test.BUCKET_LABELS[bucket]);
  }
  assert.equal(
    Object.keys(context.__test.BUCKET_LABELS).sort().join(","),
    [...context.__test.BUCKET_ORDER].sort().join(","),
  );
});

test("beta bucket totals held value across bot shapes and never sums across buckets", () => {
  const items = [
    // Bull-holder: equity lives under bull_holder.total_equity_usdc.
    { target: { name: "Bull-holder", bucket: "beta", status: { pnl_total: 0, bull_holder: { total_equity_usdc: 1000 } } }, index: 0 },
    // Accumulator: a reconciled asset balance, and a pnl_total of 0 that
    // must not win over it.
    { target: { name: "HYPE", bucket: "beta", status: { pnl_total: 0, accumulator: { total_equity_usdc: 250.5 } } }, index: 1 },
    // Equity unavailable (venue read failed) — skipped, not counted as 0.
    { target: { name: "Broken", bucket: "beta", status: { bull_holder: { total_equity_usdc: null } } }, index: 2 },
  ];
  const stats = context.__test.bucketAggregateStats("beta", items);
  const stat = (label) => stats.find((s) => s.label === label);
  assert.equal(stat("Equity held").value, "1,250.5 USDC");
  // No cached history in this context, so month-to-date is unknown, and
  // the benchmark column is not wired up until bot-strategy#955.
  assert.equal(stat("MTD change").value, "-");
  assert.equal(stat("vs buy & hold").value, "-");
  // Other buckets get their aggregates from their own issues (#957/#958);
  // until then they must render no numbers at all rather than a total
  // borrowed from another bucket.
  assert.equal(context.__test.bucketAggregateStats("unclassified", items).length, 0);
  // The α bucket reports study progress, never a figure borrowed from
  // another bucket's equity.
  const asAlpha = context.__test.bucketAggregateStats("alpha_candidate", items);
  assert.equal(asAlpha.find((s) => s.label === "Nearest readout").value, "-");
  // β-shaped payloads carry no subsidy cost, so the subsidy aggregate
  // reports nothing rather than borrowing their equity.
  const asSubsidy = context.__test.bucketAggregateStats("subsidy", items);
  assert.equal(asSubsidy.length, 1);
  assert.equal(asSubsidy[0].value, "-");
});

test("MTD stays unavailable until a recorded month-start baseline exists", () => {
  // A beta target the server has no equity_history for (bull-holder):
  // updateCard seeds the cache with the current observation, so the only
  // point is one this page just made. Treating it as the baseline made
  // MTD read 0.0 on every reload and change-since-page-load after that
  // (PR #36 review).
  const target = { name: "holder", service: "debot-bull-holder", instance_id: "i-1", bucket: "beta" };
  const key = context.__test.keyForTarget(target, 0);
  const data = { bull_holder: { total_equity_usdc: 1250.5 }, updated_at: new Date().toISOString() };
  context.__test.updateHistoryCache(key, data);
  const items = [{ target: { ...target, status: data }, index: 0 }];
  const stat = (stats, label) => stats.find((s) => s.label === label);
  const snapshotOnly = context.__test.bucketAggregateStats("beta", items);
  assert.equal(stat(snapshotOnly, "Equity held").value, "1,250.5 USDC");
  assert.equal(stat(snapshotOnly, "MTD change").value, "-");
  assert.equal(stat(snapshotOnly, "MTD change").signed, null);

  // Once the server sends a recorded series that reaches back before the
  // month rollover, the delta is real and gets rendered.
  const now = new Date();
  const monthStart = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1);
  context.__test.updateHistoryCache(key, {
    ...data,
    equity_history: [
      { ts: monthStart - 86400000, equity: 1000 },
      { ts: monthStart + 3600000, equity: 1100 },
    ],
  });
  const recorded = context.__test.bucketAggregateStats("beta", items);
  assert.equal(stat(recorded, "MTD change").signed, 250.5);

  // A series that only begins inside the month is still a baseline when
  // it is the server's own record (bot provisioned mid-month).
  const key2 = context.__test.keyForTarget({ ...target, name: "holder2" }, 1);
  context.__test.updateHistoryCache(key2, {
    ...data,
    equity_history: [{ ts: monthStart + 3600000, equity: 1200 }],
  });
  const items2 = [{ target: { ...target, name: "holder2", status: data }, index: 1 }];
  assert.equal(stat(context.__test.bucketAggregateStats("beta", items2), "MTD change").signed, 50.5);

  // A bucket where only some targets have a baseline must not show a
  // partial delta beside an "Equity held" that counts them all
  // (PR #36 review round 2).
  const mixed = [
    { target: { ...target, name: "holder2", status: data }, index: 1 },   // recorded
    { target: { ...target, name: "holder3", status: data }, index: 2 },   // snapshot only
  ];
  context.__test.updateHistoryCache(context.__test.keyForTarget({ ...target, name: "holder3" }, 2), data);
  const partial = context.__test.bucketAggregateStats("beta", mixed);
  assert.equal(stat(partial, "Equity held").value, "2,501.0 USDC");
  assert.equal(stat(partial, "MTD change").value, "-");
  assert.equal(stat(partial, "MTD change").signed, null);

  // The predicate itself: identical series, opposite answers.
  const inMonth = [{ ts: monthStart + 1000, equity: 42 }];
  assert.equal(context.__test.baselineEquityAt(inMonth, monthStart, true), 42);
  assert.equal(context.__test.baselineEquityAt(inMonth, monthStart, false), null);
  assert.equal(context.__test.baselineEquityAt(inMonth, monthStart), null);
});

test("accumulator equity is the held balance, not its constant-zero pnl_total", () => {
  assert.equal(
    context.__test.snapshotEquityValue({ pnl_total: 0, accumulator: { total_equity_usdc: 987.65 } }),
    987.65,
  );
  assert.equal(
    context.__test.snapshotEquityValue({ pnl_total: 0, accumulator: { total_equity_usdc: null } }),
    null,
  );
});

// A card stub good enough for the benchmark renderer: it only ever
// touches [data-field] leaves, and each must report what it was given.
const benchmarkCard = () => {
  const fields = new Map();
  return {
    fields,
    querySelector(selector) {
      if (!fields.has(selector)) {
        fields.set(selector, {
          textContent: "",
          hidden: false,
          title: "",
          removeAttribute() { this.title = ""; },
          classList: { toggle() {}, add() {}, remove() {} },
        });
      }
      return fields.get(selector);
    },
    text(name) {
      return this.querySelector(`[data-field="${name}"]`).textContent;
    },
  };
};

// 1h apart so both series clear the 7-day CAGR gate used by Calmar.
const series = (values, startMs = Date.UTC(2026, 0, 1)) =>
  values.map((equity, i) => ({ ts: startMs + i * 24 * 60 * 60 * 1000, equity }));

test("benchmark rows compare bot and buy & hold, and say nothing when the anchor is missing", () => {
  const card = benchmarkCard();
  const holder = {
    total_equity_usdc: 1100,
    benchmark: { anchor_ts: 1788000000, cost_usd: 900, cash_usd: 100, equity_usd: 1000 },
    cum_funding_usdc: -12.5,
    cum_fees_usdc: -2.5,
  };
  // Bot 1000 → 1100 with a dip to 950 (5% DD); benchmark 1000 → 1000
  // with a dip to 800 (20% DD): the shallower-drawdown claim, visible.
  const botHistory = series([1000, 950, 1100]);
  const benchHistory = series([1000, 800, 1000]);
  context.__test.renderHolderBenchmark(card, holder, 1100, botHistory, benchHistory);
  assert.equal(card.text("holder-bench-excess"), "+100.0 USDC (10.0%)");
  assert.equal(card.text("holder-bench-dd"), "5.0% / 20.0%");
  assert.equal(card.text("holder-bench-costs"), "-15.0 USDC");
  assert.equal(card.querySelector('[data-field="holder-bench-note"]').hidden, true);

  // No anchor configured: every derived row goes to "-" and the card
  // says why instead of quietly comparing against nothing.
  const bare = benchmarkCard();
  context.__test.renderHolderBenchmark(
    bare,
    { total_equity_usdc: 1100, benchmark: null, benchmark_error: "Buy & hold anchor not configured" },
    1100,
    botHistory,
    [],
  );
  assert.equal(bare.text("holder-bench-excess"), "-");
  assert.equal(bare.text("holder-bench-calmar"), "-");
  assert.equal(bare.text("holder-bench-costs"), "-");
  assert.equal(bare.text("holder-bench-note"), "Buy & hold anchor not configured");

  // A target that had a benchmark and lost it (a producer config_fp
  // change makes the snapshot unverifiable) keeps a cached benchmark
  // series while its own equity keeps growing. Comparing them would put
  // two windows with different ends side by side and show stale
  // benchmark statistics next to the "unavailable" note.
  const lost = benchmarkCard();
  context.__test.renderHolderBenchmark(
    lost,
    { total_equity_usdc: 1100, benchmark: null, benchmark_error: "Startup investment snapshot does not match producer configuration" },
    1100,
    botHistory,
    benchHistory,
  );
  assert.equal(lost.text("holder-bench-dd"), "5.0% / -");
  assert.equal(lost.text("holder-bench-excess"), "-");
  assert.equal(lost.text("holder-bench-note"), "Startup investment snapshot does not match producer configuration");
  assert.equal(bare.querySelector('[data-field="holder-bench-note"]').hidden, false);
});

test("drawdown and Calmar are windowed ratios, not currency, and refuse to divide by nothing", () => {
  assert.equal(context.__test.maxDrawdownPct(series([100, 80, 90])), 20);
  // Monotonic series never drew down, so Calmar is undefined rather than
  // infinitely good.
  assert.equal(context.__test.maxDrawdownPct(series([100, 110])), 0);
  assert.equal(context.__test.calmarRatio(series([100, 110])), null);
  assert.equal(context.__test.maxDrawdownPct([]), null);
  assert.equal(context.__test.maxDrawdownPct(series([100])), null);
  // Under 7 days of history computeStats withholds CAGR, so Calmar
  // cannot be computed even though the drawdown is known.
  const short = [
    { ts: Date.UTC(2026, 0, 1), equity: 100 },
    { ts: Date.UTC(2026, 0, 2), equity: 80 },
  ];
  assert.equal(context.__test.maxDrawdownPct(short), 20);
  assert.equal(context.__test.calmarRatio(short), null);

  // Past the 7-day gate the ratio is real, and a deeper drawdown for the
  // same end-to-end return scores worse — the whole point of ranking a β
  // bot against buy & hold on Calmar rather than on return alone.
  const shallow = series([100, 95, 100, 105, 105, 105, 105, 110, 121]);
  const deep = series([100, 70, 100, 105, 105, 105, 105, 110, 121]);
  const shallowCalmar = context.__test.calmarRatio(shallow);
  const deepCalmar = context.__test.calmarRatio(deep);
  assert.ok(Number.isFinite(shallowCalmar) && shallowCalmar > 0);
  assert.ok(shallowCalmar > deepCalmar);
});

test("benchmark series is cached on the same timestamps as the equity series", () => {
  const data = {
    ts: 1788600000,
    bull_holder: {
      total_equity_usdc: 1100,
      benchmark: { equity_usd: 1000 },
      hyperliquid: { observed_at: 1788600060 },
      lighter: { observed_at: 1788600120 },
    },
  };
  const equityPoint = context.__test.snapshotToPoint(data);
  const benchmarkPoint = context.__test.snapshotToBenchmarkPoint(data);
  assert.equal(equityPoint.ts, 1788600120 * 1000);
  assert.equal(benchmarkPoint.ts, equityPoint.ts);
  assert.equal(benchmarkPoint.equity, 1000);
  // No benchmark on the payload → no point, so the two series never
  // drift onto different timestamps.
  assert.equal(context.__test.snapshotToBenchmarkPoint({ bull_holder: { total_equity_usdc: 1100 } }), null);
  assert.equal(context.__test.benchmarkEquityValue({ bull_holder: { benchmark: { equity_usd: null } } }), null);
});

test("a changed benchmark anchor restarts both series instead of splicing books", () => {
  const key = "anchor-test";
  const book = (anchorTs, equity, observedAt) => ({
    ts: observedAt,
    bull_holder: {
      total_equity_usdc: 1000,
      benchmark: { anchor_ts: anchorTs, cost_usd: 900, cash_usd: 100, equity_usd: equity },
      hyperliquid: { observed_at: observedAt },
      lighter: { observed_at: observedAt },
    },
  });
  context.__test.updateBenchmarkCache(key, book(1000, 1000, 1_700_000_000));
  context.__test.updateBenchmarkCache(key, book(1000, 1100, 1_700_000_060));
  assert.equal(context.__test.benchmarkByKey.get(key).length, 2);
  context.__test.historyByKey.set(key, [{ ts: 1, equity: 1 }, { ts: 2, equity: 2 }]);

  // The first benchmark a page sees also restarts the equity series:
  // points cached while the anchor was unconfigured reach back before
  // the benchmark series starts, and comparing a long bot window against
  // a short benchmark one overstates the bot's drawdown.
  const firstKey = "first-benchmark";
  context.__test.historyByKey.set(firstKey, [{ ts: 1, equity: 1 }, { ts: 2, equity: 2 }]);
  context.__test.updateBenchmarkCache(firstKey, book(1000, 1000, 1_700_000_000));
  assert.equal(context.__test.historyByKey.get(firstKey), undefined);

  // A later rollout re-anchors the book under the same target key.
  // Appending its values to the old book's series would show a jump that
  // never happened, so both series restart together.
  // Correcting an anchor price without touching its timestamp or the
  // allocation is also a different book, and the old series must not
  // carry into it.
  const priced = context.__test.updateBenchmarkCache(key, {
    ts: 1_700_000_090,
    bull_holder: {
      total_equity_usdc: 1000,
      benchmark: {
        anchor_ts: 1000,
        cost_usd: 900,
        cash_usd: 100,
        equity_usd: 1050,
        assets: [{ symbol: "BTC", anchor_price_usd: 51000 }],
      },
      hyperliquid: { observed_at: 1_700_000_090 },
      lighter: { observed_at: 1_700_000_090 },
    },
  });
  assert.equal(priced.length, 1);

  const after = context.__test.updateBenchmarkCache(key, book(2000, 400, 1_700_000_120));
  assert.equal(after.length, 1);
  assert.equal(after[0].equity, 400);
  assert.equal(context.__test.historyByKey.get(key), undefined);
});

test("drawdowns are compared only over observations both series share", () => {
  const botHistory = series([1000, 950, 1100]);
  // The benchmark kept being priced through an outage that stalled the
  // bot's own equity: it carries a fourth sample the bot never recorded,
  // and a 50% collapse inside it. Counting that tick on one side only
  // would attribute an outage-period move to the benchmark alone.
  const benchHistory = [
    ...series([1000, 800, 1000]),
    { ts: Date.UTC(2026, 0, 1) + 3 * 24 * 60 * 60 * 1000, equity: 500 },
  ];
  const card = benchmarkCard();
  context.__test.renderHolderBenchmark(
    card,
    { total_equity_usdc: 1100, benchmark: { anchor_ts: 1, cost_usd: 900, cash_usd: 100, equity_usd: 1000 } },
    1100,
    botHistory,
    benchHistory,
  );
  assert.equal(card.text("holder-bench-dd"), "5.0% / 20.0%");

  // The current excess still needs both current values; the historical
  // comparison over shared points stays valid without them.
  const noEquity = benchmarkCard();
  context.__test.renderHolderBenchmark(
    noEquity,
    { total_equity_usdc: null, benchmark: { anchor_ts: 1, cost_usd: 900, cash_usd: 100, equity_usd: 1000 } },
    null,
    botHistory,
    benchHistory,
  );
  assert.equal(noEquity.text("holder-bench-excess"), "-");
  assert.equal(noEquity.text("holder-bench-dd"), "5.0% / 20.0%");
});

test("beta aggregate sums excess only over targets that actually have a benchmark", () => {
  const items = [
    { target: { bucket: "beta", status: { bull_holder: { total_equity_usdc: 1100, benchmark: { equity_usd: 1000 } } } }, index: 0 },
    // Anchor not configured: contributes its equity but no excess.
    { target: { bucket: "beta", status: { accumulator: { total_equity_usdc: 500 } } }, index: 1 },
  ];
  const stats = context.__test.bucketAggregateStats("beta", items);
  const stat = (label) => stats.find((s) => s.label === label);
  assert.equal(stat("Equity held").value, "1,600.0 USDC");
  assert.equal(stat("vs buy & hold").value, "+100.0 USDC");
  assert.match(stat("vs buy & hold").title, /1 of 2 targets/);
  assert.equal(
    context.__test.bucketAggregateStats("beta", [items[1]]).find((s) => s.label === "vs buy & hold").value,
    "-",
  );
});

test("subsidy card leads with cost per unit and never manufactures a denominator", () => {
  const card = benchmarkCard();
  const target = {
    bucket: "subsidy",
    subsidy_kpi: { unit: "points", imputed_unit_value_usd: 0.05, value_source_date: "2026-09-08", stale: false },
    status: {
      subsidy: {
        unit: "points",
        units_total: 20000,
        units_7d: 1000,
        cost_total_usd: 210,
        cost_7d_usd: 14,
        as_of_ts: Math.floor(Date.now() / 1000) - 3600,
      },
      trade_stats: { pnl: -180 },
    },
  };
  context.__test.renderSubsidyPanel(card, target, target.status);
  assert.equal(card.querySelector('[data-field="subsidy-panel"]').hidden, false);
  assert.equal(card.text("subsidy-cpu-7d"), "0.0140 USDC / points");
  assert.equal(card.text("subsidy-cpu-total"), "0.0105 USDC / points");
  assert.equal(card.text("subsidy-units"), "20,000.00 points");
  // The bot's own ledger wins over the fallback derived from trade_stats.
  assert.equal(card.text("subsidy-cost"), "210.0 USDC");
  assert.equal(card.text("subsidy-value"), "1,000.0 USDC (as of 2026-09-08)");
  assert.equal(card.querySelector('[data-field="subsidy-note"]').hidden, true);

  // No ledger yet: cost is still sourced, but nothing is divided by a
  // denominator that does not exist.
  const pending = benchmarkCard();
  context.__test.renderSubsidyPanel(
    pending,
    { bucket: "subsidy", subsidy_kpi: { unit: "points", stale: false } },
    { trade_stats: { pnl: -180 } },
  );
  assert.equal(pending.text("subsidy-cpu-total"), "-");
  assert.equal(pending.text("subsidy-units"), "-");
  assert.equal(pending.text("subsidy-cost"), "180.0 USDC");
  assert.equal(pending.text("subsidy-value"), "-");
  assert.match(pending.text("subsidy-note"), /bot-strategy#938/);

  // A target with no configured KPI keeps the panel out of the way.
  const other = benchmarkCard();
  context.__test.renderSubsidyPanel(other, { bucket: "beta" }, { pnl_total: 10 });
  assert.equal(other.querySelector('[data-field="subsidy-panel"]').hidden, true);
});

test("subsidy card ages the ledger separately from the status object", () => {
  const kpi = { unit: "points", stale: false };
  const fresh = benchmarkCard();
  const now = Math.floor(Date.now() / 1000);
  context.__test.renderSubsidyPanel(fresh, { subsidy_kpi: kpi }, {
    subsidy: { unit: "points", units_total: 100, units_7d: 10, cost_total_usd: 10, cost_7d_usd: 1, as_of_ts: now - 3600 },
  });
  assert.notEqual(fresh.text("subsidy-as-of"), "-");
  assert.equal(fresh.querySelector('[data-field="subsidy-note"]').hidden, true);

  // The status object refreshes every minute; the ledger is daily. A
  // ledger that stopped three days ago still renders units and costs,
  // and without its own age they read as current.
  const stalled = benchmarkCard();
  context.__test.renderSubsidyPanel(stalled, { subsidy_kpi: kpi }, {
    subsidy: { unit: "points", units_total: 100, units_7d: 10, cost_total_usd: 10, cost_7d_usd: 1, as_of_ts: now - 3 * 86400 },
  });
  assert.match(stalled.text("subsidy-note"), /has not been written/);

  // An out-of-range epoch is still a valid int64 on the wire, and
  // formatting it throws a RangeError that would take the whole render
  // down. It is treated as no timestamp at all.
  const bogus = benchmarkCard();
  context.__test.renderSubsidyPanel(bogus, { subsidy_kpi: kpi }, {
    subsidy: { unit: "points", units_total: 100, units_7d: 10, cost_total_usd: 10, cost_7d_usd: 1, as_of_ts: 9e18 },
  });
  assert.equal(bogus.text("subsidy-as-of"), "-");
  assert.match(bogus.text("subsidy-note"), /does not report when it was written/);

  // A ledger that prices the cost before it can count units: the cost is
  // the ledger's, not the fallback's, and the note must not say otherwise.
  const partial = benchmarkCard();
  context.__test.renderSubsidyPanel(partial, { subsidy_kpi: kpi }, {
    subsidy: { unit: "points", cost_total_usd: 10, as_of_ts: now - 3600 },
    trade_stats: { pnl: -999 },
  });
  assert.equal(partial.text("subsidy-cost"), "10.0 USDC");
  assert.match(partial.text("subsidy-note"), /reports cost but not units/);

  // A ledger with no timestamp cannot be aged at all, which is its own
  // thing to say rather than silently passing as fresh.
  const undated = benchmarkCard();
  context.__test.renderSubsidyPanel(undated, { subsidy_kpi: kpi }, {
    subsidy: { unit: "points", units_total: 100, units_7d: 10, cost_total_usd: 10, cost_7d_usd: 1 },
  });
  assert.equal(undated.text("subsidy-as-of"), "-");
  assert.match(undated.text("subsidy-note"), /does not report when it was written/);
});

test("a program change marks the KPI stale on the card until it is reviewed", () => {
  const card = benchmarkCard();
  context.__test.renderSubsidyPanel(
    card,
    { bucket: "subsidy", subsidy_kpi: { unit: "points", stale: true, stale_since: "2026-09-01" } },
    { trade_stats: { pnl: -10 } },
  );
  const pill = card.querySelector('[data-field="kpi-stale"]');
  assert.equal(pill.hidden, false);
  assert.equal(pill.textContent, "KPI STALE since 2026-09-01");

  const fresh = benchmarkCard();
  context.__test.renderSubsidyPanel(
    fresh,
    { bucket: "subsidy", subsidy_kpi: { unit: "points", stale: false } },
    { trade_stats: { pnl: -10 } },
  );
  assert.equal(fresh.querySelector('[data-field="kpi-stale"]').hidden, true);
});

test("cost falls back to the bot's own net result, in the right direction", () => {
  // Arcus values its initial basket at current prices, so this is
  // already the price paid rather than a price move.
  assert.equal(context.__test.subsidyCostFallback({ arcus: { cumulative_loss_usd: 42 } }), 42);
  // cumulative_loss_usd is floored at zero for the risk limits, so an
  // Arcus run that came out ahead would report a cost of exactly zero.
  // The signed field wins wherever the exporter provides it.
  assert.equal(
    context.__test.subsidyCostFallback({ arcus: { cumulative_loss_usd: 0, cumulative_cost_usd: -12 } }),
    -12,
  );
  // A losing subsidy bot has a positive cost; a bot that came out ahead
  // has a negative one.
  assert.equal(context.__test.subsidyCostFallback({ trade_stats: { pnl: -180 } }), 180);
  assert.equal(context.__test.subsidyCostFallback({ trade_stats: { pnl: 20 } }), -20);
  assert.equal(context.__test.subsidyCostFallback({ pnl_total: 1000 }), null);
  assert.equal(context.__test.costPerUnit(100, 0), null);
  assert.equal(context.__test.costPerUnit(100, null), null);
});

test("subsidy aggregate totals cost across the bucket but keeps units apart by unit", () => {
  const items = [
    {
      target: {
        subsidy_kpi: { unit: "points" },
        status: { subsidy: { unit: "points", units_total: 10000, cost_total_usd: 100 } },
      },
      index: 0,
    },
    {
      target: {
        subsidy_kpi: { unit: "points" },
        status: { subsidy: { unit: "points", units_total: 10000, cost_total_usd: 50 } },
      },
      index: 1,
    },
    {
      // Different unit, and no ledger: contributes cost only.
      target: { subsidy_kpi: { unit: "USD activity" }, status: { arcus: { cumulative_loss_usd: 30 } } },
      index: 2,
    },
  ];
  const stats = context.__test.bucketAggregateStats("subsidy", items);
  const stat = (label) => stats.find((s) => s.label === label);
  assert.equal(stat("Cost paid").value, "180.0 USDC");
  assert.equal(stat("Units (points)").value, "20,000.00 points");
  // Points cost 150 of the 180: the Arcus spend must not be priced into
  // thepoints  denominator.
  assert.equal(stat("Cost / points").value, "0.0075 USDC / points");
  assert.equal(stat("Units (USD activity)"), undefined);
});

test("a same-unit target reporting cost without units withholds the ratio", () => {
  const stats = context.__test.bucketAggregateStats("subsidy", [
    {
      target: {
        subsidy_kpi: { unit: "points" },
        status: { subsidy: { unit: "points", units_total: 10000, cost_total_usd: 100 } },
      },
      index: 0,
    },
    {
      // Same unit, cost priced, units not counted yet. Dropping it would
      // leave a ratio that divides both costs' worth of spending by only
      // one target's units.
      target: {
        subsidy_kpi: { unit: "points" },
        status: { subsidy: { unit: "points", cost_total_usd: 50 } },
      },
      index: 1,
    },
  ]);
  assert.equal(stats.find((s) => s.label === "Cost paid").value, "150.0 USDC");
  assert.equal(stats.find((s) => s.label === "Cost / points").value, "-");
  assert.match(stats.find((s) => s.label === "Units (points)").value, /partial/);
});

test("an unreadable or invalidated same-unit target makes its unit partial", () => {
  const good = {
    target: {
      subsidy_kpi: { unit: "points" },
      status: { subsidy: { unit: "points", units_total: 10000, cost_total_usd: 100 } },
    },
    index: 0,
  };
  // The S3 read failed: the target is still in the bucket, and a ratio
  // over the rest presented as the bucket's own would be wrong.
  const unreadable = context.__test.bucketAggregateStats("subsidy", [
    good,
    { target: { subsidy_kpi: { unit: "points" } }, index: 1 },
  ]);
  assert.equal(unreadable.find((s) => s.label === "Cost / points").value, "-");

  // The venue changed the program and nobody has re-reviewed: the card
  // says so, and the bucket header must not launder those figures into
  // an apparently current ratio.
  const stale = context.__test.bucketAggregateStats("subsidy", [
    good,
    {
      target: {
        subsidy_kpi: { unit: "points", stale: true, stale_since: "2026-09-01" },
        status: { subsidy: { unit: "points", units_total: 500, cost_total_usd: 5 } },
      },
      index: 1,
    },
  ]);
  assert.equal(stale.find((s) => s.label === "Cost / points").value, "-");
});

test("cost per unit needs both sides from the same window", () => {
  // The ledger counts units but does not price them. The fallback cost
  // is current while the units are as of the ledger's older write, so
  // dividing one by the other spreads fees accrued since over yesterday's
  // units. The cumulative cost still shows.
  const card = benchmarkCard();
  context.__test.renderSubsidyPanel(
    card,
    { subsidy_kpi: { unit: "points", stale: false } },
    {
      subsidy: { unit: "points", units_total: 10000, as_of_ts: Math.floor(Date.now() / 1000) - 3600 },
      trade_stats: { pnl: -180 },
    },
  );
  assert.equal(card.text("subsidy-cpu-total"), "-");
  assert.equal(card.text("subsidy-units"), "10,000.00 points");
  assert.equal(card.text("subsidy-cost"), "180.0 USDC");
  assert.match(card.text("subsidy-note"), /older window/);
});

test("a same-unit target with no ledger at all also withholds the ratio", () => {
  const stats = context.__test.bucketAggregateStats("subsidy", [
    {
      target: {
        subsidy_kpi: { unit: "points" },
        status: { subsidy: { unit: "points", units_total: 10000, cost_total_usd: 100 } },
      },
      index: 0,
    },
    {
      // The state every subsidy target is in until bot-strategy#938
      // ships, and the state one arm is in while the other already has a
      // ledger: no subsidy block, but a cost from the fallback.
      target: { subsidy_kpi: { unit: "points" }, status: { trade_stats: { pnl: -50 } } },
      index: 1,
    },
  ]);
  assert.equal(stats.find((s) => s.label === "Cost paid").value, "150.0 USDC");
  assert.equal(stats.find((s) => s.label === "Cost / points").value, "-");
  assert.match(stats.find((s) => s.label === "Units (points)").value, /partial/);
});

test("units the server treats as one unit are aggregated as one row", () => {
  // usableSubsidyUnits matches the bot's unit against the configured one
  // case-insensitively and trimmed, so the aggregate has to key the same
  // way or two spellings of the same unit split into two uncombinable
  // rows, each with a cost per unit computed over half the units.
  const stats = context.__test.bucketAggregateStats("subsidy", [
    {
      target: {
        subsidy_kpi: { unit: "points" },
        status: { subsidy: { unit: "points", units_total: 10000, cost_total_usd: 100 } },
      },
      index: 0,
    },
    {
      target: {
        subsidy_kpi: { unit: " Points " },
        status: { subsidy: { unit: "Points", units_total: 10000, cost_total_usd: 50 } },
      },
      index: 1,
    },
  ]);
  const unitRows = stats.filter((s) => s.label.startsWith("Units ("));
  assert.equal(unitRows.length, 1);
  assert.equal(unitRows[0].value, "20,000.00 points");
  assert.equal(stats.find((s) => s.label === "Cost / points").value, "0.0075 USDC / points");
});

test("alpha candidate card shows gate progress and nothing that could be peeked at", () => {
  const card = benchmarkCard();
  const target = {
    bucket: "alpha_candidate",
    gate: {
      spec_hash: "a1b2c3d4e5f6a1b2",
      required_samples: 60,
      readout_on: "2026-10-02",
      days_to_readout: 24,
      readout_due: false,
      valid_samples: 12,
      sample_source: "book decision journal",
      spec_drift: false,
    },
    status: {},
  };
  const data = {
    gate: { spec_hash: "a1b2c3d4e5f6a1b2", valid_samples: 12 },
    book: { last_decision: { key: "2026-09-08T00:30Z", outcome: "applied" } },
  };
  context.__test.renderGatePanel(card, target, data);
  assert.equal(card.querySelector('[data-field="gate-panel"]').hidden, false);
  assert.equal(card.text("gate-samples"), "12 / 60");
  assert.equal(card.text("gate-readout"), "2026-10-02 (24d)");
  // The spec hash is shown truncated, like every other hash on the card.
  assert.equal(card.text("gate-spec"), "a1b2c3d4e5f6");
  assert.equal(card.text("gate-health"), "Sampling normally");
  assert.equal(card.querySelector('[data-field="readout-due"]').hidden, true);
  assert.equal(card.querySelector('[data-field="gate-note"]').hidden, true);

  // Readout day: the card says so, and so does the header pill.
  const due = benchmarkCard();
  context.__test.renderGatePanel(
    due,
    { bucket: "alpha_candidate", gate: { ...target.gate, days_to_readout: 0, readout_due: true } },
    data,
  );
  assert.equal(due.text("gate-readout"), "2026-10-02 — readout due");
  assert.equal(due.querySelector('[data-field="readout-due"]').hidden, false);

  // A target that isn't a configured study keeps the panel away.
  const other = benchmarkCard();
  context.__test.renderGatePanel(other, { bucket: "beta" }, {});
  assert.equal(other.querySelector('[data-field="gate-panel"]').hidden, true);
});

test("gate panel refuses to fill in a sample count it does not have", () => {
  const base = {
    spec_hash: "a1b2c3d4e5f6",
    required_samples: 60,
    readout_on: "2026-10-02",
    days_to_readout: 24,
    readout_due: false,
    spec_drift: false,
  };
  const pending = benchmarkCard();
  context.__test.renderGatePanel(pending, { gate: base }, {});
  assert.equal(pending.text("gate-samples"), "- / 60");
  assert.equal(pending.text("gate-health"), "-");
  assert.match(pending.text("gate-note"), /not reporting a sample count/);

  // Spec drift: the study running is not the study registered, so its
  // samples are withheld rather than counted toward the frozen gate.
  const drift = benchmarkCard();
  context.__test.renderGatePanel(drift, { gate: { ...base, spec_drift: true } }, {});
  assert.equal(drift.text("gate-samples"), "- / 60");
  assert.match(drift.text("gate-note"), /different gate spec/);
});

test("gate health reports the machinery, not the result", () => {
  const health = context.__test.gateHealthText;
  assert.equal(health({ decision_on_time: true, signal_hash_matched: true }, {}), "Sampling normally");
  assert.equal(health({ decision_on_time: false }, {}), "decision late");
  assert.equal(health({ signal_hash_matched: false }, {}), "signal hash mismatch");
  assert.equal(health(null, { book: { last_decision: { outcome: "rejected" } } }), "last decision rejected");
  assert.equal(health(null, { book: { daily_halted: true, last_decision: { outcome: "applied" } } }), "daily loss halt");
  assert.equal(health(null, { book: { equity_ready: false, last_decision: { outcome: "applied" } } }), "venue equity unavailable");
  // Nothing observed at all is "-", never a green "normal".
  assert.equal(health(null, {}), "-");
});

test("alpha candidate cards hide equity, PnL, win rate and CAGR", () => {
  const card = benchmarkCard();
  const blinded = ["trading-headline", "trading-kv", "trading-stats-header", "trading-stats", "trading-chart"];
  context.__test.blindAlphaCandidate(card, true);
  for (const field of blinded) {
    assert.equal(card.querySelector(`[data-field="${field}"]`).hidden, true, field);
  }
  context.__test.blindAlphaCandidate(card, false);
  for (const field of blinded) {
    assert.equal(card.querySelector(`[data-field="${field}"]`).hidden, false, field);
  }
});

test("blinding an alpha card also keeps the book's equity off it", () => {
  const book = {
    instance_id: "xsmom-695",
    gross_usd: 2000,
    net_usd: -50,
    equity_usd: 1009.93,
    signal_status: "applied:abc123",
    last_decision: { key: "k", outcome: "applied", attempts: 1 },
  };
  // Equity against a known starting reference is the running result the
  // blinding exists to hide; gross and net say whether the book is
  // balanced, which is operational and stays.
  const blinded = context.__test.bookViewModel(book, { blindResult: true });
  assert.equal(/equity/.test(blinded.exposure), false);
  assert.equal(/1,009|1009/.test(blinded.exposure), false);
  assert.match(blinded.exposure, /gross/);
  assert.match(blinded.exposure, /net/);
  // Every other target keeps it.
  assert.match(context.__test.bookViewModel(book).exposure, /equity/);
});

// The `hidden` attribute only sets `display: none` at the user-agent
// level, so an author `display` on the same element beats it. The DOM
// property assertions above cannot see that, so the stylesheet itself
// has to be checked: without this rule the α blinding is a no-op in a
// real browser.
test("hidden elements are actually hidden by the stylesheet", () => {
  const css = fs.readFileSync(`${__dirname}/../web/styles.css`, "utf8");
  assert.match(css, /\[hidden\]\s*\{[^}]*display:\s*none\s*!important/);
});

test("alpha bucket aggregates study count and the nearest readout, never performance", () => {
  const items = [
    { target: { bucket: "alpha_candidate", gate: { readout_on: "2026-10-02", days_to_readout: 24, readout_due: false } }, index: 0 },
    { target: { bucket: "alpha_candidate", gate: { readout_on: "2026-09-11", days_to_readout: 3, readout_due: false } }, index: 1 },
  ];
  const stats = context.__test.alphaAggregateStats(items);
  const stat = (label) => stats.find((s) => s.label === label);
  assert.equal(stat("Studies running").value, "2");
  assert.equal(stat("Nearest readout").value, "2026-09-11 (3d)");
  // No money or performance figure may appear in this bucket's header.
  for (const entry of stats) {
    assert.equal(/USDC|PnL|CAGR|Calmar/.test(entry.value), false);
  }
  const dueStats = context.__test.alphaAggregateStats([
    { target: { gate: { readout_on: "2026-09-11", days_to_readout: 0, readout_due: true } }, index: 0 },
  ]);
  assert.equal(dueStats.find((s) => s.label === "Nearest readout").value, "2026-09-11 — due");
  assert.equal(context.__test.alphaAggregateStats([{ target: {}, index: 0 }]).find((s) => s.label === "Nearest readout").value, "-");
});
