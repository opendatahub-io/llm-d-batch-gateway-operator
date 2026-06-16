#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OPERATOR_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

NAMESPACE="${NAMESPACE:-default}"

log()  { echo "  [INFO]  $*"; }
step() { echo ""; echo "==> $*"; }
warn() { echo "  [WARN]  $*" >&2; }

cd "${OPERATOR_DIR}"

step "Deleting LLMBatchGateway CR..."
kubectl delete -f config/samples/dev.yaml -n "${NAMESPACE}" --ignore-not-found --timeout=60s

step "Undeploying operator..."
make undeploy 2>/dev/null || warn "undeploy failed (may already be removed)"

step "Uninstalling CRD..."
make uninstall 2>/dev/null || warn "uninstall failed (may already be removed)"

step "Removing PostgreSQL..."
helm uninstall postgresql -n "${NAMESPACE}" 2>/dev/null || warn "PostgreSQL not found"

step "Removing Redis..."
helm uninstall redis -n "${NAMESPACE}" 2>/dev/null || warn "Redis not found"

step "Removing MinIO..."
kubectl delete deployment,svc minio -n "${NAMESPACE}" --ignore-not-found

step "Removing vLLM simulator..."
kubectl delete deployment,svc vllm-sim -n "${NAMESPACE}" --ignore-not-found

step "Removing NodePort services..."
kubectl delete svc batch-gateway-apiserver-nodeport batch-gateway-processor-nodeport -n "${NAMESPACE}" --ignore-not-found

step "Removing secrets..."
kubectl delete secret batch-gateway-secrets -n "${NAMESPACE}" --ignore-not-found

log "Dev environment cleaned up."
log "To delete the Kind cluster: make dev-rm-cluster"
