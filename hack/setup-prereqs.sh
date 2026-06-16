#!/bin/bash
set -euo pipefail

# ── Setup prerequisites for LLMBatchGateway CR ──────────────────────────────
#
# Installs PostgreSQL, Redis, MinIO (with bucket), and creates the
# batch-gateway-secrets Secret in the target namespace.
# Run this before creating a LLMBatchGateway CR.
#
# Usage:
#   ./hack/setup-prereqs.sh                    # install in batch-api namespace
#   NAMESPACE=my-ns ./hack/setup-prereqs.sh    # install in custom namespace

# ── Configuration ────────────────────────────────────────────────────────────

NAMESPACE="${NAMESPACE:-batch-api}"

POSTGRESQL_RELEASE="${POSTGRESQL_RELEASE:-postgresql}"
POSTGRESQL_PASSWORD="${POSTGRESQL_PASSWORD:-postgres}"

REDIS_RELEASE="${REDIS_RELEASE:-redis}"

MINIO_RELEASE="${MINIO_RELEASE:-minio}"
MINIO_ROOT_USER="${MINIO_ROOT_USER:-minioadmin}"
MINIO_ROOT_PASSWORD="${MINIO_ROOT_PASSWORD:-minioadmin}"
MINIO_BUCKET="${MINIO_BUCKET:-batch-gateway}"

SECRET_NAME="${SECRET_NAME:-batch-gateway-secrets}"

# ── Helpers ──────────────────────────────────────────────────────────────────

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log()  { echo -e "${GREEN}[INFO]${NC}  $*"; }
step() { echo -e "${BLUE}[STEP]${NC}  $*"; }
warn() { echo -e "${YELLOW}[WARN]${NC}  $*" >&2; }
die()  { echo -e "${RED}[ERROR]${NC} $*" >&2; exit 1; }

wait_for_deployment() {
    local name="$1" ns="$2" timeout="${3:-180s}"
    step "Waiting for deployment '${name}' to be ready..."
    local retries=0
    while ! kubectl get deploy "${name}" -n "${ns}" &>/dev/null; do
        retries=$((retries + 1))
        if [ "$retries" -ge 30 ]; then
            die "Deployment '${name}' not visible after 60s"
        fi
        sleep 2
    done
    kubectl rollout status deploy/"${name}" -n "${ns}" --timeout="${timeout}"
    log "Deployment '${name}' is ready."
}

# ── Install steps ────────────────────────────────────────────────────────────

install_postgresql() {
    step "Installing PostgreSQL..."
    if helm status "${POSTGRESQL_RELEASE}" -n "${NAMESPACE}" &>/dev/null; then
        log "PostgreSQL already installed. Skipping."
        return
    fi
    helm install "${POSTGRESQL_RELEASE}" oci://registry-1.docker.io/bitnamicharts/postgresql \
        --namespace "${NAMESPACE}" --create-namespace \
        --set auth.postgresPassword="${POSTGRESQL_PASSWORD}" \
        --set auth.database=batch \
        --set primary.persistence.enabled=false

    kubectl rollout status statefulset/"${POSTGRESQL_RELEASE}" -n "${NAMESPACE}" --timeout=180s
    log "PostgreSQL installed (database: batch)."
}

install_redis() {
    step "Installing Redis..."
    if helm status "${REDIS_RELEASE}" -n "${NAMESPACE}" &>/dev/null; then
        log "Redis already installed. Skipping."
        return
    fi
    helm install "${REDIS_RELEASE}" oci://registry-1.docker.io/bitnamicharts/redis \
        --namespace "${NAMESPACE}" --create-namespace \
        --set architecture=standalone \
        --set auth.enabled=false

    kubectl rollout status statefulset/"${REDIS_RELEASE}-master" -n "${NAMESPACE}" --timeout=180s
    log "Redis installed (standalone, no auth)."
}

