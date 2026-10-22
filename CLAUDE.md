# CLAUDE.md

## Project

homerun2-notification-catcher — Go consumer for the homerun2 pipeline. Reads `homerun.Message` records from a Redis Stream (consumer group) and **dispatches them as notifications** to external channels (MS Teams + generic JSON webhook today; Slack / email later). Routing is driven by a YAML config so the catcher behaves like every other `*-catcher` consumer — read the stream, do something with each message — rather than being Alertmanager-specific.

## Tech Stack

- **Language**: Go 1.25.5
- **Consumer**: Redis Streams via `redisqueue` (consumer groups)
- **Library**: `homerun-library/v4` for shared types and helpers
- **Build**: ko (`.ko.yaml`), no Dockerfile
- **CI**: Dagger modules (`dagger/main.go`), Taskfile
- **Deploy**: KCL manifests (`kcl/`), Kustomize, Kubernetes (added in #8); sink opt-in via `config.sinkEnabled` (#54); read-only UI opt-in via `config.httpEnabled` (#22)
- **Infra**: GitHub Actions, semantic-release, renovate

## Architecture

```
producer (omni-pitcher /pitch, /pitch/grafana, /pitch/alertmanager, …)
        │
        ▼
Redis stream "alerts" (or any stream — see REDIS_STREAM[S])
        │
        ▼
notification-catcher ── LogHandler ──► stdout
        │
        └── Dispatcher ──► outputs that pass filters
                              │
                  ┌───────────┼───────────┐
                  ▼           ▼           ▼
              MS Teams   Generic         (future:
              (Adaptive   webhook         Slack, email, …)
               Card)
```

Each caught `homerun.Message` is handed to two `MessageHandler`s: the `LogHandler` (always-on observability) and the `Dispatcher` (loaded from YAML at startup, fans out to matching outputs).

## Routing config

Loaded once at startup from `$CONFIG_PATH` (default `/etc/notification-catcher/config.yaml`). A missing or invalid file is a fatal startup error. `${VAR}` references in the YAML are interpolated from env vars; a missing/empty value is also a fatal error.

The Deployment always mounts a ConfigMap named `<name>-notify` (key `config.yaml`) at that path, but **the chart does not emit it** — the caller (cluster overlay) provides it, mirroring the `homerun2-git-pitcher-watch-config` pattern. See `docs/deployment.md`.

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
      tags_exclude: [demo=1]            # none of these may appear
      message_contains: [disk, OOM]

  - name: webhook-generic
    type: webhook
    url: https://hooks.example/notify
    method: POST                        # default POST
    headers:
      Authorization: "Bearer ${GENERIC_WEBHOOK_TOKEN}"
    filters:
      severity_min: critical
```

Filter semantics (all AND together):

| Field | Meaning |
|---|---|
| `severity_min` | message rank ≥ rule rank; unknown severities on messages count as `info` |
| `match` | exact equality on a homerun.Message field (case-insensitive on key + value). Allowed keys: `severity`, `system`, `author`, `title`, `assigneename`, `assigneeaddress`, `status` (alert state, `notify.AlertStatus`). |
| `tags_contain` | any listed substring appears in `Tags` (OR within the list, case-insensitive) |
| `message_contains` | any listed substring appears in `Message` |
| `send_resolved` | `false` drops resolved alerts |

Firing vs. resolved (#71): `notify.AlertStatus` reads the `status=` tag (omni-pitcher `/pitch/alertmanager`), else a `Resolved: ` message prefix (`/pitch/grafana`). A resolved card is green with a `RESOLVED: ` title and a `Resolved` time row.

Per-output failures are logged but don't block the rest of the fan-out. See `notify.example.yaml` for a full example.

Retries (#68): `retry: {attempts, backoff}` at the top level, overridable per output (defaults 3 attempts from 1s, doubling, ≤10s per wait). Transient errors only (connection, timeout, 408/425/429, 5xx, `Retry-After` honoured). Outputs run in parallel per message, messages one at a time; the fan-out has 30s (`dispatchTimeout`, below redisqueue's 60s visibility timeout). A send that still fails is parked in `DEAD_LETTER_STREAM`; the stream entry is acked only when every output delivered or parked it (`notify.Undelivered`, handler error → no ack → redelivered). `Dispatcher.Stop` on SIGTERM ends pending waits.

## Git Workflow

Branch-per-issue with PR and merge to main.

### Branch naming

- `feat/<issue>-<short>` for features
- `fix/<issue>-<short>` for bugs
- `test/<issue>-<short>` for test-only changes
- `chore/<issue>-<short>` for infra/CI changes

### Commit messages

- Conventional commits: `fix:`, `feat:`, `test:`, `chore:`, `docs:`
- Include `Closes #<issue-number>` to auto-close issues

## Code Conventions

- No Dockerfile — use ko for image builds
- Config via environment variables (Redis, logging) + YAML (routing), all loaded once at startup
- Unit tests must not require Redis or network; integration tests run via Dagger with a Redis service
- `Catcher` interface allows pluggable backends; `MockCatcher` for tests
- `Notifier` interface for each sink; `Dispatcher` walks the configured outputs synchronously per message

## Key Paths

- `main.go` — entrypoint, server-mode bootstrap, signal handling
- `main.go` also holds the `smoke`, `sink` and `assert-card` subcommands (one file: the Dagger Go module builds `main.go` alone)
- `internal/catcher/catcher.go` — RedisCatcher with JSON.GET payload resolution
- `internal/catcher/handlers.go` — `LogHandler` and `severityToLevel`
- `internal/catcher/mock.go` — `MockCatcher` for tests
- `internal/config/config.go` — Redis + logging env config
- `internal/config/notify.go` — YAML routing config loader (env interp, validation, severity ranking table)
- `internal/notify/notifier.go` — `Notifier` interface
- `internal/notify/teams.go` — MS Teams Adaptive-Card notifier
- `internal/notify/webhook.go` — generic JSON webhook notifier
- `internal/notify/filter.go` — `Matches(filters, msg)` predicate
- `internal/notify/status.go` — `AlertStatus`: firing / resolved from the `status=` tag or the `Resolved: ` prefix (#71)
- `internal/notify/retry.go` — `Retryable` (`StatusError`, permanent errors), the backoff loop, `DeadLetter` interface, `Stop`, `Undelivered` (#68)
- `internal/catcher/deadletter.go` — `RedisDeadLetter`: XADD to the dead-letter stream with the message JSON
- `internal/notify/dispatcher.go` — fan-out across configured outputs (in parallel per message, #68); one summary log line per message (`dispatched` with each output's result, or `no output matched`, #49) and the `OnDispatch` hook
- `internal/sink/` — `notification-catcher sink`: stand-in receiver for the outputs (`/teams/<channel>` Teams-shaped, `/webhook/<channel>` any JSON, optional catch-all `/inbox`), in-memory store grouping posts per dispatched message into notifications, JSON API, HTML view in the homerun2-core-catcher design (Pico, `--hr-*` tokens, `BASE_PATH`): Notifications tab with a Channels column, a tab per system (Teams card preview), card playground; fault injection; page assets (Pico, adaptivecards, font, logo) vendored under `internal/webui/static/vendor/`, no CDN; see `docs/sink.md`
- `internal/sink/assert.go` — `AssertClient.AssertPosts`: the polling behind `assert-card` (selection on the sink's side, expectations checked locally to report near misses)
- `internal/server/` — optional read-only HTTP surface (#22, `HTTP_ADDR`): `/healthz` (Redis ping, 503 while connecting/down), the loaded config at `<BASE_PATH>/` and `/api/config` with secrets redacted (`RedactURL`, header values); `<BASE_PATH>/events` + `/api/events`: in-process ring buffer of dispatched messages with each output's result (`events.go`, fed by `Dispatcher.OnDispatch`, links to the sink via `SINK_URL`); `/metrics` (`notification_catcher_dispatch_total{output,result}`, hand-written text format, no client lib); `AUTH_TOKEN`; see `docs/http-ui.md`
- `internal/webui/` — shared by the sink and the UI: embedded page assets (`static/`, vendored, no CDN), `ParseBasePath`, token `Auth`
- `internal/models/models.go` — `CaughtMessage` (homerun.Message + stream metadata); what `Dispatch` and every `Notifier.Send` take, so the `objectId` reaches the payloads (card footer, webhook `objectId` field, delivery log lines; #64)
- `internal/models/card.go` — Adaptive Card + Power Automate envelope structs
- `dagger/main.go` — CI functions (Lint, Build, BuildImage, ScanImage, BuildAndTestBinary, IntegrationTest, AssertCard)
- `kcl/` — *(added in #8)* KCL deployment manifests; `config.sinkEnabled` adds the sink (`<name>-sink` ServiceAccount, Deployment, Service, optional HTTPRoute + token Secret, #54); `config.catcherEnabled: false` renders it alone
- `tests/kcl-deploy-profile.yaml` / `tests/kcl-sink-profile.yaml` — flat `config.*` profiles (Dagger KCL module, not `kcl run -Y`) for the two published kustomize bases: `…-kustomize` (catcher) and `…-sink-kustomize` (sink only)
- `notify.example.yaml` — annotated reference config

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `CONFIG_PATH` | `/etc/notification-catcher/config.yaml` | YAML routing config path; missing file is fatal |
| `REDIS_ADDR` | `localhost` | Redis host |
| `REDIS_PORT` | `6379` | Redis port |
| `REDIS_PASSWORD` | *(empty)* | Redis password |
| `REDIS_STREAM` | `messages` | Redis stream to consume from |
| `REDIS_STREAMS` | *(empty)* | Comma-separated streams (overrides `REDIS_STREAM`) |
| `CONSUMER_GROUP` | `homerun2-notification-catcher` | Consumer group name |
| `CONSUMER_NAME` | hostname | Consumer name within the group |
| `CONSUMER_START_ID` | `$` | Where a **newly created** consumer group starts: `$` (only new messages), `0` (whole stream), or a stream ID. An existing group keeps its position |
| `REDIS_STARTUP_TIMEOUT` | `120s` | How long startup retries Redis before exiting (Go duration); SIGINT/SIGTERM ends the wait with exit 0 |
| `HTTP_ADDR` | *(empty)* | Listen address of the optional read-only UI + `/healthz` (#22), e.g. `:8080`; empty: no listener |
| `AUTH_TOKEN` | *(empty)* | Bearer / `?token=` for the UI page and `/api/config` (not `/healthz`) |
| `BASE_PATH` | *(empty)* | URL prefix of the UI behind a proxy |
| `EVENTS_CAPACITY` | `200` | Messages the UI's event log keeps |
| `SINK_URL` | *(empty)* | The sink's UI; the event log links each objectId to `<SINK_URL>/?objectId=…` |
| `DEAD_LETTER_STREAM` | `notification-catcher-dead-letter` | Redis stream for sends that fail after their retries (#68); `off`: only logged |
| `DEAD_LETTER_MAXLEN` | `10000` | Approximate cap of the dead-letter stream |
| `LOG_FORMAT` | `json` | Log format: `json` or `text` |
| `LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `DRY_RUN` | `false` | When truthy (`true`/`1`/`yes`/`on`), filter evaluation runs as usual but matching outputs log "would send" at INFO and skip the Notifier.Send call. Use on first reconciliation after a config change to verify routing without spamming Teams. |
| (referenced by YAML) | — | e.g. `TEAMS_WEBHOOK_URL`, custom webhook tokens — must resolve at startup |

## Smoke test (CLI)

Fire one synthetic message through the YAML-configured outputs without Redis:

```bash
TEAMS_WEBHOOK_URL=https://teams.example/... \
  notification-catcher smoke \
    --config notify.example.yaml \
    --title "disk almost full" \
    --severity warning \
    --system kubernetes \
    --tags infra,storage
```

Prints one line per output (`OK` / `SKIPPED` / `DRY-RUN` / `FAIL: …`). Useful for verifying a new output or filter before deploying.

Add `--dry-run` (or set `DRY_RUN=true`) to test routing without firing any webhooks:

```bash
notification-catcher smoke --config notify.yaml --severity warning --dry-run
```

## Notification sink (CLI)

A stand-in receiver for card design and e2e assertions (#52). Point an `msteams` output at `http://<sink>/teams/<channel>` or a `webhook` output at `http://<sink>/webhook/<channel>`; `/` lists each notification once with the channels it went to (tabs per system; Teams posts rendered as Teams shows them); the API has `GET /api/notifications` and `GET /api/messages?kind=&channel=&objectId=&fact=Title=Value`. A catch-all webhook output to `/inbox` adds messages that matched no output.

```bash
notification-catcher sink --addr :8080   # SINK_TOKEN, BASE_PATH optional
```

`notification-catcher assert-card --sink <url> --channel … --object-id … --expect-fact System=… --timeout 60s` polls the sink until a post matches, else exits 1 with the near misses (#62); Dagger `assert-card` runs it from the image or `--src`.

## Testing

```bash
# Unit tests (no Redis or network)
go test ./...

# Integration test via Dagger (spins up Redis)
task build-test-binary

# Lint
task lint

# Build + scan image
task build-scan-image-ko
```

## Reference Projects

- `homerun2-core-catcher` — scaffold source; consumer group, ko, KCL, Dagger CI, semantic-release patterns
- `homerun2-omni-pitcher` — producer that feeds the `alerts` stream via `/pitch/alertmanager` (Alertmanager, ≥ v2.4.0) and `/pitch/grafana`
