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
