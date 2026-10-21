"""
Deployments for homerun2-notification-catcher.

The catcher is a pure consumer — no Service, no Ingress, no HTTPRoute. It
runs one (or more) replicas that join a Redis consumer group and dispatch
caught messages to the outputs declared in /etc/notification-catcher/config.yaml.

The optional sink (<name>-sink, config.sinkEnabled) runs the same image with
`args: [sink]` — a stand-in receiver the outputs can point at. It needs no
Redis and no routing config; see service.k / httproute.k for how it is
exposed and docs/sink.md for its API.

Container wiring:

* envFrom <name>-env             — Redis + logging + CONSUMER_GROUP + CONFIG_PATH
* envFrom <name>-secrets         — webhook URLs / output tokens (if any)
* env REDIS_PASSWORD valueFrom   — <name>-redis (if Redis is password-protected)
* volumeMount <name>-notify      — /etc/notification-catcher/config.yaml
                                   (ConfigMap is caller-supplied — the
                                   chart only emits it in dev when
                                   config.notifyConfig is set)
"""

import labels

config = labels.config

deployment = {
    apiVersion: "apps/v1"
    kind: "Deployment"
    metadata: {
        name: config.name
        namespace: config.namespace
        labels: labels.commonLabels
        if config.annotations:
            annotations: config.annotations
    }
    spec: {
        replicas: config.replicas
        selector: {
            matchLabels: labels.selectorLabels
        }
        strategy: {
            type: "RollingUpdate"
            rollingUpdate: {
                maxSurge: 1
                maxUnavailable: 0
            }
        }
        template: {
            metadata: {
                labels: labels.commonLabels
            }
            spec: {
                serviceAccountName: config.name
                containers: [
                    {
                        name: config.name
                        image: config.image
                        imagePullPolicy: config.imagePullPolicy
                        env: [
                            if config.redisPassword:
                                {
                                    name: "REDIS_PASSWORD"
                                    valueFrom: {
                                        secretKeyRef: {
                                            name: "{}-redis".format(config.name)
                                            key: "password"
                                        }
                                    }
                                }
                        ]
                        envFrom: [
                            {
                                configMapRef: {
                                    name: "{}-env".format(config.name)
                                }
                            }
                            if config.outputSecrets:
                                {
                                    secretRef: {
                                        name: "{}-secrets".format(config.name)
                                    }
                                }
                        ]
                        resources: {
                            requests: {
                                cpu: config.cpuRequest
                                memory: config.memoryRequest
                            }
                            limits: {
                                cpu: config.cpuLimit
                                memory: config.memoryLimit
                            }
                        }
                        securityContext: {
                            readOnlyRootFilesystem: True
                            runAsNonRoot: True
                            allowPrivilegeEscalation: False
                            capabilities: {
                                drop: ["ALL"]
                            }
                        }
                        volumeMounts: [
                            {
                                name: "tmp"
                                mountPath: "/tmp"
                            }
                            {
                                name: "notify-config"
                                mountPath: "/etc/notification-catcher"
                                readOnly: True
                            }
                        ]
                    }
                ]
                securityContext: {
                    runAsNonRoot: True
                    runAsUser: 65532
                    runAsGroup: 65532
                    fsGroup: 65532
                    seccompProfile: {
                        type: "RuntimeDefault"
                    }
                }
                volumes: [
                    {
                        name: "tmp"
                        emptyDir: {}
                    }
                    {
                        name: "notify-config"
                        configMap: {
                            name: "{}-notify".format(config.name)
                            items: [
                                {
                                    key: "config.yaml"
                                    path: "config.yaml"
                                }
                            ]
                        }
                    }
                ]
                affinity: {
                    podAntiAffinity: {
                        preferredDuringSchedulingIgnoredDuringExecution: [
                            {
                                weight: 100
                                podAffinityTerm: {
                                    labelSelector: {
                                        matchLabels: labels.selectorLabels
                                    }
                                    topologyKey: "kubernetes.io/hostname"
                                }
                            }
                        ]
                    }
                }
            }
        }
    }
} if config.catcherEnabled else None

sinkDeployment = {
    apiVersion: "apps/v1"
    kind: "Deployment"
    metadata: {
        name: labels.sinkName
        namespace: config.namespace
        labels: labels.sinkLabels
        if config.annotations:
            annotations: config.annotations
    }
    spec: {
        # One replica: the sink keeps posts in memory, a second pod would
        # split them. Recreate so the old pod releases nothing it shares.
        replicas: 1
        selector: {
            matchLabels: labels.sinkSelectorLabels
        }
        strategy: {
            type: "Recreate"
        }
        template: {
            metadata: {
                labels: labels.sinkLabels
            }
            spec: {
                serviceAccountName: labels.sinkName
                automountServiceAccountToken: False
                containers: [
                    {
                        name: "sink"
                        image: config.image
                        imagePullPolicy: config.imagePullPolicy
                        args: ["sink", "--capacity", str(config.sinkCapacity)]
                        ports: [
                            {
                                name: "http"
                                containerPort: config.sinkContainerPort
                                protocol: "TCP"
                            }
                        ]
                        env: [
                            {
                                name: "SINK_ADDR"
                                value: ":{}".format(config.sinkContainerPort)
                            }
                            {
                                name: "LOG_FORMAT"
                                value: config.logFormat
                            }
                            {
                                name: "LOG_LEVEL"
                                value: config.logLevel
                            }
                            if config.sinkBasePath:
                                {
                                    name: "BASE_PATH"
                                    value: config.sinkBasePath
                                }
                            if config.sinkToken:
                                {
                                    name: "SINK_TOKEN"
                                    valueFrom: {
                                        secretKeyRef: {
                                            name: labels.sinkName
                                            key: "token"
                                        }
                                    }
                                }
                        ]
                        # /health stays at the root (no BASE_PATH, no token).
                        startupProbe: {
                            httpGet: {
                                path: "/health"
                                port: "http"
                            }
                            periodSeconds: 2
                            timeoutSeconds: 3
                            failureThreshold: 15
                        }
                        livenessProbe: {
                            httpGet: {
                                path: "/health"
                                port: "http"
                            }
                            periodSeconds: 10
                            timeoutSeconds: 5
                            failureThreshold: 3
                        }
                        readinessProbe: {
                            httpGet: {
                                path: "/health"
                                port: "http"
                            }
                            periodSeconds: 5
                            timeoutSeconds: 3
                            failureThreshold: 3
                        }
                        resources: {
                            requests: {
                                cpu: config.cpuRequest
                                memory: config.memoryRequest
                            }
                            limits: {
                                cpu: config.cpuLimit
                                memory: config.memoryLimit
                            }
                        }
                        securityContext: {
                            readOnlyRootFilesystem: True
                            runAsNonRoot: True
                            allowPrivilegeEscalation: False
                            capabilities: {
                                drop: ["ALL"]
                            }
                        }
                    }
                ]
                securityContext: {
                    runAsNonRoot: True
                    runAsUser: 65532
                    runAsGroup: 65532
                    fsGroup: 65532
                    seccompProfile: {
                        type: "RuntimeDefault"
                    }
                }
            }
        }
    }
} if config.sinkEnabled else None
