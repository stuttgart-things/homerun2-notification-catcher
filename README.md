# homerun2-notification-catcher

A Go consumer for the [homerun2](https://github.com/stuttgart-things) pipeline:
it reads `homerun.Message`s from a Redis Stream (via a consumer group) and
**dispatches them as notifications** to external channels — Microsoft Teams
and any generic JSON webhook today; Slack and email to follow.

```
Alertmanager ─▶ omni-pitcher /pitch/alertmanager ─▶ Redis stream "alerts"
                                                          │
                                               notification-catcher
                                                          │
                                            ┌─────────────┼─────────────┐
                                            ▶ MS Teams    ▶ Slack        ▶ email
```

Outbound counterpart to the existing `*-catcher` consumers: instead of logging
or storing messages, it sends them on. The Teams Adaptive Card is built in Go
in this repo — version-controlled and unit-testable — so no third-party proxy
or GUI-edited Power Automate flow is needed. Routing across outputs is driven
by a YAML config so the catcher works for *any* message stream, not just
Alertmanager.

## Docs

| | |
|---|---|
| [Configuration](docs/configuration.md) | Env vars, YAML routing config, smoke + dry-run |
| [Deployment](docs/deployment.md) | KCL manifests, Flux integration, two-layer secret pattern |
| [Alertmanager integration](docs/alertmanager.md) | Wire kube-prometheus-stack alerts through omni-pitcher into the catcher |
| [HTTP UI](docs/http-ui.md) | Optional: `/healthz` with a Redis ping, the loaded routing config (secrets redacted) |
| [Notification sink](docs/sink.md) | Stand-in receiver for the outputs: every notification in one list, Teams cards rendered, test API, card playground |
| [CI/CD](docs/cicd.md) | Workflows, image build, kustomize OCI release |
| [Preview environments](docs/preview-environments.md) | Per-PR namespaces |

## Try it out

### 1. Smoke locally — no Redis, no cluster

```bash
go build .
TEAMS_WEBHOOK_URL=https://teams.example/hook \
  ./homerun2-notification-catcher smoke \
    --config notify.example.yaml \
    --title "disk almost full" \
    --severity warning \
    --system kubernetes \
    --tags infra,storage \
    --dry-run
```

Each output prints one line: `OK`, `SKIPPED (filters did not match)`,
`DRY-RUN`, or `FAIL: <error>`. Drop `--dry-run` to actually POST to the
webhook.

### 2. Smoke on a cluster — fake Alertmanager payload through the live pipeline

When the catcher is deployed and consuming the `alerts` stream, fire a
synthetic Alertmanager v4 payload at `omni-pitcher` so the whole chain
runs end-to-end:

```bash
kubectl -n homerun2 exec deploy/homerun2-omni-pitcher -- \
  wget -qO- --post-data='{
    "version":"4","status":"firing","receiver":"msteams",
    "alerts":[{
      "status":"firing",
      "labels":{"alertname":"DryRunSmoke","severity":"warning","namespace":"platform"},
      "annotations":{"summary":"end-to-end pipeline smoke"},
      "startsAt":"'"$(date -u +%FT%TZ)"'",
      "generatorURL":"https://grafana.example/d/abc"
    }]
  }' \
    --header="Authorization: Bearer ${OMNI_PITCHER_AUTH_TOKEN}" \
    --header="Content-Type: application/json" \
    http://localhost:8080/pitch/alertmanager
```

Then tail the catcher:

```bash
kubectl -n homerun2 logs deploy/homerun2-notification-catcher --tail=20 | grep -E "dry-run|notify"
```

With `DRY_RUN=true` (the recommended first-reconciliation mode) you'll see:

```
INFO dry-run: would send  output=teams-platform-alerts  title=DryRunSmoke  severity=warning  system=msteams
```

— filter evaluation runs, the dispatcher confirms what *would* fire, but no
webhook is invoked. Once you've verified routing in `kubectl logs`, remove the
`DRY_RUN` patch from the Flux component and reconcile again to flip to live.
Live, each delivery logs `notifier sent` with the output's name, and each
failure logs `notifier failed` with the error:

```bash
kubectl -n homerun2 logs deploy/homerun2-notification-catcher --tail=50 | grep -E "notifier (sent|failed)"
```

```
INFO notifier sent  output=teams-homerun2-dev  objectId=1761000000001-0  title="Test-Warning Matrix"  severity=warning  system=portal-test
```

After the fan-out one line per message sums it up: `dispatched` with each
output's result (`sent`, `failed`, `filtered`, `dry-run`), or `no output
matched`. Query by `objectId` to follow one message:

```bash
kubectl -n homerun2 logs deploy/homerun2-notification-catcher | grep 1761000000001-0
```

With the [HTTP UI](docs/http-ui.md) on, the same results are on its Events
page and in `notification_catcher_dispatch_total{output,result}`.

### 3. Trip a real Prometheus alert

The full Phase-1 chain: lower a Prometheus rule threshold so Alertmanager
fires on the next eval, and watch the same `kubectl logs` line appear within
seconds — `DRY_RUN=true` still suppresses the actual POST.

## Status

Phase 1 (#5–#9) shipped. Multi-sink routing (msteams + generic webhook),
YAML-driven filters, env-interp for secrets, dry-run mode, KCL manifests,
Flux component, Alertmanager wiring all in place.

Phase 2 (#3) is under way: retries with backoff and a dead-letter stream
(#68) with `replay` and a Dead letter tab (#70, see
[Configuration](docs/configuration.md#retries-and-dead-letter)), firing vs.
resolved cards (#71), fallback outputs (#74), a per-output throttle (#79, see
[Throttle](docs/configuration.md#throttle-79)) and omni-pitcher's
`/pitch/alertmanager` are in. Phase 3 (#4 — Slack / email / per-assignee routing) layers on top.

## Built from

`homerun2-core-catcher` is the scaffold reference — consumer group, ko build,
KCL manifests, Dagger CI, semantic-release, and PR preview environments.
