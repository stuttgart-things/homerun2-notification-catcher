# Teams mock

A Teams-shaped webhook sink for card design, e2e tests and debugging (#52).
It accepts exactly what a Teams Workflow webhook accepts, keeps the posts in
memory, and shows them as rendered Adaptive Cards and as JSON.

It ships in the catcher's own binary/image as a subcommand, so the card
playground uses the catcher's real card builder (`BuildEnvelopeWithCard`),
not a copy.

```
omni-pitcher → redis → notification-catcher ─┬─ msteams output ──▶ real Teams (prod)
                                             └─ msteams output ──▶ teams-mock (dev / e2e / design)
```

The mock proves *what the catcher sent*, not that Teams posted it.

## Run it

```bash
go build .
./homerun2-notification-catcher teams-mock --addr :8080 --capacity 500
```

| Flag / env | Default | Meaning |
|---|---|---|
| `--addr` / `TEAMS_MOCK_ADDR` | `:8080` | Listen address |
| `--capacity` | `500` | Posts kept per channel; the oldest drop first |
| `TEAMS_MOCK_TOKEN` | *(empty)* | When set, required on every route but `/healthz`, as `Authorization: Bearer <token>` or `?token=<token>` |
| `LOG_FORMAT`, `LOG_LEVEL` | `json`, `info` | As for the catcher |

Storage is in memory: a restart empties it.

## Point the catcher at it

No catcher change is needed: give an `msteams` output the mock's URL. The
last path segment is the channel name, free to choose. With a token, put it
in the query string, as Teams Workflow URLs carry their signature there.

```yaml
outputs:
  # real channel
  - name: teams-platform
    type: msteams
    webhook_url: ${TEAMS_WEBHOOK_URL}
    filters: { severity_min: warning }

  # same filter, recorded by the mock
  - name: teams-platform-mock
    type: msteams
    webhook_url: http://teams-mock.homerun2.svc:8080/webhook/platform?token=${TEAMS_MOCK_TOKEN}
    filters: { severity_min: warning }
```

Or without Redis, through the smoke subcommand:

```bash
./homerun2-notification-catcher smoke --config notify.yaml --title "cluster built" --severity success
```

## HTML view (`GET /`)

- One column per channel, newest first, updated live over server-sent events.
- Cards rendered with Microsoft's Adaptive Cards JS renderer
  (`adaptivecards@3.0.4` from unpkg) and a host config close to Teams light
  and dark. Without internet access the page falls back to a plain renderer
  (title, text, facts, severity stripe, Open link).
- Filter by text (title, facts, channel) and severity; **raw JSON** toggles
  the posted body; each card shows its `fallbackText`, the line notification
  previews show (#45).
- **Playground**: paste a `homerun.Message` JSON and an output's `card:`
  YAML, and see the card the catcher would send. Invalid card configs are
  rejected with the config loader's error.

Add `?token=<token>` to the page URL when a token is set.

## API

| Route | |
|---|---|
| `POST /webhook/{channel}` | Teams-shaped: `{"type":"message","attachments":[{"contentType":"application/vnd.microsoft.card.adaptive","content":{"type":"AdaptiveCard",…}}]}`. Answers `202` with an empty body, like Teams; `400` for anything else |
| `GET /api/messages` | Posts, newest first. Query: `channel`, `since` (RFC3339), `after` (seq cursor), `objectId` or `q` (substring of the body), `fact=Title=Value` (repeatable, case-insensitive), `limit` |
| `GET /api/messages/{id}` | The posted envelope JSON, as received |
| `DELETE /api/messages[?channel=]` | Clear one channel or all |
| `GET /api/channels` | `{"channel": count}` |
| `GET /api/events` | Server-sent events, one `message` event per new post |
| `POST /api/render` | `{"message": {…homerun.Message}, "card": "<card YAML>"}` → the envelope `BuildEnvelopeWithCard` builds |
| `PUT /api/faults/{channel}` | Fault injection: `{"status":429,"delay":"2s","count":3}`. Waits `delay`, then answers `status` (if set) instead of accepting the post; `count` limits it to that many posts (0 = until cleared) |
| `DELETE /api/faults/{channel}`, `GET /api/faults` | Clear / list faults |
| `GET /healthz` | `ok`, never needs the token |

Each listed post carries `id`, `seq`, `channel`, `receivedAt`, `envelope`,
`raw`, and summary fields taken from the card: `title` (first TextBlock),
`style` (header Container style: `attention`, `warning`, `good`, `default`),
`fallbackText`, `facts` (FactSet title → value).

## Asserting a card in a test

```bash
# pitch, then poll until the card is there
for i in $(seq 1 60); do
  n=$(curl -s "http://teams-mock:8080/api/messages?channel=platform&objectId=$OBJECT_ID" | jq length)
  [ "$n" -gt 0 ] && break; sleep 1
done
curl -s "http://teams-mock:8080/api/messages?objectId=$OBJECT_ID&fact=System=homerun2-git-pitcher" | jq -e 'length == 1'
```

`objectId` matches anywhere in the posted body, so it finds a card once the
card carries the id (#49). Until then, match on facts or `q` with a unique
title.

`seq` grows with each post, so `after=<last seq seen>` is a cursor that
does not depend on clocks.
