# CLAUDE.md

## Project

homerun2-notification-catcher — Go consumer for the homerun2 pipeline. Reads `homerun.Message` records from a Redis Stream (consumer group) and **dispatches them as notifications** to external channels (MS Teams first; Slack / email / generic webhook to follow). Outbound counterpart to the existing `*-catcher` consumers.

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
Alertmanager ─▶ omni-pitcher /pitch/grafana ─▶ Redis stream "alerts"
                                                     │
                                          notification-catcher
                                                     │
                                       ┌─────────────┼─────────────┐
                                       ▶ MS Teams    ▶ Slack        ▶ email
```

Each caught `homerun.Message` is fanned out to one or more `MessageHandler`s.
Phase 1 ships with `LogHandler` only; `internal/notify/` (Teams notifier +
router) is added in #6 / #7.

## Git Workflow

Branch-per-issue with PR and merge. Every change gets its own branch, PR, and merge to main.

### Branch naming

- `feat/<issue-number>-<short-description>` for features
- `fix/<issue-number>-<short-description>` for bugs
- `test/<issue-number>-<short-description>` for test-only changes
- `chore/<issue-number>-<short-description>` for infra/CI changes

### Commit messages

- Conventional commits: `fix:`, `feat:`, `test:`, `chore:`, `docs:`
- Include `Closes #<issue-number>` to auto-close issues

## Code Conventions

- No Dockerfile — use ko for image builds
- Config via environment variables, loaded once at startup
- Unit tests must not require Redis; integration tests run via Dagger with a Redis service
- `Catcher` interface allows pluggable backends; `MockCatcher` for tests
- Pluggable `MessageHandler`s: log first, notify (Teams) added in #6

## Key Paths

- `main.go` — entrypoint, consumer wiring, signal handling
- `internal/catcher/catcher.go` — RedisCatcher with JSON.GET payload resolution
- `internal/catcher/handlers.go` — `LogHandler` and `severityToLevel`
- `internal/catcher/mock.go` — `MockCatcher` for tests
- `internal/config/` — env-based config loading + slog setup
- `internal/models/models.go` — `CaughtMessage` (homerun.Message + stream metadata)
- `internal/notify/` — *(added in #6)* notifier interface, Teams notifier, router
- `dagger/main.go` — CI functions (Lint, Build, BuildImage, ScanImage, BuildAndTestBinary, IntegrationTest)
- `kcl/` — *(added in #8)* KCL deployment manifests
- `Taskfile.yaml`, `.ko.yaml`, `.github/workflows/` — task runner, ko build, CI

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `REDIS_ADDR` | `localhost` | Redis host |
| `REDIS_PORT` | `6379` | Redis port |
| `REDIS_PASSWORD` | *(empty)* | Redis password |
| `REDIS_STREAM` | `alerts` | Redis stream to consume from |
| `REDIS_STREAMS` | *(empty)* | Comma-separated streams (overrides `REDIS_STREAM`) |
| `CONSUMER_GROUP` | `homerun2-notification-catcher` | Consumer group name |
| `CONSUMER_NAME` | hostname | Consumer name within the group |
| `LOG_FORMAT` | `json` | Log format: `json` or `text` |
| `LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, `error` |
| `NOTIFY_SEVERITY_MIN` | *(added in #7)* | Minimum severity to dispatch (e.g. `warning`) |
| `TEAMS_WEBHOOK_URL` | *(added in #6)* | Teams Power Automate webhook (SOPS secret) |

## Testing

```bash
# Unit tests (no Redis needed)
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
