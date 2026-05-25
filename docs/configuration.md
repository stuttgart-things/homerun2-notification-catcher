# Configuration

Two layers:

1. **Environment variables** — Redis connection, logging, consumer group, the routing-config path.
2. **YAML routing config** — outputs and their filters. Loaded once at startup from `$CONFIG_PATH` (default `/etc/notification-catcher/config.yaml`). A missing or invalid file is a fatal startup error.

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `CONFIG_PATH` | `/etc/notification-catcher/config.yaml` | Path to the YAML routing config |
| `REDIS_ADDR` | `localhost` | Redis host |
| `REDIS_PORT` | `6379` | Redis port |
| `REDIS_PASSWORD` | *(empty)* | Redis password |
| `REDIS_STREAM` | `alerts` | Stream to consume from |
| `REDIS_STREAMS` | *(empty)* | Comma-separated streams (overrides `REDIS_STREAM`) |
| `CONSUMER_GROUP` | `homerun2-notification-catcher` | Consumer group name |
| `CONSUMER_NAME` | hostname | Consumer name within the group |
| `LOG_FORMAT` | `json` | `json` or `text` |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `DRY_RUN` | `false` | When truthy (`true`/`1`/`yes`/`on`), filter evaluation runs but Notifier.Send is skipped — matching outputs log "would send" at INFO instead. |

Additional env vars referenced by the YAML config (e.g. `TEAMS_WEBHOOK_URL`) must resolve at startup or load fails.

## YAML routing config

```yaml
outputs:
  - name: teams-platform-alerts
    type: msteams                       # msteams | webhook
    webhook_url: ${TEAMS_WEBHOOK_URL}
    filters:
      severity_min: warning             # debug<info<success<warning<critical=error
      match:                            # AND across keys, case-insensitive
        system: kubernetes
      tags_contain: [infra, storage]    # OR within the list
      message_contains: [disk, OOM]

  - name: pagerduty
    type: webhook
    url: https://events.pagerduty.com/v2/enqueue
    method: POST                        # default POST
    headers:
      Authorization: "Token token=${PAGERDUTY_TOKEN}"
    filters:
      severity_min: critical
```

### Filter semantics

All filter rules AND together. List-valued rules (`tags_contain`, `message_contains`) OR within the list. Empty filters block ⇒ output matches everything.

| Field | Semantics |
|---|---|
| `severity_min` | message rank ≥ rule rank. Unknown severities on messages count as `info`. |
| `match` | exact equality on a `homerun.Message` field, case-insensitive on key + value. Allowed keys: `severity`, `system`, `author`, `title`, `assigneename`, `assigneeaddress`. |
| `tags_contain` | any listed substring appears in `Tags` (case-insensitive). |
| `message_contains` | any listed substring appears in `Message`. |

### Env-var interpolation

`${VAR}` references in the YAML resolve from environment at load time. Empty or unset values are fatal — silent misconfig is the worst failure mode for a dispatch service.

For deployments via Flux + SOPS, see [Deployment](deployment.md) — the rendered ConfigMap uses `$${VAR}` (double-dollar) so Flux's substitution leaves a single `${VAR}` for the catcher to resolve at runtime.

## Smoke test

Fire one synthetic message through the configured outputs without Redis:

```bash
TEAMS_WEBHOOK_URL=https://teams.example/... \
  notification-catcher smoke \
    --config notify.yaml \
    --title "disk almost full" \
    --severity warning \
    --system kubernetes \
    --tags infra,storage
```

Each output prints one line: `OK`, `SKIPPED (filters did not match)`, `DRY-RUN`, or `FAIL: <error>`.

## Dry-run

Set `DRY_RUN=true` (process-wide) or pass `--dry-run` to the smoke subcommand to verify routing without firing any webhooks. Filter evaluation still runs — matching outputs log

```text
INFO dry-run: would send  output=teams-platform-alerts  title="disk almost full"  severity=warning  system=kubernetes
```

at INFO and the smoke result prints `DRY-RUN` instead of `OK`. Skipped outputs (filters didn't match) are reported the same way in both modes. This is the recommended first-reconciliation mode after a config change — flip it on, watch logs, confirm routing, then flip it back.
