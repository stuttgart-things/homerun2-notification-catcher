# Alertmanager integration

End-to-end wiring of cluster alerts into the catcher:

```
Alertmanager ─ webhook ─▶ omni-pitcher  ─▶ Redis stream "alerts" ─▶ notification-catcher ─▶ Teams
              (POST          /pitch/alertmanager  (consumer-group)              (Adaptive Card)
              JSON v4)
```

omni-pitcher's `/pitch/alertmanager` endpoint (omni-pitcher ≥ v2.4.0, homerun2-omni-pitcher#210) takes Alertmanager's webhook v4 payload and pitches one message per alert (`?group=true`: one per notification group). Each message carries `status=firing|resolved` and `fingerprint=<fp>` as tags. Older setups point the receiver at `/pitch/grafana`, which accepts the same payload with a coarser mapping (see [the end of this page](#older-omni-pitcher-pitchgrafana)).

## Pieces

The catcher itself needs no code changes for #9 — it already consumes the `alerts` stream (default `REDIS_STREAM`). The wiring is purely configuration in **two other repos**:

| Config | Lives in |
|---|---|
| `ROUTES_CONFIG` for omni-pitcher (route `/pitch/alertmanager` → `alerts`) | flux repo, as the env value applied to the omni-pitcher Deployment |
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
  # Alertmanager webhooks land on this endpoint —
  # send everything to the dedicated `alerts` stream.
  - match:
      endpoint: /pitch/alertmanager
    stream: alerts
```

Order matters — earlier rules win. Put the `/pitch/alertmanager` → `alerts` rule before any catch-all. A rule on `/pitch/grafana` does **not** match the new endpoint; when moving the receiver over, add the rule (keep the Grafana one if Grafana still pitches there).

## Alertmanager receiver

Replace the existing `msteamsv2` receiver in the kube-prometheus-stack values:

```yaml
alertmanager:
  config:
    receivers:
      - name: msteams
        webhook_configs:
          - url: http://homerun2-omni-pitcher.homerun2.svc.cluster.local:8080/pitch/alertmanager
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

What omni-pitcher's `/pitch/alertmanager` does with each alert:

| `homerun.Message` field | Source in Alertmanager payload | Notes |
|---|---|---|
| `Title` | `alerts[i].labels.alertname` | then `commonLabels.alertname` |
| `Message` | `alerts[i].annotations.summary` | then `description`, then `commonAnnotations`, then `"Alert X is Y"`; `Resolved: ` prefix when resolved |
| `Severity` | `alerts[i].labels.severity` for `firing`; `success` for `resolved` | `critical`/`page` → `critical`, `warning`/`warn` → `warning`, otherwise pass-through |
| `Author` | constant `"alertmanager"` | |
| `Timestamp` | `startsAt`; `endsAt` when resolved | falls back to `time.Now()` |
| `System` | top-level `receiver` | e.g. `msteams` |
| `Tags` | `status=<firing\|resolved>,fingerprint=<fp>`, then the other labels sorted, `k=v,k=v` | a comma in a label value becomes `;` |
| `Url` | `alerts[i].generatorURL` | then `externalURL` |

With `?group=true` on the receiver URL, one message per notification: the common labels and annotations, the highest severity of the firing alerts, tags `status=…,alerts=<n>,…` and `(N alerts)` after the message.

The values feed `internal/notify/filter.go` so the catcher's YAML routing can target Alertmanager alerts by `severity_min`, `match.author: alertmanager`, `match.system: msteams`, or `tags_contain: [namespace=prod, …]`.

**Resolutions and `severity_min`:** a resolved alert has severity `success`, below `warning`, so an output with `severity_min: warning` drops it. To get resolutions into a channel, give it a second output without `severity_min` that matches them, e.g. `tags_contain: [status=resolved]` plus `match.author: alertmanager` (pairing firing and resolved cards is #71).

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
    http://localhost:8080/pitch/alertmanager

# 2. Confirm the catcher picked it up
kubectl -n homerun2 logs deploy/homerun2-notification-catcher --tail=20 | grep SmokeTest

# 3. Check the Teams channel — a warning-styled Adaptive Card should appear
```

For a fuller test, fire a real Prometheus alert (temporarily lower a threshold rule) and watch the same chain.

## Older omni-pitcher: `/pitch/grafana`

Before the dedicated endpoint, the receiver pointed at `/pitch/grafana` (with the routing rule on `endpoint: /pitch/grafana`), which accepts the same v4 payload — the schemas are field-compatible. It still works; the differences to `/pitch/alertmanager`:

- `Author` is `"grafana"`, the title falls back to `"Grafana Alert"`
- no `status=` / `fingerprint=` tags, labels unsorted, so a consumer cannot tell a resolution by its tags or pair it with its alert
- `Timestamp` is always `startsAt`, also for a resolution
- no `?group=true`

Severity (`success` when resolved) and the `Resolved: ` prefix are the same.
