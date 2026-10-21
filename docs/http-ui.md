# HTTP UI

An optional, read-only HTTP surface on the catcher itself (#22): a health
check that pings Redis, and a page showing the routing config the catcher
loaded, secrets redacted. **Off by default**: without `HTTP_ADDR` the
catcher opens no port and stays a pure consumer.

It is not the [notification sink](sink.md). The sink receives what the
outputs send; this page shows which outputs exist and what their filters
are.

## Routes

| Route | Auth | |
|---|---|---|
| `GET /healthz`, `GET /health` | no | `200` when Redis answers, else `503`; always at the root (no `BASE_PATH`) |
| `GET <BASE_PATH>/` | token | The page: Redis state, streams, consumer group, dry run, and each output with its target, filters and card facts |
| `GET <BASE_PATH>/api/config` | token | The same as JSON |
| `GET <BASE_PATH>/static/…` | no | Page assets (Pico, font, logo), embedded, shared with the sink |

```json
{"status":"ok","redis":"ok","dispatcher_loaded":true,"dry_run":false,"outputs":3}
```

`redis` is `connecting` while the catcher still waits for Redis at startup
(the listener starts before that wait), `ok`, or `error: …`.

## Redaction

What the routing config interpolated from env (`${TEAMS_WEBHOOK_URL}`, …)
never reaches the page or the JSON:

- URL query values become `REDACTED`, while the keys stay. A Teams Workflow
  URL carries its `sig` in the query.
- Userinfo is replaced.
- Path segments that look like a key are replaced: 16 or more characters
  mixing letters and digits. This covers the legacy connector URL's secret
  and the workflow id.
- Header values are replaced, while the names stay.

## Configuration

| Env | Default | |
|---|---|---|
| `HTTP_ADDR` | *(empty: off)* | Listen address, e.g. `:8080` |
| `AUTH_TOKEN` | *(empty: open)* | Required on the page and `/api/config`, as `Authorization: Bearer <token>` or `?token=<token>` |
| `BASE_PATH` | *(empty)* | URL prefix behind a reverse proxy, e.g. `/catcher` |

## Deploy (KCL)

| Profile key | Default | |
|---|---|---|
| `config.httpEnabled` | `false` | Sets `HTTP_ADDR`, adds the container port, a readiness probe on `/healthz`, and Service `<name>` |
| `config.httpContainerPort` / `httpServicePort` | `8080` / `80` | |
| `config.httpBasePath` | *(empty)* | `BASE_PATH`, and the route's `PathPrefix` |
| `config.httpToken` | *(empty)* | `AUTH_TOKEN` in `<name>-secrets`; use a Flux token like `${CATCHER_UI_TOKEN}` |
| `config.httpRouteEnabled` | `false` | HTTPRoute `<name>` (needs `httpEnabled` and a parentRef name) |
| `config.httpRouteParentRefName` / `…Namespace` / `httpRouteHostname` / `httpRouteAnnotations` | | As for the sink's route |

```bash
cd kcl && kcl run -D config.httpEnabled=true -D config.httpRouteEnabled=true \
  -D config.httpRouteParentRefName=<gateway> -D config.httpRouteHostname=<host>
```

The readiness probe makes a pod that has lost Redis not ready. There is no
liveness probe: a catcher that cannot reach Redis at startup exits on its
own after `REDIS_STARTUP_TIMEOUT`.

The published kustomize base (`tests/kcl-deploy-profile.yaml`) leaves it
off. A cluster turns it on with a patch: the env, port, probe, Service and
route.

## Next

Phase 2 of #22: `/events`, the last messages with the result per output
(sent / filtered / dry-run / failed), linked to the sink by `objectId`.
