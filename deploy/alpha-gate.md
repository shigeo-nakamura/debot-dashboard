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
- **Absent, null and zero are three different things.** A producer that
  predates #919 gets no row. A current producer that has not read its
  account yet publishes `null`, and the card shows the row as "-" toned
  as a warning -- unknown solvency is a finding, not a reason to hide the
  row. Only a number renders as money, so "$0.00" always means a real,
  empty account. Collapsing any two of these would either manufacture a
  solvency alarm or hide one (`Number(null) === 0` in JS made exactly
  that mistake once; a test pins it).
- **Which producer is which is decided by `venue_solvency_reported`, not
  by whether the key is there.** This document is decoded into Go and
  re-encoded on the `/api/status` path, and there a nil pointer and an
  absent key are the same thing: an explicit `null` from the producer
  reached the browser as a missing field, hiding the row exactly when it
  should have read "-". The solvency fields therefore carry no
  `omitempty` — nil must re-encode as `null` — and presence is stated
  outright by the producer. A Go test walks the whole decode/re-encode
  path, because the JavaScript view-model tests never cross the server
  and cannot see this class of bug.
- **Staleness is judged on the producer's flag first.** `venue_equity_stale`
  is exact: it says the last read failed. The age beside it is an
  approximation -- the connector may have served a cached sample -- so it
  is a secondary signal, and either one tones the row as a warning.
- **The age counts the time the status document sat still.** A payload
  that stops arriving freezes `venue_equity_age_secs` at whatever it was
  when written, so a reading emitted at 290 s would otherwise read as
  current forever. The card adds the elapsed time since the status
  timestamp before judging against `HAN_BRIDGE_EQUITY_STALE_SECS`
  (300 s) and before displaying it. An old balance is "unknown", not
  "unchanged".

The session schedule is exempt on the same grounds. The card shows what
this session is still waiting for -- `Entry 06:30 UTC · in 3h12m`,
`Exit 13:30 UTC · in 2h33m` -- from the frozen trading calendar. It is
the same three timestamps every session day, so it says nothing about
how the study is going, and it answers the operational question this
card otherwise leaves to tribal knowledge: when a restart is safe.

Its real job is the third state. bot-strategy#917 closes an
unconfirmable exit by leaving the position open on purpose -- a
documented open position beats a close nobody can verify -- and that
outcome had no signal anywhere but an e-mail and the journal. Past the
scheduled exit the row reads `due 13:30 UTC · 5m late` and, once the
engine has stopped retrying, `abandoned, still open 13:30 UTC · 1h
late`, both toned as warnings. A card that went on reading "Entered,
holding" indefinitely was the alternative.

Times are stated in UTC and labelled as such, because the calendar, the
runbook and the journal all are; a time silently rendered in the
viewer's zone would be read against a runbook that means something else.

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
