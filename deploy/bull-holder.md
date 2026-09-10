# Bull-holder monitoring

This is a read-only view of the standalone BTC/ETH bull-holder. It reads the
existing local status file and operator sentinel directory on the dashboard
host. Account balances come from public Hyperliquid and Lighter APIs, including
while the bot is Off, halted, or in DRY_RUN. No bot changes or bot restart are
needed. There are no ARM/ADD/order endpoints or buttons in this dashboard.

## Configuration and rollout

After the dashboard change is reviewed and the owner approves merging its PR,
deploy the dashboard binary and web assets through the existing deployment
workflow. That workflow restarts **debot-dashboard only**, not any trading bot.
Add the following entry inside `targets` in `/opt/debot-dashboard/config.yaml`
with the dedicated accounts' public identifiers, preserving all existing
targets and authentication. Do not commit real account identifiers.

```yaml
  - name: Bull-holder (Frankfurt)
    instance_id: i-0c08fba996bc21879
    service: debot-bull-holder
    region: eu-central-1
    bull_holder:
      status_path: /opt/debot-bull-holder/bull_holder/status.json
      hyperliquid_address: "<dedicated funds-owning account address>"
      lighter_account_index: "<dedicated numeric account index>"
```

Apply the config with a dashboard-only restart after backing up the config.
The dashboard service user needs read access to status.json and directory
listing access to its parent; ADD contents are optional (an unreadable pending
ADD is shown as pending with its amount unavailable). No credential env file,
KMS permissions, signing wallet, or write access to bot files is needed.
Public identifiers can be omitted while wiring the card; missing accounts show
unavailable values, not zero. Do not set S3 fields on this local target.

After deployment confirm `/api/status` has `status.bull_holder` for the new
target, `mode`/`dry_run` match the producer, and both account observation times
advance. Confirm that existing targets and their trading metrics still render.
Removing just this target and restarting the dashboard rolls back the config.

## Display semantics

### Investment settings (display-only)

The existing bot emits `config_fp`, but not its full effective budget. The
dashboard accepts an optional operator-verified startup snapshot under the
target's `bull_holder.investment`:

```yaml
investment:
  config_fp: "000000000000" # replace with the verified running fingerprint
  equity_usd: 1000
  spot_fraction: 0.90
  perp_fraction: 0.45
```

Verify these three values against the running bot's `[CONFIG] bot=bull_holder`
startup line, and verify its `fp` equals the producer status `config_fp` before
populating this snapshot. Do not copy settings from an env file that may have
changed since startup. Never print/source credential files to populate it.
The fingerprint is a freshness binding, not an independent proof that manually
entered amounts are correct. A missing/invalid producer or fingerprint mismatch
hides all budget amounts. Producer staleness remains indicated by the card's
normal freshness status; a matching snapshot describes its last reported config.
Update the verified snapshot when a later authorized bot configuration rollout
changes the fingerprint. This feature does not need a bot restart or any bot
code/configuration change; only dashboard deployment and its config reload.

### Buy & hold benchmark (bot-strategy#955)

A β bot is judged against buying the same exposure as spot and holding it, not
against zero (`docs/return-source-taxonomy.md` §4.1 in bot-strategy). The card
renders that comparison from an optional anchor inside the same verified
snapshot:

```yaml
investment:
  config_fp: "000000000000"
  equity_usd: 1000
  spot_fraction: 0.90
  perp_fraction: 0.45
  anchor:
    ts: 1757203200   # when the capital was deployed (epoch seconds)
    funded_usd: 1301 # total account equity at `ts` (optional)
    assets:
      - symbol: BTC
        spot_symbol: UBTC # Hyperliquid spot token used for the current price
        price_usd: 111000 # BTC price at `ts`
        units: 0.00432    # what the bot actually holds at `ts` (optional)
      - symbol: ETH
        spot_symbol: UETH
        price_usd: 4300
        units: 0.0989
```

