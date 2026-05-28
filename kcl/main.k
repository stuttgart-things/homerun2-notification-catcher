"""
Main entry point for homerun2-notification-catcher Kubernetes deployment.
Imports all resources and exports manifests.
"""

import serviceaccount
import configmap
import secret
import deploy

# Export all manifests, dropping None entries for resources whose generation
# was skipped (e.g. <name>-redis when Redis is password-free).
# Namespace is NOT emitted from the KCL profile — the consuming Application
# uses `CreateNamespace=true`, and PR-preview namespaces are pre-created by
# Kyverno. See stuttgart-things/homerun2-omni-pitcher#132.
manifests = [
    m for m in [
        serviceaccount.serviceAccount
        configmap.configMapEnv
        configmap.configMapNotify
        secret.secretRedis
        secret.secretOutputs
        deploy.deployment
    ] if m
]
