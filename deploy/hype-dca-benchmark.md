# HYPE accumulator: DCA benchmark and staking carry

The accumulator is a β + carry bot (`docs/buckets.md`): its return is the
asset's own price plus, once staking lands, the staking yield. Neither is a
trading result. The one execution question worth asking is whether it
accumulated more cheaply than the naive alternative — spending the same budget
every day and not thinking about it (bot-strategy#956, taxonomy §4.1).

## Configuration

```yaml
  - name: HYPE Accumulator
    # ... existing target fields ...
    bucket: beta
    accumulator:
      dca:
        window_start: "2026-09-11" # first purchase date, UTC
        symbol: HYPE               # the asset being accumulated (must be HYPE)
        market: spot               # spot (default) or perp
```

`symbol` has to be `HYPE`: the cost basis is read from the accumulator's HYPE
balance and its HYPE spend, so benchmarking another symbol would compare HYPE
purchases against an unrelated market and report the difference as an execution
edge. The field exists to make the market resolution below explicit, not to
change the asset — generalising it needs the status and ledger fields to name
their asset first.

`market` is not cosmetic. `candleSnapshot` reads a bare symbol as the
**perpetual**; a spot market is addressed by its own pair id (HYPE spot is
`@107`), which the dashboard looks up from the public spot metadata. Pricing
spot accumulation against perp closes would measure the execution edge against
the basis rather than against the naive schedule, so `spot` is the default and
an asset with no spot market is reported as such rather than silently falling
back.

The bot does not report when it started buying, and inferring that date from a
status snapshot would silently move the benchmark, so the operator verifies it
here the same way as the bull-holder's anchor.

## What the card shows

- **Cost basis** — authoritative USDC debited on fills (`operations.spent_usdc`,
  the durable purchase journal) divided by the HYPE actually purchased. Staking
  rewards are subtracted from the balance first: they are yield the bot earned,
  not HYPE it bought cheaply, and counting them would flatter the execution
  number.
- **Naive DCA** — the average unit cost of spending the same amount on every
  daily close in the window. That is the **harmonic** mean of the closes, not
  their arithmetic mean: a fixed budget buys more units when the price is low,
  which is the entire reason DCA is the benchmark. Averaging the prices instead
  would hand the bot an edge it never earned (on closes of 60 and 120 the
  harmonic mean is 80 while the arithmetic mean is 90).
- **Execution edge** — `(naive DCA − cost basis) / naive DCA` in bps. Positive
  means the bot accumulated below the naive schedule.
- **Price β (unrealized)** — the former "Unrealized PnL" row, relabelled: it is
  the mark-to-market of the purchased HYPE, which is price exposure, not skill.
- **Staking rewards (carry)** — accrued yield in HYPE and its value at the
  current mark, always its own row and never folded into price β. Renders "-"
  until the bot reports it (bot-strategy#847, live after 2026-09-18), which is
  different from a bot that stakes and earned nothing.

Daily closes come from the public `candleSnapshot` info endpoint — no account
identity, no signing material — and are cached for 30 minutes, since they change
once a day while the dashboard polls every 20 seconds. A failed read is cached
for one minute instead: a timeout or a rate limit is transient, and holding it
for the full TTL would blank the benchmark for half an hour over one bad
request. The current UTC day's candle is still open, so its close is the latest
intraday price rather than a daily close; it is excluded from the window.

That leaves one accounting asymmetry, disclosed on the card rather than papered
over: a fill the bot makes today is already in its cost basis (`spent_usdc` is
cumulative, with no daily breakdown to remove it from) while today is not yet in
the schedule it is compared against. The two alternatives are mutually
exclusive — counting today's open candle would price the naive schedule at a
tick instead of a close — so the residual is left and bounded: at most one day's
weight in an N-day window, and it clears at the next UTC rollover. The benchmark is derived
by the dashboard and any `accumulator_dca` in the producer payload is discarded
before it is computed.

## status.json contract for staking

```json
"accumulator": {
  "staked_hype": 120.0,
  "staking_rewards_hype": 2.5
}
```

Both are optional and both are pointers on the wire: absent means "not
reported", which the card shows as "-".
