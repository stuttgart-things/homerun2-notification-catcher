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
| `HTTP_ADDR` | *(empty)* | Optional read-only UI + `/healthz` (e.g. `:8080`); see [HTTP UI](http-ui.md) |
| `AUTH_TOKEN` | *(empty)* | Token for the UI page and `/api/config` |
| `BASE_PATH` | *(empty)* | URL prefix of the UI behind a proxy |
| `EVENTS_CAPACITY` | `200` | Messages the UI's event log keeps |
| `SINK_URL` | *(empty)* | The sink's UI; the event log links each message there |
| `DEAD_LETTER_STREAM` | `notification-catcher-dead-letter` | Redis stream where sends that still fail after their retries are parked; `off`: only logged. See [Retries and dead letter](#retries-and-dead-letter) |
| `DEAD_LETTER_MAXLEN` | `10000` | The dead-letter stream is trimmed to about this many entries |
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

## Retries and dead letter

A send that fails with a transient error is retried with exponential
backoff (#68): connection errors and timeouts, `408`, `425`, `429` and
`5xx`. A `Retry-After` header is honoured. Any other `4xx` (a wrong URL, a
rejected payload) fails at once.

```yaml
retry:              # default for every output
  attempts: 3       # sends in total, the first included; 1 = no retry (max 10)
  backoff: 1s       # first wait, doubled each time, at most 10s per wait
outputs:
  - name: teams-platform-alerts
    type: msteams
    webhook_url: ${TEAMS_WEBHOOK_URL}
    retry:
      attempts: 5   # unset fields fall back to the top-level retry
```

Without a `retry` block the defaults are 3 attempts from 1s. Outputs run
in parallel for each message, so an output that retries through an outage
does not hold up the others. Messages still go out one at a time, so each
channel gets them in stream order. The whole fan-out of a message has 30s.
A wait that would not fit is skipped and the send counts as failed, which
keeps every entry well inside redisqueue's 60s visibility timeout.

A send that still fails is **parked** in the dead-letter stream
(`DEAD_LETTER_STREAM`), one entry per output and message:

| Field | |
|---|---|
| `output` | the output's name |
| `objectId`, `streamId` | the message's objectId and its entry in the source stream |
| `error`, `attempts`, `failedAt` | the last error, the sends made, when it gave up (RFC 3339) |
| `message` | the message as JSON (the webhook payload), so the entry outlives the pitcher's JSON document |

```bash
kubectl -n homerun2 exec redis-stack-node-0 -- redis-cli -a "$REDIS_PASSWORD" --no-auth-warning XRANGE notification-catcher-dead-letter - + COUNT 10
```

The stream entry is acked only when every matching output delivered or
parked the message. If the dead-letter write fails as well, the entry is
left pending and redisqueue delivers it again after its visibility timeout.
That repeats the outputs that did succeed, so delivery is at least once.
On SIGTERM, pending waits end: the failed outputs are parked right away
instead of holding up the shutdown.

With `DEAD_LETTER_STREAM=off`, a failed send is only logged (`notifier
failed`), as before #68.

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
