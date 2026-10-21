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
    redisStream: str = "messages"
    redisPassword: str = ""
    consumerGroup: str = "homerun2-notification-catcher"

    # Logging
    logFormat: str = "json"
    logLevel: str = "info"

    # Routing config YAML. Default empty — the caller (cluster overlay)
    # supplies a ConfigMap named <name>-notify with key `config.yaml`, which
    # the Deployment always mounts at /etc/notification-catcher/config.yaml.
    # See docs/deployment.md.
    #
    # Setting this to a non-empty string also emits the ConfigMap inline
    # from the chart, intended for self-contained dev/test renders only —
    # in production it collides with the caller-supplied CM.
    notifyConfig: str = ""

    # Output secrets — emitted into a Secret <name>-secrets and envFrom'd by
    # the Deployment. Values are expected to be Flux substitution tokens
    # (${TEAMS_WEBHOOK_URL}, ${PAGERDUTY_TOKEN}, …) which Flux rewrites at
    # apply time from the cluster's homerun2-secrets Secret.
    outputSecrets: {str:str} = {}

    # Extra env vars folded into the env ConfigMap (envFrom)
    extraEnvVars: {str:str} = {}

    # Notification sink (`notification-catcher sink`, #52) — a stand-in
    # receiver for the outputs, deployed next to the catcher from the same
    # image. Off by default so the published kustomize base is unchanged.
    # Always one replica: posts live in an in-memory store per pod.
    sinkEnabled: bool = False
    sinkCapacity: int = 500
    # Path prefix the sink is served under (BASE_PATH), e.g. "/notify".
    # Also the HTTPRoute's PathPrefix match. Empty serves at "/".
    sinkBasePath: str = ""
    # SINK_TOKEN — emitted into a Secret <name>-sink. Expected to be a Flux
    # substitution token (${SINK_TOKEN}). Empty leaves the sink open.
    sinkToken: str = ""
    sinkServiceType: str = "ClusterIP"
    sinkServicePort: int = 80
    sinkContainerPort: int = 8080

    # HTTPRoute (Gateway API) for the sink
    sinkHttpRouteEnabled: bool = False
    sinkHttpRouteParentRefName: str = ""
    sinkHttpRouteParentRefNamespace: str = ""
    sinkHttpRouteHostname: str = ""
    sinkHttpRouteAnnotations: {str:str} = {}

    # Labels and annotations
    labels: {str:str} = {}
    annotations: {str:str} = {}

    check:
        replicas >= 0, "replicas must be >= 0"
        sinkCapacity >= 1, "sinkCapacity must be >= 1"
        sinkServicePort >= 1 and sinkServicePort <= 65535, "sinkServicePort must be valid"
        sinkContainerPort >= 1 and sinkContainerPort <= 65535, "sinkContainerPort must be valid"
        not sinkBasePath or (sinkBasePath.startswith("/") and not sinkBasePath.endswith("/")), "sinkBasePath must start with / and not end with /"
        not sinkHttpRouteEnabled or sinkEnabled, "sinkHttpRouteEnabled requires sinkEnabled"
        not sinkHttpRouteEnabled or sinkHttpRouteParentRefName != "", "sinkHttpRouteParentRefName must be set when sinkHttpRouteEnabled"
        name != "", "name must not be empty"
        namespace != "", "namespace must not be empty"
