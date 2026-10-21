"""
Secrets for homerun2-notification-catcher.

* <name>-redis    — Redis password (key: password). Mounted via
                    secretKeyRef on REDIS_PASSWORD. Skipped when
                    config.redisPassword is empty. Uses `data` with
                    base64.encode so a Flux release.yaml patch on
                    `data.password: ${HOMERUN2_REDIS_PASSWORD_B64}` can
                    strategic-merge cleanly. (stringData would silently
                    win over data on apply and defeat the patch.)

* <name>-secrets  — Webhook URLs and similar (env-var shape). envFrom'd
                    by the Deployment so each key becomes a container env
                    var (e.g. TEAMS_WEBHOOK_URL). Values are expected to be
                    Flux substitution tokens — Flux rewrites them at apply
                    time from the cluster's homerun2-secrets Secret.
                    Substitution acts on the plaintext placeholder, so
                    stringData is the right field here. Skipped when
                    config.outputSecrets is empty. With config.httpEnabled
                    and config.httpToken it also carries AUTH_TOKEN for the
                    catcher's UI (#22).

* <name>-sink     — SINK_TOKEN for the optional sink (key: token), read
                    via secretKeyRef. Like <name>-secrets the value is a
                    Flux substitution token, so stringData. Skipped unless
                    config.sinkEnabled and config.sinkToken are set.
"""

import base64
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
    data: {
        password: base64.encode(config.redisPassword)
    }
} if config.catcherEnabled and config.redisPassword else None

# Output secrets, plus the UI token when the HTTP surface has one.
_outputSecretData = config.outputSecrets | ({AUTH_TOKEN: config.httpToken} if config.httpEnabled and config.httpToken else {})

secretOutputs = {
    apiVersion: "v1"
    kind: "Secret"
    metadata: {
        name: "{}-secrets".format(config.name)
        namespace: config.namespace
        labels: labels.commonLabels
    }
    type: "Opaque"
    stringData: _outputSecretData
} if config.catcherEnabled and _outputSecretData else None

secretSink = {
    apiVersion: "v1"
    kind: "Secret"
    metadata: {
        name: labels.sinkName
        namespace: config.namespace
        labels: labels.sinkLabels
    }
    type: "Opaque"
    stringData: {
        token: config.sinkToken
    }
} if config.sinkEnabled and config.sinkToken else None
