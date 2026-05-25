"""
ConfigMaps for homerun2-notification-catcher.

* <name>-env  — envFrom'd by the Deployment. Carries Redis connection,
                logging, consumer group, the CONFIG_PATH pointer, plus any
                extra env vars folded in via config.extraEnvVars.

* <name>-notify — caller-supplied. The Deployment always mounts a ConfigMap
                  with this name at /etc/notification-catcher/config.yaml,
                  but the chart only emits one when config.notifyConfig is
                  set (for self-contained dev/test renders). In production
                  the cluster overlay provides the CM — see
                  docs/deployment.md.
"""

import labels

config = labels.config

# Base env-var map. Keys go straight into envFrom and become the container's env.
_envData = {
    LOG_FORMAT: config.logFormat
    LOG_LEVEL: config.logLevel
    REDIS_ADDR: config.redisAddr
    REDIS_PORT: str(config.redisPort)
    REDIS_STREAM: config.redisStream
    CONSUMER_GROUP: config.consumerGroup
    CONFIG_PATH: "/etc/notification-catcher/config.yaml"
}

configMapEnv = {
    apiVersion: "v1"
    kind: "ConfigMap"
    metadata: {
        name: "{}-env".format(config.name)
        namespace: config.namespace
        labels: labels.commonLabels
    }
    data: _envData | config.extraEnvVars
}

configMapNotify = {
    apiVersion: "v1"
    kind: "ConfigMap"
    metadata: {
        name: "{}-notify".format(config.name)
        namespace: config.namespace
        labels: labels.commonLabels
    }
    data: {
        "config.yaml": config.notifyConfig
    }
} if config.notifyConfig else None
