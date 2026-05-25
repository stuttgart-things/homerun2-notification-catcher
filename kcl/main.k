"""
Main entry point for homerun2-notification-catcher Kubernetes deployment.
Imports all resources and exports manifests.
"""

import namespace
import serviceaccount
import configmap
import secret
import deploy

# Export all manifests, dropping None entries for resources whose generation
# was skipped (e.g. <name>-redis when Redis is password-free).
manifests = [
    m for m in [
        namespace.namespace
        serviceaccount.serviceAccount
        configmap.configMapEnv
        configmap.configMapNotify
        secret.secretRedis
        secret.secretOutputs
        deploy.deployment
    ] if m
]
