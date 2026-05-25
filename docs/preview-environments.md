# Preview environments

Every PR gets a per-namespace preview deployment driven by the kustomize OCI tag pushed by `push-kustomize-pr.yaml`.

## What's in a preview namespace

| Resource | Source |
|---|---|
| `homerun2-notification-catcher` | this PR's image + kustomize tag |
| `redis-stack` | co-tenanted from the homerun2-install chart |
| `homerun2-omni-pitcher` | co-tenanted producer (latest release) |
| seed `Job` | posts a fixture to `/pitch` so a Teams notification fires when you load the preview |

## Lifecycle

| Event | Effect |
|---|---|
| PR opened/reopened | `comment-preview-url.yaml` posts a sticky comment with the preview URL and namespace |
| `kcl/**` or `tests/kcl-*.yaml` changes | `push-kustomize-pr.yaml` re-renders and pushes the preview OCI tag |
| PR closed | `cleanup-pr-artifacts.yaml` deletes the preview OCI tag and the per-PR namespace |

## Naming

- Namespace: `homerun2-notification-catcher-pr-<number>`
- Preview URL: `https://cc-pr-<number>.homerun2-dev.sthings-vsphere.labul.sva.de`
- Image tag: `pr-<number>-<sha>`
- Kustomize OCI tag: `pr-<number>-<sha>`

## Caveats

- `push-kustomize-pr.yaml` only runs when files under `kcl/**` or `tests/kcl-*.yaml` change — Go-only PRs don't re-publish the kustomize base. The preview deploy on the cluster pulls the latest matching tag.
- Webhook secrets in the preview namespace come from the same cluster-side `homerun2-flux-secrets` as production — be careful with smoke fixtures that would page real on-call.
