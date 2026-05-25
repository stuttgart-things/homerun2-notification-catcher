"""
Schema definition for homerun2-notification-catcher deployment configuration.
"""

schema NotificationCatcher:
    """Configuration for homerun2-notification-catcher Kubernetes deployment."""

    # Metadata
    name: str = "homerun2-notification-catcher"
    namespace: str = "homerun2"

    # Image
    image: str = "ghcr.io/stuttgart-things/homerun2-notification-catcher:latest"
    imagePullPolicy: str = "IfNotPresent"

    # Replicas + resources — a consumer that fans out webhooks is light
    replicas: int = 1
    cpuRequest: str = "50m"
    cpuLimit: str = "200m"
    memoryRequest: str = "64Mi"
    memoryLimit: str = "256Mi"

    # Redis (consumer-group source)
    redisAddr: str = "redis-stack.homerun2.svc.cluster.local"
    redisPort: int | str = "6379"
    redisStream: str = "alerts"
    redisPassword: str = ""
    consumerGroup: str = "homerun2-notification-catcher"

    # Logging
    logFormat: str = "json"
    logLevel: str = "info"

    # Routing config YAML — full content of /etc/notification-catcher/config.yaml.
    # Use $${VAR} (double-dollar) for any value that should be interpolated by
    # the catcher from env vars at runtime — Flux's substituteFrom collapses
    # $${VAR} → ${VAR} in the applied manifest, then the catcher resolves
    # ${VAR} against its environment. See README + docs/deployment.md.
    notifyConfig: str = ""

    # Output secrets — emitted into a Secret <name>-secrets and envFrom'd by
    # the Deployment. Values are expected to be Flux substitution tokens
    # (${TEAMS_WEBHOOK_URL}, ${PAGERDUTY_TOKEN}, …) which Flux rewrites at
    # apply time from the cluster's homerun2-secrets Secret.
    outputSecrets: {str:str} = {}

    # Extra env vars folded into the env ConfigMap (envFrom)
    extraEnvVars: {str:str} = {}

    # Labels and annotations
    labels: {str:str} = {}
    annotations: {str:str} = {}

    check:
        replicas >= 0, "replicas must be >= 0"
        name != "", "name must not be empty"
        namespace != "", "namespace must not be empty"
