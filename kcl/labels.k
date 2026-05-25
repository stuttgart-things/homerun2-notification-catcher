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
