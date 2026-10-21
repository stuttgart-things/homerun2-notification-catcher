"""
Services for homerun2-notification-catcher.

<name>-sink only with config.sinkEnabled; <name> (the catcher's UI, #22)
only with config.httpEnabled -- otherwise the catcher has no Service. Outputs reach the sink in-cluster at
http://<name>-sink.<namespace>.svc[:sinkServicePort]<sinkBasePath>/teams/<channel>.
"""

import labels

config = labels.config

sinkService = {
    apiVersion: "v1"
    kind: "Service"
    metadata: {
        name: labels.sinkName
        namespace: config.namespace
        labels: labels.sinkLabels
    }
    spec: {
        type: config.sinkServiceType
        ports: [
            {
                name: "http"
                port: config.sinkServicePort
                targetPort: "http"
                protocol: "TCP"
            }
        ]
        selector: labels.sinkSelectorLabels
    }
} if config.sinkEnabled else None

# The catcher's own UI (#22), with config.httpEnabled.
service = {
    apiVersion: "v1"
    kind: "Service"
    metadata: {
        name: config.name
        namespace: config.namespace
        labels: labels.commonLabels
    }
    spec: {
        type: "ClusterIP"
        ports: [
            {
                name: "http"
                port: config.httpServicePort
                targetPort: "http"
                protocol: "TCP"
            }
        ]
        selector: labels.selectorLabels
    }
} if config.catcherEnabled and config.httpEnabled else None
