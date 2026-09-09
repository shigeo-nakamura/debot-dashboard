# α candidates: gate progress instead of PnL

`book-runtime-xsmom-695` (bot-strategy#695, readout 2026-10-02) and
`engine-b-live` (#866) are α candidates: hypotheses with a gate that was frozen
before anyone looked at the data. Showing their running PnL, equity curve, win
rate or CAGR is peeking, and peeking is how a pre-registered study stops being
one (taxonomy §4.3). The card therefore hides those and shows only what the
gate needs.

## Configuration

```yaml
  - name: XSMOM shadow track
    instance_id: local
    service: xsmom-695-shadow
    region: eu-central-1
    s3_bucket: debot-dashboard
    s3_key: debot/status/xsmom-695-shadow/status.json
    # The watcher publishes hourly, so the default 180 s freshness window
    # would read every card as stale.
    stale_after_secs: 7200
    bucket: alpha_candidate
    gate:
      spec_hash: "b33440bde55908f2"   # 12-64 lowercase hex, the frozen pre-registration
      required_samples: 91
      readout_on: "2026-10-02"        # UTC
      sample_source: "daily marks in the shadow-paper ledger (bot-strategy#695)"
```

**Which producer holds the gate matters.** XSMOM has two: the shadow-paper
watcher whose ledger the 2026-10-02 readout is computed from, and the Tokyo
`book-runtime-xsmom-695` that started following it on 2026-09-11. Only the
first one carries `gate:` — counting the follower's decisions against the
frozen gate would report progress on a study the readout never reads
(bot-strategy#964).

`gate:` is only valid on a target in the alpha_candidate bucket; anywhere else
it is a startup config error. `spec_hash` lives in the dashboard config, not
only in the bot, so the dashboard can state which spec it is counting against
independently of the process being observed.

## What is shown, and what is withheld

- **Valid samples** `n / required`. The count comes from the bot's own `gate`
  block. Until a bot emits one the row reads `- / 60`: the dashboard never
  estimates a sample count.
- **Next readout** — the frozen date, counting down, flipping to
  "readout due" on the day along with a `READOUT DUE` header pill. On that day
  the operator runs the pre-registered script and posts the result **on the
  issue**, not on the dashboard.
- **Frozen spec** — the first 12 characters of `spec_hash`.
- **Sampling health** — whether the machinery is producing samples: a late
  decision, a signal-hash mismatch, a halt, or a last decision that was
  rejected or skipped — for a book runtime derived from the decision it actually
  took, for Engine B from its `han_bridge` session halt. It covers every state
  that blocks new entries (kill switch, daily or session DD halt, circuit
  breaker, book or Engine B halt, venue equity unavailable): a study that cannot
  enter is not accumulating samples. Each is named by a fixed label, never by
  the producer's halt reason, which can embed the number that caused it
  ("session loss $160.00 > limit $150.00") — the running result the blinding
  exists to withhold. Nothing observed at all reads "-", never a green "normal".

- **Sampling health** also reports "Not sampling (stale)" when the target's own
  status has stopped arriving: a frozen payload's last `decision_on_time: true`
  is not evidence that the study is still running.

- **Sampling health** reports "sample overdue" when the producer's own
  `next_sample_due_at` has passed and the readout has not. A fresh status
  object proves the producer is alive, not that the study is accumulating:
  the XSMOM watcher kept publishing through the 2026-08-26..08-31 gap that
  cost the track six marks. The deadline is the producer's because only it
  knows its cadence — XSMOM marks daily, Engine B once per session, the
  ex-dividend book (#948) far more sparsely — and a dashboard-side rule
  general enough to cover all three would flag none of them. A producer that
  declares no deadline gets no verdict: the dashboard never infers one from
  `last_sample_at`.

## Solvency is exempt (bot-strategy#919)

One class of number stays visible on a blinded card: what the venue says
the account holds. Engine B's Han Bridge panel shows `Venue equity`,
`Available` and, while a position is open, `Unrealized (mid est.)` from
its `han_bridge` block, regardless of blinding.

The line this draws is the same one the halt pills already sit on. A
halt is shown because it answers "can this still trade", which is safety;
its *reason* is withheld because it can embed the running loss, which is
performance. Account solvency is the safety half of the same question --
an α candidate that quietly ran out of margin is a study that stopped
sampling, and "Sampling health" cannot say so if nothing may report the
balance. For Engine B specifically the peeking risk is close to nil in
practice: a $100 lot moves a five-figure account by cents, so the equity
figure is dominated by the deposit, not by the result.

Three rules keep this from becoming a back door:

- **Only the venue's own figures.** No equity *curve*, no drawdown, no
  win rate, no PnL series -- a point-in-time balance, not a track record.
- **Null is not zero.** The bot publishes `null` for "not read yet" and
  the card renders "-". A row reading "$0.00" therefore always means a
  real, empty account. Collapsing the two would either manufacture a
  solvency alarm or hide one (`Number(null) === 0` in JS made exactly
  this mistake once; a test pins it).
- **Age travels with the value.** A failed refresh keeps the last reading
  and lets its age grow rather than restamping it; past
  `HAN_BRIDGE_EQUITY_STALE_SECS` (300 s) the card annotates it and tones
  it as a warning. An old balance is "unknown", not "unchanged".

Realized PnL, the equity chart and the lifetime stats stay hidden. So
does the halt reason.

The generic trading view's equity headline, PnL rows, lifetime stats and equity
chart are hidden for these targets, along with the risk progress panel (its bars
state the live drawdown in bps against its threshold) and the book panel's
equity term. The halt pills stay in the header — halt state is safety, not
performance — but their tooltips withhold the reason and the magnitudes, and
the book panel's note row and Engine B's halt row name a halt rather than
quoting its reason. A producer's reason can embed the loss that caused it, so
no path on a blinded card copies one verbatim. The bucket header counts
as running only the targets that are reporting, able to enter, and running the
registered spec — a drifted spec produces no countable samples. The numbers stay in `status.json` and in
`/api/status` so the readout script can still read them; the frontend simply
does not render them.

## status.json contract

```json
"gate": {
  "spec_hash": "a1b2c3d4e5f6",
  "valid_samples": 12,
  "last_sample_at": 1757300000,
  "decision_on_time": true,
  "signal_hash_matched": true,
  "next_sample_due_at": 1757386400,
  "sample_cadence_secs": 86400
}
```

`next_sample_due_at` is a unix second and already includes whatever grace
the producer allows itself (the XSMOM watcher retries hourly, so its
deadline is 01:30 UTC for a mark nominally taken at 00:20).
`sample_cadence_secs` is shown to the operator and never used to derive a
deadline. Both are optional; both are withheld along with the sample count
when the spec hash drifts.

There is deliberately no PnL, t-statistic or equity in this block.

If the reported `spec_hash` differs from the configured one, the sample count
is withheld and the card says so: the study running is not the study that was
registered, and counting its samples against the frozen gate would launder a
changed hypothesis into a pre-registered result. Re-freezing a spec is a change
to the pre-registration — update the issue first, then this config.
