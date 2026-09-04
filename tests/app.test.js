const assert = require("node:assert/strict");
const fs = require("node:fs");
const test = require("node:test");
const vm = require("node:vm");

const source = `${fs.readFileSync(`${__dirname}/../web/app.js`, "utf8")}
globalThis.__test = { renderRiskHistory, isAccumulatorStatus, isTargetUnhealthy, accumulatorViewModel, isHanBridgeStatus, hanBridgeViewModel, isHanBridgeHalted };`;
const context = {
  document: { getElementById: () => null },
  fetch: () => new Promise(() => {}),
  setInterval: () => 0,
};
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
