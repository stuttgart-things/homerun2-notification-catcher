# Alertmanager integration

End-to-end wiring of cluster alerts into the catcher:

```
Alertmanager ─ webhook ─▶ omni-pitcher  ─▶ Redis stream "alerts" ─▶ notification-catcher ─▶ Teams
              (POST          /pitch/grafana       (consumer-group)              (Adaptive Card)
              JSON v4)
```

omni-pitcher's `/pitch/grafana` endpoint accepts Alertmanager's webhook v4 payload directly — the schemas are field-compatible (`alerts[].{labels,annotations,startsAt,endsAt,generatorURL,status,fingerprint}` plus `receiver`, `commonLabels`, `commonAnnotations`). A dedicated `/pitch/alertmanager` endpoint with a cleaner mapping is Phase 2 (#3).

## Pieces

The catcher itself needs no code changes for #9 — it already consumes the `alerts` stream (default `REDIS_STREAM`). The wiring is purely configuration in **two other repos**:

| Config | Lives in |
|---|---|
| `ROUTES_CONFIG` for omni-pitcher (route `/pitch/grafana` → `alerts`) | flux repo, as the env value applied to the omni-pitcher Deployment |
| `kube-prometheus-stack` Alertmanager receiver `msteams` | flux repo, as Helm values for the kube-prometheus-stack `HelmRelease` |

## omni-pitcher routing

Add (or extend) the `ROUTES_CONFIG` value that omni-pitcher reads at startup:

```yaml
# Allowlist — every stream named below in `routes:` or `default_stream`
# must appear here, otherwise omni-pitcher fails startup.
streams:
  - messages
  - alerts

default_stream: messages

routes:
  # Alertmanager (and Grafana) webhooks land on this endpoint —
  # send everything to the dedicated `alerts` stream.
  - match:
      endpoint: /pitch/grafana
    stream: alerts
```

Order matters — earlier rules win. Put the `/pitch/grafana` → `alerts` rule before any catch-all.

## Alertmanager receiver

Replace the existing `msteamsv2` receiver in the kube-prometheus-stack values:

```yaml
alertmanager:
  config:
    receivers:
      - name: msteams
        webhook_configs:
          - url: http://homerun2-omni-pitcher.homerun2.svc.cluster.local:8080/pitch/grafana
            send_resolved: true
            http_config:
              authorization:
                type: Bearer
                # SOPS-encrypted in the flux repo; populated via Helm values
                # or substituteFrom from homerun2-secrets.
                credentials: ${OMNI_PITCHER_TOKEN}

    route:
      # … existing top-level routing tree …
      receiver: msteams
```

If the pitcher's bearer token check is disabled (`AUTH_TOKEN` unset on omni-pitcher), drop the `http_config.authorization` block.

## Field mapping

What omni-pitcher's `/pitch/grafana` does with an Alertmanager payload:

| `homerun.Message` field | Source in Alertmanager payload | Notes |
|---|---|---|
| `Title` | `alerts[i].labels.alertname` | falls back to literal `"Grafana Alert"` |
| `Message` | `alerts[i].annotations.summary` | falls back to `annotations.description`, then `"Alert X is Y"` |
| `Severity` | `alerts[i].labels.severity` for `firing`; `info` for `resolved` | mapped via `mapGrafanaSeverity` — `critical`/`page` → `critical`, `warning`/`warn` → `warning`, otherwise pass-through |
| `Author` | constant `"grafana"` | cosmetic — Phase 2 `/pitch/alertmanager` endpoint will set `"alertmanager"` |
| `Timestamp` | `alerts[i].startsAt` | falls back to `time.Now()` if missing |
| `System` | top-level `receiver` | e.g. `msteams` |
| `Tags` | every `alerts[i].labels` key except `alertname` and `severity`, joined `k=v,k=v` | |
| `Url` | `alerts[i].generatorURL` | (`dashboardURL` / `panelURL` are Grafana-only — empty for Alertmanager) |

The values feed `internal/notify/filter.go` so the catcher's YAML routing can target Alertmanager alerts by `severity_min`, `match.system: msteams`, or `tags_contain: [namespace=prod, …]`.

## Notification-catcher config

The catcher's `notify.yaml` from `tests/kcl-deploy-profile.yaml` already handles this:

```yaml
outputs:
  - name: teams-platform-alerts
    type: msteams
    webhook_url: $${TEAMS_WEBHOOK_URL}
    filters:
      severity_min: warning
```

Every Alertmanager alert with severity ≥ warning fires the Teams card. To narrow further:

```yaml
filters:
  severity_min: warning
  match:
    system: msteams            # Alertmanager receiver name
  tags_contain:
    - namespace=platform       # only alerts from labels.namespace=platform
```

## Smoke test (post-deploy)

> **Tip — first reconciliation:** set `DRY_RUN=true` on the catcher's
> Deployment before flipping the Alertmanager receiver. The catcher still
> consumes the stream and runs filter evaluation but logs `dry-run: would
> send  output=… title=… severity=…` instead of POSTing to Teams. Verify
> routing in `kubectl logs`, then remove the env var (or set it to `false`)
> and reconcile again.

Verify the chain end-to-end:

```bash
# 1. Pitch a synthetic Alertmanager payload directly at omni-pitcher
kubectl -n homerun2 exec deploy/homerun2-omni-pitcher -- \
  wget -qO- --post-data='{
    "version": "4",
    "status": "firing",
    "receiver": "msteams",
    "alerts": [{
      "status": "firing",
      "labels": {
        "alertname": "SmokeTest",
        "severity": "warning",
        "namespace": "platform"
      },
      "annotations": {
        "summary": "End-to-end pipeline smoke from amtool"
      },
      "startsAt": "'"$(date -Iseconds -u)"'",
      "generatorURL": "https://grafana.example/d/abc"
    }]
  }' \
    --header="Authorization: Bearer ${OMNI_PITCHER_TOKEN}" \
    --header="Content-Type: application/json" \
    http://localhost:8080/pitch/grafana

# 2. Confirm the catcher picked it up
kubectl -n homerun2 logs deploy/homerun2-notification-catcher --tail=20 | grep SmokeTest

# 3. Check the Teams channel — a warning-styled Adaptive Card should appear
```

For a fuller test, fire a real Prometheus alert (temporarily lower a threshold rule) and watch the same chain.

## Why not a dedicated `/pitch/alertmanager`?

Phase 2 (#3) introduces one. Until then, `/pitch/grafana` is good enough — the only payload differences are:

- `Author` is hardcoded to `"grafana"` (cosmetic)
- `payload.Title` / `payload.Message` aren't set by Alertmanager, but the handler already falls back to per-alert label/annotation lookups

So no functional gap, just naming. Bringing Phase 2 forward would buy `Author: "alertmanager"` and clearer log lines.
