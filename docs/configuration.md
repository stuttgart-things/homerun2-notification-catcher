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
| `match` | exact equality on a `homerun.Message` field, case-insensitive on key + value. Allowed keys: `severity`, `system`, `author`, `title`, `assigneename`, `assigneeaddress`, and `status` (the alert state, see below). |
| `tags_contain` | any listed substring appears in `Tags` (case-insensitive). |
| `tags_exclude` | no listed substring appears in `Tags` (case-insensitive) -- e.g. `[demo=1]` keeps demo traffic out. |
| `message_contains` | any listed substring appears in `Message`. |
| `send_resolved` | `false` drops resolved alerts, as Alertmanager's `send_resolved` does. Unset or `true`: they pass. |

### Fallback outputs (#74)

`fallback: true` makes an output a catch-all: it takes a message that passes its own filters only when **no other, non-fallback output matched** it. "Every error nobody else routed", without repeating each route as an exclusion:

```yaml
  - name: teams-flux-errors
    type: msteams
    webhook_url: "${TEAMS_WEBHOOK_URL}"
    filters:
      match: { system: flux }
      severity_min: error
  - name: teams-platform-alerts         # errors from everything else
    type: msteams
    webhook_url: "${TEAMS_WEBHOOK_URL}"
    fallback: true
    filters:
      severity_min: error
```

A flux error goes to `teams-flux-errors` only; an error from any other system goes to `teams-platform-alerts`. A covered fallback reports `filtered` (in the event log, the metrics and the `dispatched` log line); `smoke` prints `SKIPPED (fallback: another output matched)`. Fallbacks do not cover each other.

"Matched" counts every non-fallback output whose filters pass, including a filterless catch-all such as the sink's `/inbox` webhook: beside one of those, a fallback never sends. Make the inbox a fallback too if it should only get what nothing else took.

### Firing and resolved alerts (#71)

The catcher reads an alert's state from the message: the `status=firing|resolved` tag omni-pitcher's `/pitch/alertmanager` sets, else `resolved` when the message opens with `Resolved: ` (as on `/pitch/grafana`). A message with neither is not an alert: `match: {status: …}` doesn't match it and `send_resolved` lets it pass.

A resolved alert's Teams card is green whatever the severity: the title is prefixed `RESOLVED: `, the message loses its own `Resolved: `, and the time row is labelled `Resolved` (omni-pitcher stamps a resolution with `endsAt`). A firing alert keeps its severity's colour.

Resolutions have severity `success`, below a `severity_min: warning` floor. To get them into the channel the alerts go to, add an output for them:

```yaml
  - name: teams-alerts
    type: msteams
    webhook_url: "${TEAMS_WEBHOOK_URL}"
    filters:
      match: { author: alertmanager }
      severity_min: warning
  - name: teams-alerts-resolved
    type: msteams
    webhook_url: "${TEAMS_WEBHOOK_URL}"
    filters:
      match: { author: alertmanager, status: resolved }
```

Without `severity_min`, `send_resolved: false` keeps an output to the firing alerts only.

### Preview text (msteams)

