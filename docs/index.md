# homerun2-notification-catcher

A Go consumer for the [homerun2](https://github.com/stuttgart-things) pipeline. Reads `homerun.Message` records from a Redis Stream (via a consumer group) and **dispatches them as notifications** to external channels — Microsoft Teams and any generic JSON webhook, with Slack / email to follow.

Outbound counterpart to the existing `*-catcher` consumers: instead of logging or storing messages, it sends them on. Routing is driven by a YAML config so the catcher works the same way for every kind of message stream, not just Alertmanager.

## Pipeline

```
producer (omni-pitcher, …) ─▶ Redis stream "alerts"
                                   │
                                   ▼
                      notification-catcher
                      (consumer group)
                                   │
                       ┌───────────┼───────────┐
                       ▶ MS Teams   ▶ Generic   (future:
                         (Adaptive    webhook    Slack,
                          Card)                  email)
```

Adaptive Card formatting and routing live in version-controlled Go code in this repo — no third-party proxy, no GUI-edited Power Automate flow.

## Reading order

1. **[Configuration](configuration.md)** — env vars and the YAML routing config.
2. **[Deployment](deployment.md)** — KCL manifests, Flux integration, SOPS-backed webhook secrets.
3. **[Alertmanager integration](alertmanager.md)** — wire kube-prometheus-stack alerts through omni-pitcher into the catcher.
4. **[CI/CD](cicd.md)** — workflows, image build/scan, kustomize OCI release pipeline.
5. **[Preview environments](preview-environments.md)** — per-PR namespaces.
