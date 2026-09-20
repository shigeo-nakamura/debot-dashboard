# Robinhood points hedge card (bot-strategy#1046)

The Robinhood Chain Lighter pairtrade arms (`Robinhood Freq` / `Robinhood B`,
service `debot-pair-robinhood-lighter`) were stopped and disabled on
2026-09-19: they bled ~$35/day for ~8.5 points a week. Their two cards now
show a stale status object forever. The bot that replaces them is
`debot-xvenue-hedge-holder` (pairtrade `src/bin/xvenue_hedge_holder.rs`): a BTC
long on Lighter on Robinhood Chain hedged by an equal short on Lighter Core,
operator-armed, held for the weekly points drop.

## What the card shows

The producer's `status.json` keeps the generic top level the card already
renders (equity of both venues as `pnl_total`, `pnl_today` since 00:00 UTC, one
position per leg, `kill_switch_active`) and adds:

- the `subsidy` block (`deploy/subsidy-kpi.md`), so the KPI panel reads
  **cost per point since ARM**: `units_total` = the long account's live points
  since ARM (from the points collector's history, bot-strategy#938),
  `cost_total_usd` = −(equity change since ARM). The panel's generic labels
  say "since start" / "Cumulative": on this card that means **the current
  book**, from its ARM. A DISARM followed by a new ARM resets both figures
  (the bot re-bases points and equity at every ARM), so the cost of an
  earlier book is not in the number — the per-book readout on
  bot-strategy#1046 is where cycles are compared;
- a `hedge_holder` block rendered as an extra section: mode
  (Off / Building / Holding / Unwinding / Halted), the two leg sizes against
  the target, whether the legs are equal (warn past the bot's tolerance),
  each venue's liquidation headroom (margin is not shared across venues, so
  the lower side liquidates first), the RH−Core basis, the equity change
  since ARM, the halt reason when halted, and — while a venue is
  unreachable or the two marks diverge — a `Feed` row with the producer's
  `feed_problem` and the age of the snapshot every other figure comes from
  (`snapshot_at`; the bot keeps publishing through an outage instead of
  going stale, e.g. Lighter Core 502/503 on 2026-09-20 11:02–11:12Z).

A halted hedge holder shows as `degraded` and counts under the fleet's halts.
The pairtrade "Stats (lifetime)" block is hidden on this card: the bot has no
trades in that sense.

## Configuration and rollout

After the dashboard PR is merged and deployed (that workflow restarts
**debot-dashboard only**), edit `/opt/debot-dashboard/config.yaml` on the
Frankfurt host, after backing it up:

1. Remove the `Robinhood Freq` and `Robinhood B` targets (or keep them until
   the last `points_history` readout on bot-strategy#1046 is done — they only
   cost two stale cards).
2. Add, inside `targets`:

```yaml
  - name: Robinhood Hedge
    instance_id: i-0095af4fe0efbc5dd
    service: debot-xvenue-hedge-holder
    region: ap-northeast-1
    s3_bucket: debot-dashboard
    s3_region: eu-central-1
    s3_key: debot/status/robinhood-lighter/xvenue-hedge-holder.json
    dex: "Lighter (Robinhood Chain) / Lighter Core"
    bucket: subsidy
    subsidy:
      unit: points
      imputed_unit_value_usd: 8
      value_source_date: "2026-09-18"
```

3. Dashboard-only restart. Confirm `/api/status` has `status.hedge_holder`
   for the new target and that `status.subsidy` appears once the bot has been
   ARMed (it is absent before the first ARM: there is no points baseline to
   count from).

The `imputed_unit_value_usd` of $8 is the 12-month horizon of ~11M LIT
(~$36M) over ~85k points/week (bot-strategy#1046 Phase 0); the 6-month
horizon is ~$16. It is an assumption, dated, and the card says so. Set
`program_changed_on` when Lighter changes the program (the weekly
announcement is the only notice) and `kpi_reviewed_on` after re-evaluating.
