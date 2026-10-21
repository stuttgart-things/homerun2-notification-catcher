"""
HTTPRoutes (Gateway API) for the homerun2-notification-catcher sink and,
further down, the catcher's own UI.

Emitted with config.sinkHttpRouteEnabled (requires sinkEnabled). Matches
config.sinkBasePath as PathPrefix and forwards the path unchanged — the
sink serves its UI and API under BASE_PATH itself.
"""

import labels

config = labels.config

_hostname = config.sinkHttpRouteHostname

sinkHttpRoute = {
    apiVersion: "gateway.networking.k8s.io/v1"
    kind: "HTTPRoute"
    metadata: {
        name: labels.sinkName
        namespace: config.namespace
        labels: labels.sinkLabels
        if config.annotations or config.sinkHttpRouteAnnotations:
            annotations: {
                **config.annotations
                **config.sinkHttpRouteAnnotations
            }
    }
    spec: {
        parentRefs: [
            {
                name: config.sinkHttpRouteParentRefName
                if config.sinkHttpRouteParentRefNamespace:
                    namespace: config.sinkHttpRouteParentRefNamespace
            }
        ]
        if _hostname:
            hostnames: [_hostname]
        rules: [
            {
                matches: [
                    {
                        path: {
                            type: "PathPrefix"
                            value: config.sinkBasePath or "/"
                        }
                    }
                ]
                backendRefs: [
                    {
                        name: labels.sinkName
                        port: config.sinkServicePort
                    }
                ]
            }
        ]
    }
} if config.sinkEnabled and config.sinkHttpRouteEnabled else None

# The catcher's own UI (#22): config.httpRouteEnabled, PathPrefix
# config.httpBasePath, to Service <name>.
httpRoute = {
    apiVersion: "gateway.networking.k8s.io/v1"
    kind: "HTTPRoute"
    metadata: {
        name: config.name
        namespace: config.namespace
        labels: labels.commonLabels
        if config.annotations or config.httpRouteAnnotations:
            annotations: {
                **config.annotations
                **config.httpRouteAnnotations
            }
    }
    spec: {
        parentRefs: [
            {
                name: config.httpRouteParentRefName
                if config.httpRouteParentRefNamespace:
                    namespace: config.httpRouteParentRefNamespace
            }
        ]
        if config.httpRouteHostname:
            hostnames: [config.httpRouteHostname]
        rules: [
            {
                matches: [
                    {
                        path: {
                            type: "PathPrefix"
                            value: config.httpBasePath or "/"
                        }
                    }
                ]
                backendRefs: [
                    {
                        name: config.name
                        port: config.httpServicePort
                    }
                ]
            }
        ]
    }
} if config.catcherEnabled and config.httpEnabled and config.httpRouteEnabled else None