install_minio() {
    step "Installing MinIO..."
    if kubectl get deployment "${MINIO_RELEASE}" -n "${NAMESPACE}" &>/dev/null; then
        log "MinIO already exists. Skipping."
        return
    fi

    kubectl apply -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${MINIO_RELEASE}
  namespace: ${NAMESPACE}
  labels:
    app: ${MINIO_RELEASE}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: ${MINIO_RELEASE}
  template:
    metadata:
      labels:
        app: ${MINIO_RELEASE}
    spec:
      containers:
      - name: minio
        image: quay.io/minio/minio:RELEASE.2024-12-18T13-15-44Z
        args: ["server", "/data", "--console-address", ":9001"]
        env:
        - name: MINIO_ROOT_USER
          value: "${MINIO_ROOT_USER}"
        - name: MINIO_ROOT_PASSWORD
          value: "${MINIO_ROOT_PASSWORD}"
        ports:
        - containerPort: 9000
          name: api
        - containerPort: 9001
          name: console
        readinessProbe:
          httpGet:
            path: /minio/health/ready
            port: 9000
          initialDelaySeconds: 5
          periodSeconds: 5
        volumeMounts:
        - name: data
          mountPath: /data
      volumes:
      - name: data
        emptyDir: {}
---
apiVersion: v1
kind: Service
metadata:
  name: ${MINIO_RELEASE}
  namespace: ${NAMESPACE}
  labels:
    app: ${MINIO_RELEASE}
spec:
  selector:
    app: ${MINIO_RELEASE}
  ports:
  - name: api
    port: 9000
    targetPort: 9000
  - name: console
    port: 9001
    targetPort: 9001
EOF

    wait_for_deployment "${MINIO_RELEASE}" "${NAMESPACE}"

    step "Creating MinIO bucket '${MINIO_BUCKET}'..."
    local minio_pod
    minio_pod=$(kubectl get pod -n "${NAMESPACE}" -l "app=${MINIO_RELEASE}" \
        -o jsonpath='{.items[0].metadata.name}')
    [ -z "${minio_pod}" ] && die "No MinIO pod found"
    kubectl exec -n "${NAMESPACE}" "${minio_pod}" -- \
        mc alias set local http://localhost:9000 "${MINIO_ROOT_USER}" "${MINIO_ROOT_PASSWORD}" 2>/dev/null \
        || die "Failed to configure MinIO client"
    kubectl exec -n "${NAMESPACE}" "${minio_pod}" -- \
        mc mb "local/${MINIO_BUCKET}" 2>/dev/null || true
    log "MinIO installed (bucket: ${MINIO_BUCKET})."
}

create_secret() {
    step "Creating ${SECRET_NAME}..."

    local redis_url="redis://${REDIS_RELEASE}-master.${NAMESPACE}.svc.cluster.local:6379/0"
    local postgresql_url="postgresql://postgres:${POSTGRESQL_PASSWORD}@${POSTGRESQL_RELEASE}.${NAMESPACE}.svc.cluster.local:5432/batch?sslmode=disable"

    kubectl apply -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: ${SECRET_NAME}
  namespace: ${NAMESPACE}
stringData:
  redis-url: "${redis_url}"
  postgresql-url: "${postgresql_url}"
  inference-api-key: "dummy-api-key"
  s3-secret-access-key: "${MINIO_ROOT_PASSWORD}"
EOF
    log "Secret '${SECRET_NAME}' created."
}

# ── Main ─────────────────────────────────────────────────────────────────────

main() {
    step "Setting up prerequisites for LLMBatchGateway in ${NAMESPACE}"

    kubectl create namespace "${NAMESPACE}" 2>/dev/null || true

    install_postgresql
    install_redis
    install_minio
    create_secret

    echo ""
    log "Prerequisites ready:"
    echo "  Namespace:    ${NAMESPACE}"
    echo "  PostgreSQL:   ${POSTGRESQL_RELEASE}.${NAMESPACE}.svc.cluster.local:5432/batch"
    echo "  Redis:        ${REDIS_RELEASE}-master.${NAMESPACE}.svc.cluster.local:6379"
    echo "  MinIO (S3):   ${MINIO_RELEASE}.${NAMESPACE}.svc.cluster.local:9000 (bucket: ${MINIO_BUCKET})"
    echo "  Secret:       ${SECRET_NAME}"
}

main "$@"
