# Deployment

## Layout

Manifests are generated from KCL in `kcl/`. One file per Kubernetes resource:

| File | Resource(s) |
|---|---|
| `schema.k` | `NotificationCatcher` schema with check rules |
| `labels.k` | Schema materialisation (reads `option("config.*")`) + common labels |
| `namespace.k` | Namespace |
| `serviceaccount.k` | ServiceAccount |
| `configmap.k` | `<name>-env` (envFrom'd). `<name>-notify` is caller-supplied. |
| `secret.k` | `<name>-redis` (Redis password) + `<name>-secrets` (output webhook tokens) |
| `deploy.k` | Deployment |
| `main.k` | Entry point — exports `manifests` |

The catcher is a pure consumer — **no Service, no Ingress, no HTTPRoute**.

## Rendering

```bash
# Render via Dagger (production pipeline, what CI uses)
task render-manifests-quick

# Render with kcl CLI directly (local dev)
cd kcl && kcl run -D config.image=… -D config.redisAddr=… …
```

The `tests/kcl-deploy-profile.yaml` file holds the values used by the Dagger render task and CI. It is the source of truth for the kustomize base that gets published as the release OCI artifact.

## Two-layer secret pattern

This is the load-bearing design choice. We want:

- Routing logic (the YAML) **inspectable** via `kubectl get cm` — no namespace-secret-read needed to audit which messages go where.
- Webhook URLs and similar **encrypted at rest** in etcd — not plaintext in a ConfigMap.

Both goals are satisfied by splitting `${VAR}` references across two manifests, with a deliberate escape:

### `<name>-notify` ConfigMap (caller-supplied)

Holds the routing YAML, mounted at `/etc/notification-catcher/config.yaml`. The Deployment in this chart always mounts a ConfigMap named `<name>-notify` with key `config.yaml`, but **the chart does not emit it** — the caller (cluster overlay) is responsible for creating it. This keeps routing rules in the cluster repo next to other cluster-scoped config (mirroring `homerun2-git-pitcher-watch-config.yaml`).

The right placeholder syntax for secret references depends on whether the Flux Kustomization that reconciles this file has `substituteFrom`:

- **With substituteFrom** (e.g. apply the ConfigMap through the chart's Flux release or a wrapper Kustomization that points at `homerun2-secrets`): use `$${VAR}`. Flux collapses `$$` → `$` at apply time, so the *applied* CM contains a literal `${VAR}`. The catcher resolves the single-`${VAR}` at startup against env.
- **Without substituteFrom** (most cluster overlays — e.g. `clusters/.../apps/`): use a single `${VAR}`. Flux does nothing, the CM lands with `${VAR}`, and the catcher resolves it at startup.

Either way the real webhook URL **never lands in the ConfigMap**; the catcher's env-interpolation (`internal/config/notify.go`) replaces `${VAR}` from the env supplied by the `<name>-secrets` Secret at startup.

Caller-side ConfigMap, no-substituteFrom (most common):

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: homerun2-notification-catcher-notify
  namespace: homerun2
data:
  config.yaml: |
    outputs:
      - name: teams-platform-alerts
        type: msteams
        webhook_url: "${TEAMS_WEBHOOK_URL}"
        filters:
          severity_min: warning
```

### `<name>-secrets` Secret

`stringData` map with one key per output secret. Values are single-dollar Flux substitution tokens — Flux rewrites them at apply time from the cluster-side `homerun2-secrets` Secret. The Deployment `envFrom`s this Secret, so each key becomes a container env var.

```yaml
stringData:
  TEAMS_WEBHOOK_URL: ${TEAMS_WEBHOOK_URL}     # Flux substitutes from homerun2-secrets
```

### Resulting pod state

```
$ kubectl get cm <name>-notify -o yaml
  data:
    config.yaml: |
      ...
      webhook_url: ${TEAMS_WEBHOOK_URL}       # placeholder still

$ kubectl get secret <name>-secrets -o jsonpath='{.data.TEAMS_WEBHOOK_URL}' | base64 -d
  https://teams.example/...                   # real URL

$ kubectl exec <pod> -- env | grep TEAMS
  TEAMS_WEBHOOK_URL=https://teams.example/... # via envFrom

# Catcher reads /etc/notification-catcher/config.yaml, sees ${TEAMS_WEBHOOK_URL},
# resolves it against os.Getenv → real URL on the wire.
```

## Flux integration

The cluster repo references this repo's kustomize OCI artifact via Flux. Add a component under `flux/apps/homerun2/components/notification-catcher/`:

```yaml
# kustomization.yaml
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: homerun2-notification-catcher
  namespace: homerun2-flux
spec:
  interval: 10m
  sourceRef:
    kind: OCIRepository
    name: homerun2-notification-catcher
  path: ./
  prune: true
  wait: true
  postBuild:
    substitute:
      # x-release-please-start-version
      # renovate: datasource=github-releases depName=stuttgart-things/homerun2-notification-catcher
      HOMERUN2_NOTIFICATION_CATCHER_VERSION: v1.1.0
      # x-release-please-end
    substituteFrom:
      - kind: Secret
        name: homerun2-secrets
---
apiVersion: source.toolkit.fluxcd.io/v1beta2
kind: OCIRepository
metadata:
  name: homerun2-notification-catcher
  namespace: homerun2-flux
spec:
  interval: 5m
  url: oci://ghcr.io/stuttgart-things/homerun2-notification-catcher-kustomize
  ref:
    tag: ${HOMERUN2_NOTIFICATION_CATCHER_VERSION}
```

The `homerun2-secrets` Secret in the cluster (SOPS-encrypted in the cluster repo) holds the real values for `TEAMS_WEBHOOK_URL`, `REDIS_PASSWORD`, etc. Flux's `substituteFrom` rewrites the placeholders in this repo's manifests at apply time.

### Scaling outputs

To add a new output type that needs a secret env var (e.g. PagerDuty):

1. Add the key to `config.outputSecrets` in `tests/kcl-deploy-profile.yaml`:

   ```yaml
   config.outputSecrets:
     TEAMS_WEBHOOK_URL: "${TEAMS_WEBHOOK_URL}"
     PAGERDUTY_TOKEN: "${PAGERDUTY_TOKEN}"
   ```

2. Reference it in the caller-supplied `<name>-notify` ConfigMap using `$${PAGERDUTY_TOKEN}`:

   ```yaml
   - name: pagerduty
     type: webhook
     url: https://events.pagerduty.com/v2/enqueue
     headers:
       Authorization: "Token token=$${PAGERDUTY_TOKEN}"
   ```

3. Add `PAGERDUTY_TOKEN` to the cluster's `homerun2-secrets` SOPS Secret.

Cut a release; Flux picks up the new OCI tag; the new output starts firing.

## Local deploy without Flux

Render manifests, edit the placeholders in the Secrets manually (or set them in the profile to the real values before render), and `kubectl apply -k`. Then hand-apply a `<name>-notify` ConfigMap with single `${…}` placeholders (since there is no Flux substituteFrom to collapse `$$` → `$`) — the catcher resolves single `${…}` directly against env at startup. See `notify.example.yaml` for a template.

## Namespace

Default `homerun2`. Override via `config.namespace` in the profile.

## Image pinning

Renovate's `gomod` manager bumps Go dependencies. The container image tag in this repo's `.ko.yaml` is set by semantic-release at tag time. The cluster-side `HOMERUN2_NOTIFICATION_CATCHER_VERSION` is bumped by Renovate's `github-releases` datasource against this repo's tags.
