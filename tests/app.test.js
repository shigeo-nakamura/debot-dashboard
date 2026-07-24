const assert = require("node:assert/strict");
const fs = require("node:fs");
const test = require("node:test");
const vm = require("node:vm");

const source = `${fs.readFileSync(`${__dirname}/../web/app.js`, "utf8")}
globalThis.__test = { renderRiskHistory };`;
const context = {
  document: { getElementById: () => null },
  fetch: () => new Promise(() => {}),
  setInterval: () => 0,
};
vm.runInNewContext(source, context);

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
