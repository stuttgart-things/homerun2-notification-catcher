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
| `REDIS_STREAM` | `messages` | Stream to consume from |
| `REDIS_STREAMS` | *(empty)* | Comma-separated streams (overrides `REDIS_STREAM`) |
| `CONSUMER_GROUP` | `homerun2-notification-catcher` | Consumer group name |
| `CONSUMER_NAME` | hostname | Consumer name within the group |
| `CONSUMER_START_ID` | `$` | Where a **newly created** consumer group starts: `$` (only new messages), `0` (whole stream), or a stream ID. An existing group keeps its position |
| `REDIS_STARTUP_TIMEOUT` | `120s` | How long startup retries Redis before exiting (Go duration); SIGINT/SIGTERM ends the wait with exit 0 |
| `LOG_FORMAT` | `json` | `json` or `text` |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `DRY_RUN` | `false` | When truthy (`true`/`1`/`yes`/`on`), filter evaluation runs but Notifier.Send is skipped — matching outputs log "would send" at INFO instead. |

Additional env vars referenced by the YAML config (e.g. `TEAMS_WEBHOOK_URL`) must resolve at startup or load fails.

### Where a new instance starts reading

A notification is about *now*. Replaying a stream's history would post every
old alert or result to a real channel again, so the catcher never does that
unless asked:

- **New consumer group** (first start, a new instance, a newly added stream,
  or after the group was deleted): the group is created at `$`, and only
  messages pitched **after** it exists are handled. Set `CONSUMER_START_ID=0`
  to replay the whole stream instead, or a stream ID to start after that entry.
- **Existing consumer group** (a restart): the group keeps its position, so
  messages pitched while the catcher was down are still delivered.
  `CONSUMER_START_ID` has no effect on an existing group.

Messages are handled **one at a time, in stream order**. A burst (two alerts,
or a point and the match-winning point, pitched milliseconds apart) reaches the
channel in the order it happened. The cost: one slow or unreachable output
delays the messages behind it, up to the dispatch timeout per message.

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

All filter rules AND together. List-valued rules (`tags_contain`, `tags_exclude`, `message_contains`) OR within the list. Empty filters block ⇒ output matches everything.

| Field | Semantics |
|---|---|
| `severity_min` | message rank ≥ rule rank. Unknown severities on messages count as `info`. |
| `match` | exact equality on a `homerun.Message` field, case-insensitive on key + value. Allowed keys: `severity`, `system`, `author`, `title`, `assigneename`, `assigneeaddress`. |
| `tags_contain` | any listed substring appears in `Tags` (case-insensitive). |
| `tags_exclude` | no listed substring appears in `Tags` (case-insensitive) -- e.g. `[demo=1]` keeps demo traffic out. |
| `message_contains` | any listed substring appears in `Message`. |

### Card facts (msteams)

By default a Teams card shows the title, the message and a FactSet of severity, system, author, tags and time, and below it, small and dimmed, the message's `objectId` (the id omni-pitcher answered `/pitch` with, to look it up in the logs or the sink; #64). A `webhook` output sends the `homerun.Message` JSON with an added `objectId` field. `card.facts` replaces the FactSet row for row (the footer stays), so a producer that packs its data into `key=value` tags gets one readable row per value:

```yaml
  - name: teams-tabletennis-results
    type: msteams
    webhook_url: "${TEAMS_WEBHOOK_URL}"
    filters:
      match: { system: tabletennis }
      tags_contain: [transition=match_won]
      tags_exclude: [demo=1]
    card:
      facts:
        - { title: Spieler A, tag: player_a }   # value of player_a=... in Tags
        - { title: Spieler B, tag: player_b }
        - { title: Sätze, tag: sets }
        - { title: Zeit, field: timestamp }     # a message field
```

Each row sets `title` and exactly one of `tag` (key looked up in the comma-separated `Tags`, case-insensitive) or `field` (`severity`, `system`, `author`, `title`, `message`, `tags`, `timestamp`, `url`, `assigneename`, `assigneeaddress`). A row whose value is empty is left out. `card` is rejected on a `webhook` output.

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
