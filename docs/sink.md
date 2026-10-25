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
| `POST /webhook/{channel}` | Any JSON (the `webhook` output sends a `homerun.Message` plus `objectId`) | `200`; `400` for invalid JSON |
| `POST /inbox` | A `homerun.Message` from a catch-all `webhook` output (no filters). Not a channel: it adds the messages that went to no channel to the list | `200`; `400` otherwise |

Posts of one catcher dispatch are grouped into one notification by the
message's `objectId` (#64): the card's footer, the `objectId` field of the
webhook JSON. Posts from catchers before v3.7, which send no id, group by
content instead: same severity, title and message (normalised as the Teams
card builds them). Either way within 30 s (the catcher's fan-out cap), each
channel once; the same message dispatched again becomes a new notification.

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
| `SINK_TOKEN` | *(empty)* | When set, required on every route but `/health`/`/healthz` and the page's static files, as `Authorization: Bearer <token>` or `?token=<token>` |
| `BASE_PATH` | *(empty)* | URL prefix behind a reverse proxy, e.g. `/notify` behind homerun2-portal; health stays at the root |
| `LOG_FORMAT`, `LOG_LEVEL` | `json`, `info` | As for the catcher |

Storage is in memory: a restart empties it.

## Deploy

### Published artifact

Each release publishes the sink as its own kustomize base, next to the
catcher's:

| Artifact | Profile | Contents |
|---|---|---|
| `ghcr.io/stuttgart-things/homerun2-notification-catcher-kustomize:<version>` | `tests/kcl-deploy-profile.yaml` | the catcher, no sink |
| `ghcr.io/stuttgart-things/homerun2-notification-catcher-sink-kustomize:<version>` | `tests/kcl-sink-profile.yaml` | `<name>-sink` ServiceAccount, Deployment, Service; image pinned to `<version>` |

A cluster opts in by deploying the second artifact as its own app (Argo
Application, Flux Kustomization) in the catcher's namespace. It has no
HTTPRoute (hostname and gateway are per cluster, the consumer adds one: a
`PathPrefix: /` route to Service `<name>-sink` port 80) and no token. PRs
that touch `kcl/` push both as `pr-<n>-<sha>`.

### KCL

The chart in `kcl/` deploys the sink when `config.sinkEnabled` is set (off
by default), next to the catcher, or alone with `config.catcherEnabled:
false`. It emits `<name>-sink`: a ServiceAccount (no token mounted), a
Deployment running the catcher image with `args: [sink]` (one replica, the
store is per pod), a Service, and optionally an HTTPRoute and a Secret for
`SINK_TOKEN`.

| Profile key | Default | Meaning |
|---|---|---|
| `config.catcherEnabled` | `true` | Emit the catcher; `false` renders the sink alone |
| `config.sinkEnabled` | `false` | Emit the sink ServiceAccount, Deployment and Service |
| `config.sinkCapacity` | `500` | `--capacity` |
| `config.sinkBasePath` | *(empty)* | `BASE_PATH`, and the HTTPRoute's `PathPrefix` (e.g. `/notify`) |
| `config.sinkToken` | *(empty)* | Emits Secret `<name>-sink` (key `token`) read as `SINK_TOKEN`; use a Flux token like `${SINK_TOKEN}` |
| `config.sinkServiceType` / `sinkServicePort` / `sinkContainerPort` | `ClusterIP` / `80` / `8080` | Service and container port |
| `config.sinkHttpRouteEnabled` | `false` | Emit an HTTPRoute (needs `sinkEnabled` and a parentRef name) |
| `config.sinkHttpRouteParentRefName` / `…Namespace` | *(empty)* | Gateway the route attaches to |
| `config.sinkHttpRouteHostname` | *(empty)* | Route hostname |
| `config.sinkHttpRouteAnnotations` | `{}` | Extra route annotations |

The profiles in `tests/` are flat `config.*` maps for the Dagger KCL module
(`task render-manifests-quick`); with the kcl CLI pass the keys as `-D`:

```bash
cd kcl && kcl run -D config.sinkEnabled=true -D config.sinkBasePath=/notify \
  -D config.sinkHttpRouteEnabled=true -D config.sinkHttpRouteParentRefName=<gateway> \
  -D config.sinkHttpRouteHostname=<host>
```

In-cluster the outputs reach it at
`http://<name>-sink.<namespace>.svc<sinkBasePath>/teams/<channel>` (Service
port 80). The pod's probes use `/health` at the root, which needs no token.

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
    webhook_url: http://homerun2-notification-catcher-sink.homerun2.svc/teams/platform?token=${SINK_TOKEN}
    filters: { severity_min: warning }

  # a target that is not connected yet: see what it would get
  - name: slack-standin
    type: webhook
    url: http://homerun2-notification-catcher-sink.homerun2.svc/webhook/slack?token=${SINK_TOKEN}
    filters: { severity_min: critical }

  # optional: everything the catcher caught, also what matched no output
  - name: sink-inbox
    type: webhook
    url: http://homerun2-notification-catcher-sink.homerun2.svc/inbox?token=${SINK_TOKEN}
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
  it (Microsoft's Adaptive Cards JS renderer, `adaptivecards@3.0.4`, with a
  host config close to Teams dark or light; a plain renderer if that script
  fails), the notification preview line (`fallbackText`, the envelope's `summary` when a card has none; #45) and the raw
  payload.
- **Webhook**: the webhook posts with their JSON payload.
- **Card playground**: paste a `homerun.Message` JSON (an `objectId` key in
  it becomes the card's footer) and an output's `card:` YAML, and see the
  card the catcher would send. Invalid card
  configs are rejected with the config loader's error.

The tab is kept in the URL (`#msteams`, …). `?objectId=<id>` filters the
notifications to that message and opens it (the catcher's event log links
there, [HTTP UI](http-ui.md)). Add `?token=<token>` to the page
URL when a token is set.

The page needs no internet: Pico CSS, the Adaptive Cards renderer, the
header font and the logo are embedded in the binary and served from
`<BASE_PATH>/static/` (no token, cached for a day). Versions, sources and
licenses: `internal/webui/static/vendor/README.md`.

## API

| Route | |
|---|---|
| `GET /api/notifications` | Notifications, newest first, each with `deliveries` (`postId`, `kind`, `channel`, `receivedAt`) and `inbox`. Query: `q` (title, message, system, tags), `limit` |
| `GET /api/messages` | Posts, newest first. Query: `kind` (`msteams`, `webhook`), `channel`, `since` (RFC3339), `after` (seq cursor), `objectId` (the message's id; for posts without one, the id anywhere in the body), `q` (substring of the body), `fact=Title=Value` (repeatable, case-insensitive), `limit` |
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

`assert-card` (same binary/image) polls `GET /api/messages` until posts
match, and fails at `--timeout` with the near misses: the posts that were
selected and the expectation each one failed.

```bash
notification-catcher assert-card \
  --sink http://homerun2-notification-catcher-sink.homerun2.svc \
  --channel cluster-build-watch --object-id "$OBJECT_ID" \
  --expect-fact System=cluster-build-watch --expect-severity success --timeout 60s
# OK post 7 msteams/cluster-build-watch "cluster built" [success]
```

On failure:

```
assert-card: no matching post (channel=cluster-build-watch&kind=msteams)
  post 7 msteams/cluster-build-watch "cluster built" [success]: fact System = "request-vm", want "cluster-build-watch"
```

| Flag | Default | |
|---|---|---|
| `--sink` / `SINK_URL` | *(required)* | Sink base URL, incl. `BASE_PATH` |
| `--token` / `SINK_TOKEN` | *(empty)* | Sent as `Authorization: Bearer` |
| `--kind` | `msteams` | `webhook`, or empty for both |
| `--channel` | | Selects posts to that channel |
| `--object-id` | | Selects the posts of this message (its `objectId`) |
| `--contains` | | Selects posts whose body contains it |
| `--since` | | Selects posts received after it: a duration ago (`10m`) or RFC3339 |
| `--expect-fact Title=Value` | | Repeatable; title and value case-insensitive |
| `--expect-title` | | Substring of the title |
| `--expect-severity` | | Severity, case-insensitive |
| `--count` | `1` | At least this many posts must match |
| `--timeout`, `--interval` | `60s`, `1s` | |

400, 401 and 404 from the sink fail at once; an unreachable sink and 5xx
are retried until the timeout.

From a pipeline, through Dagger (the image, or `--src .` for a branch):

```bash
dagger call -m github.com/stuttgart-things/homerun2-notification-catcher/dagger assert-card \
  --sink-url https://notification-sink.<cluster-fqdn> --channel cluster-build-watch \
  --object-id "$OBJECT_ID" --expect-fact System=cluster-build-watch --timeout 60s
```

A sink running as a Dagger service is passed with `--sink` (e.g.
`tcp://localhost:8080` from the host) and reached as `--sink-url
http://sink:8080`. `--token` takes a Dagger secret (`env://SINK_TOKEN`).

The `objectId` is the one omni-pitcher answers `/pitch` with. The catcher
passes it on from v3.7 (#64); for posts from an older catcher it matches
anywhere in the body, which usually means not at all. Then select by channel
and `--since`, or `--contains` a unique title.

Without the subcommand, `seq` grows with each post, so
`GET /api/messages?after=<last seq seen>` is a cursor that does not depend
on clocks.
