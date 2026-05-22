# homerun2-notification-catcher

A Go consumer for the [homerun2](https://github.com/stuttgart-things) pipeline:
it reads `homerun.Message`s from a Redis Stream (via a consumer group) and
**dispatches them as notifications** to external channels — Microsoft Teams
first, with Slack / email / generic webhook to follow.

```
Alertmanager ─▶ omni-pitcher /pitch/grafana ─▶ Redis stream "alerts"
                                                     │
                                          notification-catcher
                                                     │
                                       ┌─────────────┼─────────────┐
                                       ▶ MS Teams    ▶ Slack        ▶ email
```

It is the outbound counterpart to the existing `*-catcher` consumers: instead
of logging or storing messages, it sends them on. The Teams Adaptive Card is
built in Go in this repo — version-controlled and unit-testable — so no
third-party proxy or GUI-edited Power Automate flow is needed.

## Status

🚧 **Scaffolding stage.** See the **[design issue (#1)](../../issues/1)** for the
architecture and the phased plan.

## Built from

`homerun2-core-catcher` is the scaffold reference — consumer group, ko build,
KCL manifests, Dagger CI, semantic-release, and PR preview environments.