Where Teams cannot render a card (notifications, activity feed, chat previews, some clients) it shows only "Card - access it on https://go.skype.com/cards.unsupported" unless the payload carries text of its own. Every card carries one plain-text line, `[severity] title: body` (`[resolved] …` for a resolution, the body left out when it only repeats the title), twice: as the card's `fallbackText` and as the message's `summary` next to `attachments` (#45). For a card posted through a Power Automate Workflow, the preview may still read "Card": whether the Workflow passes `summary` on is up to Teams ([microsoft/AdaptiveCards#8647](https://github.com/microsoft/AdaptiveCards/issues/8647)).

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

Each row sets `title` and exactly one of `tag` (key looked up in the comma-separated `Tags`, case-insensitive) or `field` (`severity`, `system`, `author`, `title`, `message`, `tags`, `timestamp`, `url`, `assigneename`, `assigneeaddress`). A row whose value is empty is left out. `card` is rejected on a `webhook` output. `card.facts` works the same in the [rich layout](#rich-card-layout-msteams-82).

### Rich card layout (msteams, #82)

`card.layout: rich` switches an output to a card made to be scanned at a glance: a header coloured by severity with an emoji and a short headline, the full title and message below, the time in the reader's time zone, buttons, and the tags and `objectId` collapsed behind a **Details** button. Without `layout` (or with `layout: default`) the card stays exactly as described above.

| Part | Rendered as |
|---|---|
| Header | `Container` with `bleed: true` and the severity's style: `success` → `good` ✅, `warning` → `warning` ⚠️, `error` / `critical` → `attention` 🔥, `info` (and unknown) → `accent` ℹ️, `debug` → `default` 🐞. A resolved alert is `good` ✅ with `RESOLVED: ` before the headline, as in the default layout (#71). |
| Headline | emoji + the first `headline` template whose placeholders all resolve, else the message title; bold, large. |
| Subtitle | the first `subtitle` template whose placeholders all resolve, small and dimmed under the headline; none if no template resolves. |
| Avatar | `avatar: github`: `https://github.com/<author>.png`, round (`style: Person`), left of the headline — only when the author looks like a GitHub login and is not a tool or bot (`…[bot]`, `…-bot`, `grafana`, `alertmanager`, `flux`, `renovate`, …, or the message's `system`). `none` (default): no image. |
| Text | the full title (unless it is the headline) and the message (resolved: without its `Resolved: `). |
| Facts | `Author`, `System`, `Time` (`Resolved` for a resolution); `card.facts` replaces them as in the default layout. An RFC 3339 timestamp, also in a `field: timestamp` fact, is rendered as `{{DATE(<utc>, SHORT)}}, {{TIME(<utc>)}}`, which Teams shows in the reader's time zone; any other value as is. |
| Details | hidden `Container` (`id: details`): `Tags: …` and `objectId …` (the sink and `assert-card` still find it); the **Details** button (`Action.ToggleVisibility`) shows it. Left out, with its button, when the message has neither. |
| Buttons | `actions`: one `Action.OpenUrl` each; without `actions`, **Open** for the message's `url`. A button whose URL comes out empty or is not an absolute `http(s)` URL is left out. |
| Card | version `1.5`, `msteams.width: Full`; `fallbackText` / `summary` as in the default layout (#45). |

Templates are plain text with placeholders: `{tag:<key>}` (the value of `<key>=` in the message's comma-separated `tags`, case-insensitive) and `{field:<name>}` (a message field, the names `card.facts` takes). A template is all or nothing: if one of its placeholders is empty, the template is skipped, so a message without the tags (an older producer, a different event) falls back to the next template, then to the title — never `PR # merged`. `headline` and `subtitle` take one template or a list tried in order. Each action sets `title` and exactly one of `field` (a message field holding the URL) or `template`.

A git PR output (homerun2-git-pitcher sets `url` and the tags `repo`, `number`, `action`, `head`, `base`, `actor`):

```yaml
  - name: teams-git
    type: msteams
    webhook_url: "${TEAMS_WEBHOOK_URL_GIT}"
    filters:
      match: { system: homerun2-git-pitcher }
    card:
      layout: rich
      headline: "PR #{tag:number} {tag:action}"   # falls back to the title
      subtitle:
        - "{tag:repo} · {tag:head} → {tag:base}"
        - "{tag:repo}"
      avatar: github
      actions:
        - { title: "PR öffnen", field: url }
        - { title: Diff, template: "{field:url}/files" }
```

renders a merged PR as (envelope left out):

```json
{"type":"AdaptiveCard","version":"1.5","msteams":{"width":"Full"},
 "fallbackText":"[success] feat(platform-sthings): route the catcher's Teams outputs to their channels: Merged by patrick-hermann-sva",
 "body":[
  {"type":"Container","style":"good","bleed":true,"items":[{"type":"ColumnSet","columns":[
    {"type":"Column","width":"auto","items":[{"type":"Image","url":"https://github.com/patrick-hermann-sva.png","size":"Small","style":"Person","altText":"patrick-hermann-sva"}]},
    {"type":"Column","width":"stretch","verticalContentAlignment":"Center","items":[
      {"type":"TextBlock","text":"✅ PR #4404 merged","weight":"Bolder","size":"Large","wrap":true},
      {"type":"TextBlock","text":"stuttgart-things/stuttgart-things · feat/platform-sthings-teams-routing → main","isSubtle":true,"spacing":"None","wrap":true}]}]}]},
  {"type":"TextBlock","text":"feat(platform-sthings): route the catcher's Teams outputs to their channels","wrap":true},
  {"type":"TextBlock","text":"Merged by patrick-hermann-sva","wrap":true},
  {"type":"FactSet","facts":[{"title":"Author","value":"patrick-hermann-sva"},{"title":"System","value":"homerun2-git-pitcher"},
    {"title":"Time","value":"{{DATE(2026-11-07T21:47:06Z, SHORT)}}, {{TIME(2026-11-07T21:47:06Z)}}"}]},
  {"type":"Container","id":"details","isVisible":false,"items":[
    {"type":"TextBlock","text":"Tags: github, pull_request, repo=stuttgart-things/stuttgart-things, number=4404, …","isSubtle":true,"size":"Small","wrap":true},
    {"type":"TextBlock","text":"objectId abf0fc1e-…-homerun2-git-pitcher","isSubtle":true,"size":"Small","fontType":"Monospace","wrap":true}]}],
 "actions":[
  {"type":"Action.OpenUrl","title":"PR öffnen","url":"https://github.com/stuttgart-things/stuttgart-things/pull/4404"},
  {"type":"Action.OpenUrl","title":"Diff","url":"https://github.com/stuttgart-things/stuttgart-things/pull/4404/files"},
  {"type":"Action.ToggleVisibility","title":"Details","targetElements":["details"]}]}
```

An alerts output (omni-pitcher's `/pitch/alertmanager` sets `alertname`, `namespace`, `cluster`, `status`, …):

```yaml
  - name: teams-alerts
    type: msteams
    webhook_url: "${TEAMS_WEBHOOK_URL_ALERTS}"
    filters:
      match: { author: alertmanager }
    card:
      layout: rich
      headline: "{tag:alertname}"
      subtitle:
        - "{tag:namespace} · {tag:cluster}"
        - "{field:system}"
      actions:
        - { title: Grafana, field: url }
```

A firing `KubeJobFailed` warning gets a yellow header `⚠️ KubeJobFailed` over `flux-system · platform-sthings`; its resolution a green `✅ RESOLVED: KubeJobFailed` with a `Resolved` time. More cards (every case of the tests) are in [`internal/notify/testdata/cards`](https://github.com/stuttgart-things/homerun2-notification-catcher/tree/main/internal/notify/testdata/cards); the sink's card playground renders a `card:` block live.

A top-level `card:` is the default of every `msteams` output, merged field by field with the output's own (`layout`, `avatar`; `headline`, `subtitle`, `actions` and `facts` replace as a whole), so `card: {layout: rich}` at the top switches every Teams output and an output can go back with `card: {layout: default}`.

An unknown `layout` or `avatar`, a placeholder other than `{tag:<key>}` / `{field:<known field>}`, an empty template, or an action without `title` or with both or neither of `field` / `template` fails the config load at startup, naming the output and the key, e.g. `output[0] "teams-git": card.headline[0] "PR #{pr:number}": placeholder {pr:number}: want {tag:<key>} or {field:<name>}`.

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

The catcher UI's **Dead letter** tab lists them (with `HTTP_ADDR`, see
[HTTP UI](http-ui.md#dead-letter)), and `notification_catcher_dead_letter_pending`
counts them.

## Throttle (#79)

A guard for a channel that would otherwise get a flood, e.g. Kubernetes
events from every cluster. Optional at the top level, overridable per output
field by field like `retry`:

```yaml
throttle:                  # default for every output; leave out for none
  key: [author, title]     # fields that make a message "the same"; default author, system, title
  window: 1h               # a key sent within the window is not sent again
  max: 20                  # at most this many posts per window and output; 0: no cap
outputs:
  - name: teams-k8s-events
    type: msteams
    webhook_url: ${TEAMS_WEBHOOK_URL}
    throttle:
      max: 5               # unset fields fall back to the top-level throttle
  - name: pagerduty-events
    type: webhook
    url: https://events.pagerduty.com/v2/enqueue
    throttle:
      window: "0"          # this output is not throttled
```

| Field | |
|---|---|
| `key` | message fields joined into the key: `severity`, `system`, `author`, `title`, `message`, `tags`, `url`, `assigneename`, `assigneeaddress`, `status` |
| `window` | Go duration. A key is held back for `window` after it was sent; `max` counts over a window opened by the first message after the last one ended. Required (here or at the top level) when `key` or `max` is set; no window: no throttle |
| `max` | posts per window and output; `0` or unset: no cap |

A held-back message gets the result `throttled` in the summary log line,
the event log and `notification_catcher_dispatch_total{result="throttled"}`.
It is not sent and not dead-lettered, and its stream entry is acked.

Once an output reaches `max`, it holds back everything else until the window
ends and then sends exactly one post, `N notifications held back (throttle)`,
with the most frequent keys (checked every 15s, so it arrives shortly after
the window ends). A window in which only repeats were held back ends quietly.
With `DRY_RUN` the summary is only logged.

A resolved alert (`status=resolved` tag or a `Resolved: ` message) is never
held back and does not count towards `max`; it clears its key, so the next
firing one goes out again. The state is in memory, per pod: a restart forgets
the windows. `smoke` and `replay` bypass the throttle.

### Replay (#70)

`notification-catcher replay` sends the parked entries of one output again,
through that output's notifier with the config and card of now, oldest first
and each with the output's retry policy, and deletes each entry that goes
through. A failure stays parked; the command then exits 1. Filters are not
applied again: the message matched them when it was dispatched.

Run it in the catcher's pod, which has `CONFIG_PATH`, the `REDIS_*`
settings and `DEAD_LETTER_STREAM`:

```bash
kubectl -n homerun2 exec deploy/homerun2-notification-catcher -- \
  /ko-app/homerun2-notification-catcher replay --output teams-platform-alerts --dry-run
```

| Flag | |
|---|---|
| `--output` | the output to replay (required); an output that is no longer in the config is refused |
| `--object-id` | only the entry of this objectId |
| `--since` | only entries parked since then: a duration (`24h`) or an RFC 3339 time |
| `--dry-run` | list what would be sent; send and delete nothing |
| `--config`, `--stream`, `--timeout` | default: `CONFIG_PATH`, `DEAD_LETTER_STREAM`, 30s per entry |

```
- 1792681512800-0 obj-1 "disk 1" (parked 2026-10-22T15:05:12Z): SENT, deleted
- 1792681513042-0 obj-2 "disk 2" (parked 2026-10-22T15:05:13Z): FAIL (3 attempts): teams: webhook returned 503 (kept)
replayed 1, failed 1 (output teams-alerts, stream notification-catcher-dead-letter)
```

Without the UI, `redis-cli` reads the stream too:

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
