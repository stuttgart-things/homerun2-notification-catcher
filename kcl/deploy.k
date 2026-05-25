"""
Deployment for homerun2-notification-catcher.

The catcher is a pure consumer — no Service, no Ingress, no HTTPRoute. It
runs one (or more) replicas that join a Redis consumer group and dispatch
caught messages to the outputs declared in /etc/notification-catcher/config.yaml.

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
}
