"""
ServiceAccounts for homerun2-notification-catcher.

The sink has its own (<name>-sink): rendered alone as the
<name>-sink-kustomize artifact it is a separate Argo/Flux app, and two apps
must not own the same object. It needs no API access, so no token either.
"""

import labels

config = labels.config

serviceAccount = {
    apiVersion: "v1"
    kind: "ServiceAccount"
    metadata: {
        name: config.name
        namespace: config.namespace
        labels: labels.commonLabels
    }
} if config.catcherEnabled else None

sinkServiceAccount = {
    apiVersion: "v1"
    kind: "ServiceAccount"
    metadata: {
        name: labels.sinkName
        namespace: config.namespace
        labels: labels.sinkLabels
    }
    automountServiceAccountToken: False
} if config.sinkEnabled else None
