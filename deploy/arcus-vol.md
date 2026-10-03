# Arcus presence runtime card (bot-strategy#1093)

Read-only view of `arcus_vol_runtime`, the Arcus Perps presence / volume bot
that rests one post-only quote per side a few bp behind the touch of a single
market (SPY-USD at the time of writing) and re-pegs only when the touch leaves
a band. The card answers "is it quoting, and what did the fills cost", not
"what did it earn": a presence bot's PnL is the price paid for being on the
book.

The dashboard reads the runtime's own `status.json` on the same host. It
makes no venue calls and reads no credential or operator config file.

## What the card shows

| Row | Source |
|---|---|
| State | `QUOTING` (plan `quote`, both sides resting), `QUOTING (placing)` (one side missing right after a re-peg), `PULLED (<reason>)` (plan `pull:<reason>`, e.g. `stale_book`, `shock`, `dms_unarmed`), `FLATTENING (<reason>)`, `HALTED (<reason>)` (`daily_stop` clears at 00:00 UTC; `kill_switch` and `sticky: …` need the operator), `STALE` (status older than `stale_after_secs`), `UNAVAILABLE` (file unreadable / not this runtime's — the measurement rows then show `—`, never zero, and the target adds nothing to the subsidy bucket's cost) |
| Market | market · live/paper · configured offset and re-peg band |
| Bid / Ask | resting price (distance from the touch, bp) × size in USD; `—` for a side not resting |
| Book | venue best bid / ask the runtime last saw |
| Inventory | `flat` or quantity (USD) |
| PnL today / cumulative | net of fees, plus "left of stop" when the stop limits are configured (limit + net, floored at 0) |
| Volume today / fills | today's traded notional, lifetime fill count, lifetime maker share |
| Presence 24h | **dashboard-side sampling**: share of the last 24 h (at the dashboard's poll interval) in which the runtime planned to quote with both sides resting, and the number of re-pegs seen (a side's order changing between two consecutive samples). Every poll is a sample: one that finds the file unreadable, invalid or stale counts as not quoting, so an outage lowers the figure instead of vanishing from it. In memory only — it starts over when the dashboard restarts, and the row says how long the window actually covers |
| API key | days until the configured expiry; amber at ≤ 30 d (rotate soon), red at ≤ 7 d or expired (orders about to fail). The runtime cannot renew the key |
| Halt | the runtime's halt label, when halted |

Header fields follow the generic rules: `Last update` is the status file's
own timestamp; a halt counts in the fleet "halts" figure; `kill_switch` halts
light the kill-switch pill; a halted, stale or unavailable runtime counts as
unhealthy / degraded.

## Configuration

Add the target to `/opt/debot-dashboard/config.yaml` (owner-edited, not
managed by the deploy workflow), keeping every existing target:

```yaml
  - name: Arcus presence (SPY)
    instance_id: i-xxxxxxxxxxxxxxxxx
    service: debot-arcus-vol
    region: ap-northeast-1
    bucket: subsidy
    arcus_vol:
      status_path: /var/lib/debot-arcus-vol/state/status.json
      daily_stop_usd: 5          # = DAILY_STOP_USD in the runtime's config.env
      cum_stop_usd: 21           # = CUM_STOP_USD
      api_key_valid_until: "2027-03-30T06:32:00Z"
      stale_after_secs: 20       # optional, default 20
```

- `status_path` must be absolute; do not set `s3_bucket` / `s3_key` or
  `bull_holder` on this target (one source per target). The dashboard user needs read access to the file and its directory
  (the runtime writes it 0644 in a 0755 directory).
- `daily_stop_usd`, `cum_stop_usd` and `api_key_valid_until` are display
  values copied from the operator's files. Change them here when they change
  there; leaving them out hides the "left of stop" suffix and the API-key
  row rather than showing a wrong figure.
- `bucket: subsidy` is declared explicitly: the return-source taxonomy
  (`docs/buckets.md`, mirroring bot-strategy) does not list the service yet,
  so without it the card renders as "Unclassified". In the subsidy bucket the
  runtime's lifetime net result (negated) feeds the bucket's "Cost paid"
  figure, the same way `trade_stats.pnl` does for pairtrade targets. Adding the service to the
  taxonomy is a bot-strategy ledger change first.

## Rollout

The deploy workflow restarts **debot-dashboard only**. The target type is
additive: a `config.yaml` without an `arcus_vol` target loads exactly as
before, so merging this change does not require the host edit first. To show
the card: (1) add the target above to `config.yaml` (back it up first),
(2) `sudo systemctl restart debot-dashboard`, (3) confirm `/api/status` has
`status.arcus_vol` for the target with `state`, `quotes` and `presence`, and
that the existing cards still render. Removing the target and restarting
rolls back.

Nothing here starts, stops or configures the runtime itself; that is the
operator's `systemctl` and the files under `/etc/debot-arcus-vol/`.