The anchor must list every leg the bot trades, checked against the producer's
`configured_symbols` (its running config's universe, pairtrade#295) and falling
back to the open `legs` for a producer that does not report it yet. The
configured universe is what matters: `legs` is empty until a tranche fills, and
before ARM is exactly when a misconfigured anchor sits unnoticed. The whole spot allocation is
split across the legs it does list, so an anchor missing one spends that leg's
budget on the others — not a partial benchmark but a different portfolio,
beating or losing to the bot by the spread between the legs. The dashboard
suppresses the benchmark on a mismatch.

Verify `ts` and each `price_usd` the same way as the rest of the snapshot, and
against the same `config_fp`: a fingerprint mismatch hides the benchmark along
with the budget amounts. `spot_symbol` defaults to `symbol` and is only needed
where the venue's spot token differs (BTC → `UBTC`, ETH → `UETH`).

The benchmark spends `equity_usd × spot_fraction` at the anchor, split equally
by USD across the legs (matching how the bot deploys the same
`tranche_spot_usd` into each leg), and leaves the remainder in cash. That
derivation is only the bot's book when the bot bought everything at
`price_usd`, so each asset also takes an optional **`units`** — the quantity the
benchmark holds — which replaces it. With `units` set the benchmark's cost at
the anchor is what those quantities were worth then, and the rest of
`funded_usd` is cash. It is all-or-nothing across the legs: sizing some from the
bot's real book and the rest from the declared allocation is two benchmarks
added together, and is rejected. See the cutover procedure below for why a
laddered entry needs it. The bot's
leverage and hedge are what the comparison is about, so the benchmark takes none
of them. It is
priced from the same public `spotMetaAndAssetCtxs` marks the card already reads,
which are now fetched whether or not an account is configured — a DRY_RUN bot
owns nothing, and its benchmark still has to be priced. A leg the marks cannot
price suppresses the whole benchmark rather than dropping that leg, since a
partial benchmark reads as the bot beating buy & hold.

The sample quantities above are consistent with the rest of the sample anchor:
`0.00432 × 111000 + 0.0989 × 4300` is $904.79 of spot, inside the `funded_usd:
1301` the account holds, with the remaining $396.21 as cash. A set of `units`
worth more than `funded_usd` at the anchor prices is rejected as levered, so an
example that does not add up is a runbook that suppresses the benchmark.

`funded_usd` is what both sides start from, and it is **not** the same number as
`equity_usd`. The card's bot side is live account equity — Hyperliquid spot plus
Lighter — which includes the perp leg's margin buffer, while `equity_usd` is the
capital the strategy declares. On 2026-09-08 those were $1,301 and $1,000, and
sizing the benchmark from the declared figure reported the $301 difference as a
31% outperformance (bot-strategy#963). Set it to the total account equity at
`ts`, verified the same way as the prices; omit it only when the accounts hold
exactly the declared capital. A `funded_usd` below the spot allocation is
rejected, since covering the gap would make the benchmark levered.

**While the bot is in DRY_RUN the card compares nothing.** It places no orders,
so those balances are the untouched deposit: the bot's side is a constant and an
"excess" would only track the market falling. The four rows read "-" with that
reason, and no benchmark samples are cached, so the drawdown comparison begins
at the moment the bot goes live rather than dragging in a flat pre-live stretch.
The anchor is accepted before then and simply stays dormant, but the values to
put in it do not exist yet — see below.

Four rows are shown. **Excess vs b&h** is current combined equity minus the
benchmark, in USDC and percent. **Max DD** and **Calmar** are computed for both
series over the same window, and that window is only as long as this page has
been open: the bull-holder producer writes no equity history file, so a freshly
loaded dashboard has no past to compare and both rows read "-" until enough
samples accumulate (Calmar additionally needs ≥ 7 days, like the card's CAGR).
**Funding + fees paid** comes from the producer's `cum_funding_usdc` /
`cum_fees_usdc`; a producer that does not emit them yet renders "-", never
`$0.00` — an absent carry cost must not read as no carry cost.

The benchmark is derived by the dashboard and any `benchmark` field in the
producer payload is discarded before it is computed, in the same way as the
investment snapshot and the combined equity total.

Displayed capital is `equity_usd`; spot allocation is capital × spot fraction;
perp notional target is capital × perp fraction. They are NOT account balances,
required margin, remaining tranche amounts, or an enforced investment cap.
ADD can extend the cycle beyond the initial allocation. These settings only
control dashboard labels; changing them does not change the bot's investment.

#### Anchoring at the live cutover (bot-strategy#963)

The anchor is not set yet, and it must not be set from the DRY_RUN run: while
`dry_run: true` the accounts hold the untouched deposit, so any `funded_usd` and
prices captured then describe a portfolio the bot never bought.

**Wait for the entry ladder to finish, and capture the quantities it actually
bought.** The bot builds the book in `BULL_HOLDER_ENTRY_TRANCHES` equal daily
tranches (read the count off the `tranches=` field of the running bot's
`[CONFIG]` line; it was 5 as of 2026-09-10), so it ends up holding the sum of
each tranche's USD at that tranche's own fill price. Two things follow, and both
have to be handled or the card measures the ladder instead of the strategy:

- Anchoring at the *first* tranche gives buy & hold the whole ladder's worth of
  full exposure against a bot that is still averaging in. In a rising ramp the
  benchmark wins by construction and in a falling one the bot does.
- Anchoring at the *last* tranche fixes the timing but not the size. Without
  `units` the benchmark still derives its quantities from the declared spot
  allocation divided by the anchor price, which is what the bot would hold had
  it bought everything at that one price. Matching the totals with `funded_usd`
  only books the ladder's accumulated P&L as cash; the two sides still hold
  different amounts of the same asset, and every later move in it shows up as
  "excess" (Codex, PR #51).

Setting `units` to the bot's real per-leg spot quantity removes both. The
benchmark then holds exactly the spot book the bot holds, minus the perp leg and
its margin — which is the comparison the card is for. It also means the ramp
window itself is not measured against buy & hold; it is part of the capital the
comparison starts from. That is deliberate: there is no single-price benchmark
that is honest about a laddered entry.

The procedure, once the owner has flipped the unit out of DRY_RUN and the ladder
has completed:

1. Confirm the ladder is done from the producer status: `tranches_remaining` is
   `0` and every configured symbol has a leg.
2. Take **one** `/api/status` response and read every number out of that raw
   JSON — not off the rendered card, which rounds token quantities to four
   decimal places (`AMOUNT_DIGITS`). On the sample anchor above, that rounding
   turns `0.00432` BTC into `0.0043`, about $2.22 of exposure the benchmark
   would then be missing, and every later BTC move would show up as excess.
   From `bull_holder`:
   - `funded_usd` = `hyperliquid.equity_usdc` + `lighter.equity_usdc`. It
     includes the perp leg's margin buffer on purpose — it is what the card's
     bot side reads.
   - `units` and `price_usd` per leg = `hyperliquid.holdings[].size` and
     `.price_usdc` for that leg's spot token (`UBTC`, `UETH`), at full
     precision. The benchmark holds spot only, so the Lighter perp positions
     are deliberately not part of it.
3. Set `ts` to that observation's time, which is the later of
   `hyperliquid.observed_at` and `lighter.observed_at` in the same response —
   **not** the producer's own `ts`. Those two clocks are decoupled
   (`fetchBullHolder` refreshes the accounts on the server's poll cycle while
   the status file keeps its own heartbeat), and the card's history points are
   stamped with the account observation, so an anchor labelled with the
   producer's `ts` is labelled with a moment the equity was never read at.
4. Verify `config_fp` still equals the producer's, then apply the config with a
   dashboard-only restart.

A correctly captured anchor shows an excess of roughly zero on the first
refresh, because both sides are then the same book priced by the same marks. A
visibly non-zero first reading means the three numbers came from different
moments. Note that this check cannot catch a mislabelled `ts` on its own — the
benchmark does not re-derive prices from `ts` — so step 3 has to be right by
construction, not by inspection.

**Any later deposit or withdrawal invalidates `funded_usd`** — the card would
read the transfer as performance, in exactly the direction of the original bug.
There is no way to net a transfer out of a single-anchor benchmark, so re-anchor
at the transfer using the same four steps. Re-anchoring restarts the comparison:
the old excess and the cached drawdown window are discarded, and the card begins
again from ~0. Record when and why in the issue, so a discontinuity in the
series is not later read as a jump in performance.

### Actual unrealized PnL

Each actual account and the combined book now show unrealized PnL, separately
from DRY_RUN strategy holdings. Lighter sums nonzero positions' venue-reported
`unrealized_pnl`. Hyperliquid estimates spot PnL as marked token value minus
`entryNtl` from `spotClearinghouseState`; this is the venue's entry-notional
estimate, not a transfer/fee-aware accounting ledger. See the
[official spot response schema](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/info-endpoint/spot).
Missing, invalid, or zero basis for a nonzero holding makes its PnL unknown.
Missing price/PnL for any holding or a failed account snapshot suppresses the
account and combined PnL, not the available equity. A successfully observed
account with no non-USDC holdings / open positions has zero unrealized PnL.
No producer simulation costs/positions are used for actual PnL. PnL is already
reflected in equity and is never added to equity a second time.

### Existing state and balance semantics

- Off = waiting for ARM; On = holding/adding; Exited = manual ARM needed again.
- ARM accepted/exited timestamps and completed/remaining tranches are bot state.
  Pending ARM/ADD/DISARM/RISK_ACK files are sampled requests, not proof an action
  executed. ADD's accepted work appears in remaining tranches; the producer has
  no separate last-ADD timestamp/history. A short-lived file can disappear
  between polls. KILL_SWITCH is displayed independently.
- Daily drawdown is `100 * (1 - last_close / peak_close)`, floored at zero.
  This uses the bot's last evaluated daily close, not a live price tick.
  The close date is always displayed; a missing close/peak shows unavailable.
  BTC or ETH breaching its price exit closes both. It is not a portfolio loss
  limit. Reported perp stop price/size is bot state, not a fresh open-order
  verification. DRY_RUN stop/position values are explicitly simulated.
- Actual Hyperliquid spot assets use `spotClearinghouseState` totals (hold is
  already part of total), valued against USDC using metadata token IDs and
  `spotMetaAndAssetCtxs` marks. USDC available = total - hold. Missing prices for
  held tokens make total equity unavailable rather than silently dropping them.
  This is **spot** account value, not HL perp/vault equity.
- Lighter collateral/available/equity use `collateral`, `available_balance`, and
  `total_asset_value` respectively. Nonzero perp positions show signed quantity,
  absolute notional, mark inferred from notional/size, unrealized PnL and
  liquidation price when available. Notional is exposure, not token ownership.
- Combined monitored equity = HL spot account value + Lighter account equity.
  Perp notional and unrealized PnL are not added again. Snapshots are observed
  independently, so this is not an atomic cross-venue valuation. Values use
  USDC as the numeraire; they do not model USDC/USD depegging.
- Actual account assets remain separate from simulated bot holdings in DRY_RUN.
  Holder values are excluded from the existing fleet trading PnL, return,
  drawdown, position aggregates and generic trading Prometheus metrics.
  Fleet target/health counts still include the card. Missing account data or
  a bot halt makes it degraded. Producer `ts` controls liveness independently
  of account queries (180 seconds), so fresh balances cannot hide a stale bot.

Both venues are queried concurrently with an overall 15-second deadline per
poll (default poll interval: 20 seconds). Accounts need no signing keys. Errors
are sanitized so account URLs and upstream response bodies do not reach users.

## Validation

```sh
go test -race ./...
go vet ./...
go build ./...
node --check web/app.js
node --test tests/app.test.js
```

The fixture represents the existing bull_holder status producer. Mocked API
tests cover actual/simulated separation, no notional double-count, account
identity checks, unpriced assets, invalid balances, stale producer status,
pending ADD interpretation and public payload field filtering.

References:

- https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/info-endpoint/spot
- https://apidocs.lighter.xyz/reference/account
- bot-strategy #893 / #895 / #909 / #910 (strategy and live-readiness work)
