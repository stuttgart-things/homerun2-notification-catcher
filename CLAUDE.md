# CLAUDE.md

## Project

homerun2-notification-catcher — Go consumer for the homerun2 pipeline. Reads `homerun.Message` records from a Redis Stream (consumer group) and **dispatches them as notifications** to external channels (MS Teams + generic JSON webhook today; Slack / email later). Routing is driven by a YAML config so the catcher behaves like every other `*-catcher` consumer — read the stream, do something with each message — rather than being Alertmanager-specific.

## Tech Stack

- **Language**: Go 1.25.5
- **Consumer**: Redis Streams via `redisqueue` (consumer groups)
- **Library**: `homerun-library/v3` for shared types and helpers
- **Build**: ko (`.ko.yaml`), no Dockerfile
- **CI**: Dagger modules (`dagger/main.go`), Taskfile
- **Deploy**: KCL manifests (`kcl/`), Kustomize, Kubernetes (added in #8)
- **Infra**: GitHub Actions, semantic-release, renovate

## Architecture

```
producer (omni-pitcher /pitch, /pitch/grafana, …)
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
| `match` | exact equality on a homerun.Message field (case-insensitive on key + value). Allowed keys: `severity`, `system`, `author`, `title`, `assigneename`, `assigneeaddress`. |
| `tags_contain` | any listed substring appears in `Tags` (OR within the list, case-insensitive) |
| `message_contains` | any listed substring appears in `Message` |

Per-output failures are logged but don't block the rest of the fan-out. See `notify.example.yaml` for a full example.

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
- `smoke.go` — `notification-catcher smoke …` subcommand (in-process, no Redis)
- `internal/catcher/catcher.go` — RedisCatcher with JSON.GET payload resolution
- `internal/catcher/handlers.go` — `LogHandler` and `severityToLevel`
- `internal/catcher/mock.go` — `MockCatcher` for tests
- `internal/config/config.go` — Redis + logging env config
- `internal/config/notify.go` — YAML routing config loader (env interp, validation, severity ranking table)
- `internal/notify/notifier.go` — `Notifier` interface
- `internal/notify/teams.go` — MS Teams Adaptive-Card notifier
- `internal/notify/webhook.go` — generic JSON webhook notifier
- `internal/notify/filter.go` — `Matches(filters, msg)` predicate
- `internal/notify/dispatcher.go` — fan-out across configured outputs
- `internal/models/models.go` — `CaughtMessage` (homerun.Message + stream metadata)
- `internal/models/card.go` — Adaptive Card + Power Automate envelope structs
- `dagger/main.go` — CI functions (Lint, Build, BuildImage, ScanImage, BuildAndTestBinary, IntegrationTest)
- `kcl/` — *(added in #8)* KCL deployment manifests
- `notify.example.yaml` — annotated reference config

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `CONFIG_PATH` | `/etc/notification-catcher/config.yaml` | YAML routing config path; missing file is fatal |
| `REDIS_ADDR` | `localhost` | Redis host |
| `REDIS_PORT` | `6379` | Redis port |
| `REDIS_PASSWORD` | *(empty)* | Redis password |
| `REDIS_STREAM` | `alerts` | Redis stream to consume from |
| `REDIS_STREAMS` | *(empty)* | Comma-separated streams (overrides `REDIS_STREAM`) |
| `CONSUMER_GROUP` | `homerun2-notification-catcher` | Consumer group name |
| `CONSUMER_NAME` | hostname | Consumer name within the group |
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
- `homerun2-omni-pitcher` — producer that feeds the `alerts` stream via `/pitch/grafana`
