# α candidates: gate progress instead of PnL

`book-runtime-xsmom-695` (bot-strategy#695, readout 2026-10-02) and
`engine-b-live` (#866) are α candidates: hypotheses with a gate that was frozen
before anyone looked at the data. Showing their running PnL, equity curve, win
rate or CAGR is peeking, and peeking is how a pre-registered study stops being
one (taxonomy §4.3). The card therefore hides those and shows only what the
gate needs.

## Configuration

```yaml
  - name: XSMOM
    # ... existing target fields ...
    bucket: alpha_candidate
    gate:
      spec_hash: "a1b2c3d4e5f6"   # 12-64 lowercase hex, the frozen pre-registration
      required_samples: 60
      readout_on: "2026-10-02"    # UTC
      sample_source: "book runtime decision journal (bot-strategy#937)"
```

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

The generic trading view's equity headline, PnL rows, lifetime stats and equity
chart are hidden for these targets, along with the risk progress panel (its bars
state the live drawdown in bps against its threshold) and the book panel's
equity term. The halt pills stay in the header — halt state is safety, not
performance — but their tooltips withhold the magnitudes, and the book panel's
note row names a halt rather than quoting its reason. The bucket header counts
as running only the targets that are reporting *and* able to enter. The numbers stay in `status.json` and in
`/api/status` so the readout script can still read them; the frontend simply
does not render them.

## status.json contract

```json
"gate": {
  "spec_hash": "a1b2c3d4e5f6",
  "valid_samples": 12,
  "last_sample_at": 1757300000,
  "decision_on_time": true,
  "signal_hash_matched": true
}
```

There is deliberately no PnL, t-statistic or equity in this block.

If the reported `spec_hash` differs from the configured one, the sample count
is withheld and the card says so: the study running is not the study that was
registered, and counting its samples against the frozen gate would launder a
changed hypothesis into a pre-registered result. Re-freezing a spec is a change
to the pre-registration — update the issue first, then this config.
