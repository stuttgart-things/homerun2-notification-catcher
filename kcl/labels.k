"""
Common labels for homerun2-notification-catcher resources.

Materialises the NotificationCatcher schema from command-line `-D config.*`
overrides so deployment profiles can be expressed as flat key/value pairs.
"""

import schema

# Command-line overrides (kcl -D config.image=… or via profile yaml)
_image = option("config.image")
_namespace = option("config.namespace")
_replicas = option("config.replicas")
_imagePullPolicy = option("config.imagePullPolicy")

_redisAddr = option("config.redisAddr")
_redisPort = option("config.redisPort")
_redisStream = option("config.redisStream")
_redisPassword = option("config.redisPassword")
_consumerGroup = option("config.consumerGroup")

_logFormat = option("config.logFormat")
_logLevel = option("config.logLevel")

_notifyConfig = option("config.notifyConfig")
_outputSecrets = option("config.outputSecrets") or {}
_extraEnvVars = option("config.extraEnvVars") or {}

_catcherEnabled = option("config.catcherEnabled")
_sinkEnabled = option("config.sinkEnabled")
_sinkCapacity = option("config.sinkCapacity")
_sinkBasePath = option("config.sinkBasePath")
_sinkToken = option("config.sinkToken")
_sinkServiceType = option("config.sinkServiceType")
_sinkServicePort = option("config.sinkServicePort")
_sinkContainerPort = option("config.sinkContainerPort")
_sinkHttpRouteEnabled = option("config.sinkHttpRouteEnabled")
_sinkHttpRouteParentRefName = option("config.sinkHttpRouteParentRefName")
_sinkHttpRouteParentRefNamespace = option("config.sinkHttpRouteParentRefNamespace")
_sinkHttpRouteHostname = option("config.sinkHttpRouteHostname")
_sinkHttpRouteAnnotations = option("config.sinkHttpRouteAnnotations") or {}

_labels = option("config.labels") or {}
_annotations = option("config.annotations") or {}

config: schema.NotificationCatcher = schema.NotificationCatcher {
    if _image:
        image = _image
    if _namespace:
        namespace = _namespace
    if _replicas:
        replicas = int(_replicas)
    if _imagePullPolicy:
        imagePullPolicy = _imagePullPolicy

    if _redisAddr:
        redisAddr = _redisAddr
    if _redisPort:
        redisPort = _redisPort
    if _redisStream:
        redisStream = _redisStream
    if _redisPassword:
        redisPassword = _redisPassword
    if _consumerGroup:
        consumerGroup = _consumerGroup

    if _logFormat:
        logFormat = _logFormat
    if _logLevel:
        logLevel = _logLevel

    if _notifyConfig:
        notifyConfig = _notifyConfig
    if _outputSecrets:
        outputSecrets = _outputSecrets
    if _extraEnvVars:
        extraEnvVars = _extraEnvVars
    if _catcherEnabled != None:
        catcherEnabled = _catcherEnabled in [True, "True", "true"]
    if _sinkEnabled:
        sinkEnabled = _sinkEnabled in [True, "True", "true"]
    if _sinkCapacity:
        sinkCapacity = int(_sinkCapacity)
    if _sinkBasePath:
        sinkBasePath = _sinkBasePath
    if _sinkToken:
        sinkToken = _sinkToken
    if _sinkServiceType:
        sinkServiceType = _sinkServiceType
    if _sinkServicePort:
        sinkServicePort = int(_sinkServicePort)
    if _sinkContainerPort:
        sinkContainerPort = int(_sinkContainerPort)
    if _sinkHttpRouteEnabled:
        sinkHttpRouteEnabled = _sinkHttpRouteEnabled in [True, "True", "true"]
    if _sinkHttpRouteParentRefName:
        sinkHttpRouteParentRefName = _sinkHttpRouteParentRefName
    if _sinkHttpRouteParentRefNamespace:
        sinkHttpRouteParentRefNamespace = _sinkHttpRouteParentRefNamespace
    if _sinkHttpRouteHostname:
        sinkHttpRouteHostname = _sinkHttpRouteHostname
    if _sinkHttpRouteAnnotations:
        sinkHttpRouteAnnotations = _sinkHttpRouteAnnotations

    if _labels:
        labels = _labels
    if _annotations:
        annotations = _annotations
}

# Common labels applied to all resources
commonLabels = {
    "app.kubernetes.io/name": config.name
    "app.kubernetes.io/instance": config.name
    "app.kubernetes.io/component": "notifier"
    "app.kubernetes.io/part-of": "homerun2"
    **config.labels
}

# Selector labels for pods
selectorLabels = {
    "app.kubernetes.io/name": config.name
    "app.kubernetes.io/instance": config.name
}

# Sink resources (#52) get their own name so the catcher Deployment's
# selector never matches sink pods, and vice versa.
sinkName = "{}-sink".format(config.name)

sinkLabels = {
    "app.kubernetes.io/name": sinkName
    "app.kubernetes.io/instance": sinkName
    "app.kubernetes.io/component": "sink"
    "app.kubernetes.io/part-of": "homerun2"
    **config.labels
}

sinkSelectorLabels = {
    "app.kubernetes.io/name": sinkName
    "app.kubernetes.io/instance": sinkName
}
