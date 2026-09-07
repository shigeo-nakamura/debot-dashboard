# Book runtime targets

The book runtime (`book-runtime`, bot-strategy#937) hosts the slow
cross-sectional strategies: XSMOM (#695) today, Engine B (#866) once its
gate passes. It mirrors `status.json` to S3 exactly like pairtrade, so a
target needs nothing beyond the usual `s3_bucket` / `s3_key` pair.

```yaml
  - name: XSMOM
    instance_id: i-0095af4fe0efbc5dd
    service: book-runtime-xsmom-695
    region: ap-northeast-1
    s3_bucket: debot-dashboard
    s3_region: eu-central-1
    s3_key: debot/status/book-xsmom-695/status.json
    dex: "Lighter (zkLighter mainnet)"
```

The bot side sets `STATUS_S3_BUCKET=debot-dashboard` and
`STATUS_S3_KEY_PREFIX=debot/status/book-<instance>` in its unit, and
writes the object every 30 s.

## What the panel shows

The runtime has real positions and PnL, so the normal trading view still
renders; the **Book runtime** section is added below it.

| Row | Meaning |
|---|---|
| Decision | The last *completed* decision: its key, outcome (`applied` / `partial` / `rejected` / `skipped` / `halted`) and, when it took more than one, the attempt count. |
| Signal | The window in progress, from the runtime's own `signal_status`: `waiting_for_file`, `applied:<sha12>`, `partial:<sha12>`, `rejected:<reason>`, `skipped:<reason>`, `waiting_flatten:<key>`. Green when applied, amber when partial or rejected. |
| Book | Gross exposure, net exposure and equity. A dollar-neutral book should show net close to zero; a persistent non-zero net means legs are missing. |
| Next decision | The key and UTC time of the next scheduled decision. |
| Note | Only when something is wrong: a session or daily halt with its reason, a venue-equity outage (which blocks every opening intent), a residual still being retried, or a fixed-window flatten still owed. |

## Health

A target counts as unhealthy, is auto-expanded, and adds to the fleet halt
count when any of these hold:

- `book.session_halted` — sticky drawdown halt; the book was flattened and
  stays blocked until the operator consumes the `RISK_ACK` file.
- `book.daily_halted` — daily loss limit; openings blocked until the next
  UTC day.
- `book.equity_ready == false` — the live venue equity read is failing, so
  the risk rails cannot be evaluated and every opening intent is blocked.
  Reductions and flattens still run.

A `pending_residual` is deliberately *not* a halt: the runtime is still
working the window and will retry inside it.

Operational detail (config, signal contract, risk rails, runbook) lives in
the pairtrade repo: `docs/book-runtime.md` and
`docs/book-runtime-operations.md`.
