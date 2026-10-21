# Notification sink

A stand-in receiver for the catcher's outputs, for card design, e2e tests and
debugging (#52). It takes what the outputs send, keeps it in memory, and
lists **every notification once, with the channels it went to**. A tab per
system (MS Teams, Webhook) shows the posts as that system received them,
Teams posts as the card Teams renders.

It ships in the catcher's own binary/image as the `sink` subcommand, so the
card playground uses the catcher's real card builder (`BuildEnvelopeWithCard`),
not a copy. The UI follows the homerun2 family design (homerun2-core-catcher:
Pico CSS, `--hr-*` tokens, `BASE_PATH`, compact header in homerun2-portal).

```
omni-pitcher → redis → notification-catcher ─┬─ msteams output ──▶ real Teams (prod)
                                             ├─ msteams output ──▶ sink /teams/<channel>
                                             └─ webhook output ──▶ sink /webhook/<channel>
                                                                   (e.g. a stand-in for Slack, not connected yet)
```

| Sink route | Takes | Answers |
|---|---|---|
| `POST /teams/{channel}` | Exactly what a Teams Workflow webhook takes: `{"type":"message","attachments":[{"contentType":"application/vnd.microsoft.card.adaptive","content":{"type":"AdaptiveCard",…}}]}` | `202`, empty body, like Teams; `400` for anything else |
| `POST /webhook/{channel}` | Any JSON (the `webhook` output sends a `homerun.Message`) | `200`; `400` for invalid JSON |
| `POST /inbox` | A `homerun.Message` from a catch-all `webhook` output (no filters). Not a channel: it adds the messages that went to no channel to the list | `200`; `400` otherwise |

Posts of one catcher dispatch are grouped into one notification: same
severity, title and message (normalised as the Teams card builds them),
arriving within 30 s (the catcher's fan-out cap), each channel once. The
same message dispatched again becomes a new notification.

The sink proves *what the catcher sent*, not that Teams posted it, and it
only sees messages that matched an output pointed at it.

## Run it

```bash
go build .
./homerun2-notification-catcher sink --addr :8080 --capacity 500
```

| Flag / env | Default | Meaning |
|---|---|---|
| `--addr` / `SINK_ADDR` | `:8080` | Listen address |
| `--capacity` | `500` | Posts kept per channel; the oldest drop first |
| `SINK_TOKEN` | *(empty)* | When set, required on every route but `/health`/`/healthz`, as `Authorization: Bearer <token>` or `?token=<token>` |
| `BASE_PATH` | *(empty)* | URL prefix behind a reverse proxy, e.g. `/notify` behind homerun2-portal; health stays at the root |
| `LOG_FORMAT`, `LOG_LEVEL` | `json`, `info` | As for the catcher |

Storage is in memory: a restart empties it.

## Point the catcher at it

No catcher change is needed: give outputs the sink's URLs. The last path
segment is the channel name, free to choose. With a token, put it in the
query string, as Teams Workflow URLs carry their signature there.

```yaml
outputs:
  # real channel
  - name: teams-platform
    type: msteams
    webhook_url: ${TEAMS_WEBHOOK_URL}
    filters: { severity_min: warning }

  # same filter, recorded by the sink
  - name: teams-platform-sink
    type: msteams
    webhook_url: http://notify-sink.homerun2.svc:8080/teams/platform?token=${SINK_TOKEN}
    filters: { severity_min: warning }

  # a target that is not connected yet: see what it would get
  - name: slack-standin
    type: webhook
    url: http://notify-sink.homerun2.svc:8080/webhook/slack?token=${SINK_TOKEN}
    filters: { severity_min: critical }

  # optional: everything the catcher caught, also what matched no output
  - name: sink-inbox
    type: webhook
    url: http://notify-sink.homerun2.svc:8080/inbox?token=${SINK_TOKEN}
```

Or without Redis, through the smoke subcommand:

```bash
./homerun2-notification-catcher smoke --config notify.yaml --title "cluster built" --severity success
```

## HTML view (`GET /`)

Tabs, in the homerun2-core-catcher layout (filters, table, detail panel):

- **Notifications**: every notification once (title, severity, system,
  author, time) with a **Channels** column: the targets it went to, or
  *not routed* (needs the inbox). Filter by search, severity, system,
  channel. A click opens the details; a click on a channel opens that post
  in its system's tab.
- **MS Teams**: the Teams posts; the detail shows the card as Teams renders
  it (Microsoft's Adaptive Cards JS renderer, `adaptivecards@3.0.4` from
  unpkg, with a host config close to Teams dark or light; a plain renderer
  without internet), the notification preview line (`fallbackText`, #45)
  and the raw payload.
- **Webhook**: the webhook posts with their JSON payload.
- **Card playground**: paste a `homerun.Message` JSON and an output's
  `card:` YAML, and see the card the catcher would send. Invalid card
  configs are rejected with the config loader's error.

The tab is kept in the URL (`#msteams`, …). Add `?token=<token>` to the page
URL when a token is set.

## API

| Route | |
|---|---|
| `GET /api/notifications` | Notifications, newest first, each with `deliveries` (`postId`, `kind`, `channel`, `receivedAt`) and `inbox`. Query: `q` (title, message, system, tags), `limit` |
| `GET /api/messages` | Posts, newest first. Query: `kind` (`msteams`, `webhook`), `channel`, `since` (RFC3339), `after` (seq cursor), `objectId` or `q` (substring of the body), `fact=Title=Value` (repeatable, case-insensitive), `limit` |
| `GET /api/messages/{id}` | The posted body, as received |
| `DELETE /api/messages[?kind=&channel=]` | Clear matching posts (all without parameters) |
| `GET /api/channels` | `{"<kind>/<channel>": count}` |
| `GET /api/events` | Server-sent events, one `message` event per new post or inbox entry: `{"post": …, "notification": …}` |
| `POST /api/render` | `{"message": {…homerun.Message}, "card": "<card YAML>"}` → the envelope `BuildEnvelopeWithCard` builds |
| `PUT /api/faults/{kind}/{channel}` | Fault injection: `{"status":429,"delay":"2s","count":3}`. Waits `delay`, then answers `status` (if set) instead of accepting the post; `count` limits it to that many posts (0 = until cleared) |
| `DELETE /api/faults/{kind}/{channel}`, `GET /api/faults` | Clear / list faults |
| `GET /health`, `GET /healthz` | `ok`, at the root, never need the token |

Each listed post carries `id`, `seq`, `kind`, `channel`, `receivedAt`, `raw`,
`envelope` (Teams posts), and summary fields: `title`, `severity`, `system`,
`facts` (FactSet title → value, or the message fields for webhook posts),
`fallbackText` and `style` (header Container style) for Teams posts, and
`notificationId`.

## Asserting a notification in a test

```bash
# pitch, then poll until the card is there
for i in $(seq 1 60); do
  n=$(curl -s "http://notify-sink:8080/api/messages?kind=msteams&channel=platform&objectId=$OBJECT_ID" | jq length)
  [ "$n" -gt 0 ] && break; sleep 1
done
curl -s "http://notify-sink:8080/api/messages?objectId=$OBJECT_ID&fact=System=homerun2-git-pitcher" | jq -e 'length == 1'
```

`objectId` matches anywhere in the posted body, so it finds a post once the
payload carries the id (#49). Until then, match on facts or `q` with a
unique title.

`seq` grows with each post, so `after=<last seq seen>` is a cursor that
does not depend on clocks.
