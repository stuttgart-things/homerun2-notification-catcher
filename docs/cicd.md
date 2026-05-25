# CI/CD

GitHub Actions in `.github/workflows/`:

| Workflow | When | What |
|---|---|---|
| `build-test.yaml` | every PR + push to main | Dagger lint + `go test ./...` with Redis service |
| `build-scan-image.yaml` | every PR + push to main | ko build, Trivy scan, push image to ghcr |
| `lint-repo.yaml` | every PR | Repository linting (yamllint, markdownlint, …) |
| `push-kustomize-pr.yaml` | PRs touching `kcl/**` or `tests/kcl-*.yaml` | Render kustomize base, push as preview OCI tag |
| `comment-preview-url.yaml` | PR opened/reopened | Comment the preview namespace + URL |
| `cleanup-pr-artifacts.yaml` | PR closed | Delete the preview OCI tag and namespace |
| `release.yaml` | merge to main | Semantic-release: tag, changelog, ko image, kustomize OCI |
| `pages.yaml` | merge to main | mkdocs deploy |

## Release pipeline

1. PR merged to `main`.
2. `release.yaml` runs `call-go-release.yaml` from `stuttgart-things/github-workflow-templates`. Semantic-release reads commit messages, computes the next version, tags, generates the changelog.
3. ko builds and pushes a container image to `ghcr.io/stuttgart-things/homerun2-notification-catcher:<version>`.
4. Dagger renders the kustomize base from `kcl/` against `tests/kcl-deploy-profile.yaml` and pushes it to `ghcr.io/stuttgart-things/homerun2-notification-catcher-kustomize:<version>`.
5. Renovate bumps `HOMERUN2_NOTIFICATION_CATCHER_VERSION` in the cluster repo's Flux component on the next scan.
6. Flux reconciles, pulls the new OCI tag, applies the manifests with `substituteFrom` from the cluster's SOPS-encrypted `homerun2-secrets`.

## Local sanity

```bash
# Unit tests (no Redis, no network)
go test ./...

# Integration test (Dagger spins up Redis)
task build-test-binary

# Lint
task lint

# Render manifests
task render-manifests-quick

# Build + scan image with a throw-away ttl.sh tag
task build-scan-image-ko
```
