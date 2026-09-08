# Subsidy bots: cost per unit instead of PnL

Robinhood Freq / Robinhood B and Arcus SPY/QQQ are subsidy-capture bots
(`docs/buckets.md`). Their PnL is negative by design: they pay fees, slippage
and adverse selection to earn something the venue is deliberately handing out
(points, qualifying activity). Reading that PnL as a result invites tuning a
signal that has no edge to begin with — 0/362 pairs on Arcus, bot-strategy#935
— so the card is judged on the price paid per unit earned (bot-strategy#938,
taxonomy §4.2).

## Configuration

```yaml
  - name: Robinhood Freq
    # ... existing target fields ...
    bucket: subsidy
    subsidy:
      unit: points                  # what one unit is
      imputed_unit_value_usd: 0.05  # operator assumption, optional
      value_source_date: "2026-09-08"
      program_changed_on: "2026-09-01" # optional
      kpi_reviewed_on: "2026-09-05"    # optional
```

- `subsidy:` is only valid on a target in the subsidy bucket; anywhere else it
  is a startup config error.
- `imputed_unit_value_usd` requires `value_source_date`. An imputed payout with
  no date silently ages into a fabrication. Leaving the value out is fine: an
  unpriced subsidy is a legitimate state, and the KPI still prices the cost.
  bot-strategy#938 keeps the $0 / conservative / median sensitivity table.
- `program_changed_on` is the operator's record that the venue changed the
  program. While it is later than `kpi_reviewed_on` (or that field is unset),
  the card shows a `KPI STALE since <date>` pill: the numbers describe the old
  program until someone re-evaluates them. Staleness is the default after a
  change, because re-evaluating is an explicit act.

## Where the numbers come from

The denominator — units earned — comes from the bot's own daily subsidy ledger,
which bot-strategy#938 is producing. Until a bot emits it, the card renders
"-" for every row that needs a denominator, and says so in a note. Nothing is
inferred from the other half: a cost with no units earned is shown as a cost,
never as a cost per unit.

`status.json` contract (all fields optional except `unit`):

```json
"subsidy": {
  "unit": "points",
  "units_total": 20000,
  "units_7d": 1000,
  "cost_total_usd": 210,
  "cost_7d_usd": 14,
  "as_of_ts": 1757300000
}
```

`as_of_ts` is when the ledger itself was written. The card ages it separately
and says so past two days: the ledger is a daily artifact while the status
object refreshes every minute, so without its own timestamp a ledger that
stopped days ago reads as current under the card's fresh "Last update".

Costs are **positive when money was given up**, so cost per unit reads as a
price paid; a bot that came out ahead reports a negative cost. `unit` must
match the target's configured `subsidy.unit` (case-insensitively) or the whole
block is ignored — a points figure priced with an activity conversion rate is a
wrong number presented as a measurement.

Until a bot reports `cost_total_usd`, the cumulative cost falls back to the
bot's own net result, in the right direction:

- Arcus: `cumulative_cost_usd`, which values the initial basket at current
  prices and is therefore already price-neutral. The exporter emits it
  alongside the existing `cumulative_loss_usd`, which is floored at zero
  because the risk limits compare against it — dividing that floored figure
  would report a cost of exactly zero for a run that came out ahead. An
  exporter predating the signed field falls back to the floored one.
- pairtrade-shaped bots (Robinhood): `-trade_stats.pnl`, the lifetime result
  net of the fees and slippage that make up the cost.

There is no fallback for the rolling 7-day window: it needs the daily ledger.

## What the card shows

The KPI panel sits above the bot's normal view, whatever shape its payload has.
Cost per unit (7d and since start) leads, followed by units earned, cumulative
cost, and the imputed value of what was earned with the date its conversion was
sourced. PnL stays on the card below, relabelled "Cost today (PnL)".

The subsidy bucket header aggregates cost across the bucket, but keeps units
separate per unit name: points and USD activity are different things, and only
the targets reporting a unit contribute to that unit's cost, so one bot's
spending cannot inflate another's price per point.
