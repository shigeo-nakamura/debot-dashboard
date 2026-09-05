# Arcus spot dashboard

Tracks bot-strategy#819. The current SPY/QQQ executor runs as a 15-minute
oneshot. The separate exporter reads its checkpoint, ledger, durable event
tail and systemd properties, then publishes an allowlisted status object.
It never invokes the executor, takes its runtime lock, calls an exchange,
signs, or writes bot state. The systemd unit also mounts the bot directories
read-only. There are no trading controls in the dashboard.

## Meanings and failure handling

- `ts` / `updated_at` describe the last **tick start**, not collection time.
  `arcus.exported_at` is a separate monitoring heartbeat, stale after 180s.
  Both the tick and market observation must be within `stale_after_secs`.
  Configure **1920s**: two missed 900s ticks plus 120s scheduling headroom.
  All other S3 targets retain their 180s default; the browser uses the same
  per-target value as the backend rather than its polling interval.
- Successful oneshots normally show systemd `inactive/dead`. Health checks
  use the exit result, actual execution timestamp and active/enabled timer.
  A normal `no_signal` hold is healthy. Failed ticks, disabled timers,
  unresolved executions and incomplete monitoring data are not healthy.
- Decisions are bound to the checkpoint's sequence, pair and mode. The
  latest event envelope's payload hash is checked; this is **not** a full
  event-chain audit. A matching pending event is explicitly marked pending;
  an older committed decision is never presented as the current decision.
  Changed checkpoints/service properties during collection are degraded.
- Managed inventory is checkpoint inventory valued at its saved reference
  marks, **not a freshly queried wallet balance**. Strategy loss compares
  that inventory with the daily/original basket at the same reference
  prices, floored at zero, matching runtime risk marks. Starting-basket
  drawdown measures price movement separately. None is realized trading
  PnL; gas costs are separate. The baseline UTC day is displayed even if old.
- Daily execution budget matches `ensure_execution_capacity`: all archived
  ledger attempts whose `updated_at` falls on today's UTC date, including
  rejections. The active attempt is not included in this counter. It is
  not a filled-swap counter. Any non-reconciled active attempt is degraded.
- Gas is the most recent reconciled ledger balance snapshot, shown with its
  own observation timestamp. It is **not a live RPC gas balance**. Missing
  balances/limits/losses display unknown rather than zero. Quote time is
  available only for a matching rotation plan; an observation is not a quote.
- Inventory and loss values do not enter fleet trading PnL, CAGR, win rate,
  position counts, charts or trading Prometheus gauges. Arcus participates
  in fleet health, halt counts and operational metrics.
- Source contracts: runtime checkpoint v1, execution ledger v2, event
  envelope v1. Unknown versions degrade monitoring. The public payload is
  schema v1. Projection uses floating point only for display, never orders.

## Rollout

Merge requires explicit user approval under the workspace AGENTS.md. The
existing dashboard deployment workflow builds the dashboard and refreshes
its web assets after merge; it does not install the exporter or change the
dashboard's target configuration. Complete the steps below separately.

1. Build the exporter from the reviewed commit for the Arcus host's CPU
   architecture (verified arm64/t4g.small on 2026-09-05; recheck if the host changes):

   ```bash
   GOOS=linux GOARCH=arm64 go build -trimpath -o dist/arcus-status-exporter ./cmd/arcus-status-exporter
   sha256sum dist/arcus-status-exporter
   ```

2. Copy that binary and the three `deploy/arcus-status-exporter.*` /
   `deploy/install-arcus-status-exporter.sh` files to a staging directory on
   Arcus (`i-0cb942c2950be020f`, eu-central-1). Before installation, run the
   binary as `arcus` without S3 flags/environment to preview its JSON:

   ```bash
   sudo -u arcus env -u ARCUS_STATUS_S3_BUCKET -u ARCUS_STATUS_S3_KEY ./arcus-status-exporter
   ```

   Verify pair/mode, ledger budget, observation times, and the actual systemd
   result against the existing executor. A failed tick must remain failed.
   No signer, transaction payload, arbitrary error detail or wallet address
   should appear. Do not copy raw runtime files to public artifacts.

3. Check S3 permissions, then install the monitor using the **build's** SHA:

   ```bash
   sudo bash deploy/install-arcus-status-exporter.sh ./arcus-status-exporter EXPECTED_SHA256
   sudo journalctl -u arcus-status-exporter.service -n 20 --no-pager
   aws s3 cp s3://debot-dashboard/arcus-archive/status/arcus-spot.json -
   ```

   Default destination is deliberately inside the existing Arcus archive
   prefix: as inspected on 2026-09-05, `debot-arcus-ec2` already has PutObject
   for `debot-dashboard/arcus-archive/*`. Recheck at rollout. No public ACL
   or bucket-wide grant is needed. The dashboard role must have GetObject
   on this exact key; if absent, add only that object permission. Object
   actions must not use the ListBucket-only `s3:prefix` condition. Bucket
   lifecycle rules may expire old status objects; the monitor refreshes the
   object every minute, and an absent object is an error, never healthy.

4. Back up `/opt/debot-dashboard/config.yaml` on the dashboard host. Append
   this target **inside its existing targets list**, preserving other
   targets and authentication:

   ```yaml
   - name: Arcus SPY/QQQ
     instance_id: i-0cb942c2950be020f
     service: arcus-spot-live-tick
     region: eu-central-1
     s3_bucket: debot-dashboard
     s3_region: eu-central-1
     s3_key: arcus-archive/status/arcus-spot.json
     stale_after_secs: 1920
   ```

   Restart **only `debot-dashboard`** to load its target config. No Arcus or
   Frankfurt trading-service restart is part of this rollout.

5. Verify the authenticated `/api/status` and desktop/mobile card show
   Arcus, threshold 1920, the correct last tick outcome and current fields.
   Watch one natural tick and a subsequent exporter update. Verify the
   exporter heartbeat advances without changing the bot timestamp between
   ticks. Do not trigger a live tick to test monitoring. Production rollout
   and the natural-tick check remain acceptance items until performed.

## Rollback

Disable only `arcus-status-exporter.timer` and stop its monitoring service;
restore the dashboard config backup and restart only `debot-dashboard`.
Trading config, runtime state and executor units are untouched. An old S3
object becomes stale after the independent 180s monitoring timeout.
