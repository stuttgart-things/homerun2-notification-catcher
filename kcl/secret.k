"""
Secrets for homerun2-notification-catcher.

* <name>-redis    — Redis password (key: password). Mounted via
                    secretKeyRef on REDIS_PASSWORD. Skipped when
                    config.redisPassword is empty.

* <name>-secrets  — Webhook URLs and similar (env-var shape). envFrom'd
                    by the Deployment so each key becomes a container env
                    var (e.g. TEAMS_WEBHOOK_URL). Values are expected to be
                    Flux substitution tokens — Flux rewrites them at apply
                    time from the cluster's homerun2-secrets Secret.
                    Skipped when config.outputSecrets is empty.

Both use stringData so Flux substituteFrom can rewrite the placeholder text
(substitution does not penetrate base64).
"""

import labels

config = labels.config

secretRedis = {
    apiVersion: "v1"
    kind: "Secret"
    metadata: {
        name: "{}-redis".format(config.name)
        namespace: config.namespace
        labels: labels.commonLabels
    }
    type: "Opaque"
    stringData: {
        password: config.redisPassword
    }
} if config.redisPassword else None

secretOutputs = {
    apiVersion: "v1"
    kind: "Secret"
    metadata: {
        name: "{}-secrets".format(config.name)
        namespace: config.namespace
        labels: labels.commonLabels
    }
    type: "Opaque"
    stringData: config.outputSecrets
} if config.outputSecrets else None
