"""
Service for the homerun2-notification-catcher sink (<name>-sink).

Only emitted with config.sinkEnabled — the catcher itself is a pure
consumer and has no Service. Outputs reach the sink in-cluster at
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
