#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OPERATOR_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

# ── Configuration (all overridable via env) ──────────────────────────────────

KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-batch-gateway-dev}"
NAMESPACE="${NAMESPACE:-default}"
# Operator namespace, must match config/default/kustomization.yaml's `namespace:` field.
OPERATOR_NAMESPACE="${OPERATOR_NAMESPACE:-llm-d-batch-gateway-operator-system}"
OPERATOR_IMG="${OPERATOR_IMG:-localhost/llm-d-batch-gateway-operator:dev}"

POSTGRESQL_PASSWORD="${POSTGRESQL_PASSWORD:-postgres}"
MINIO_ACCESS_KEY="${MINIO_ACCESS_KEY:-minioadmin}"
MINIO_SECRET_KEY="${MINIO_SECRET_KEY:-minioadmin}"
MINIO_REGION="${MINIO_REGION:-us-east-1}"

GATEWAY_API_VERSION="${GATEWAY_API_VERSION:-}"
PROMETHEUS_OPERATOR_VERSION="${PROMETHEUS_OPERATOR_VERSION:-}"

# read from params.env if you wanna test a different image, go update params.env
PARAMS_ENV="${OPERATOR_DIR}/config/base/params.env"
param_image() { grep -E "^$1=" "${PARAMS_ENV}" 2>/dev/null | head -n1 | cut -d= -f2- || true; }
APISERVER_IMG="${APISERVER_IMG:-$(param_image LLM_D_BATCH_GATEWAY_APISERVER_IMAGE)}"
PROCESSOR_IMG="${PROCESSOR_IMG:-$(param_image LLM_D_BATCH_GATEWAY_PROCESSOR_IMAGE)}"
GC_IMG="${GC_IMG:-$(param_image LLM_D_BATCH_GATEWAY_GC_IMAGE)}"
VLLM_SIM_IMG="${VLLM_SIM_IMG:-ghcr.io/llm-d/llm-d-inference-sim:latest}"

# Port configuration (matches batch-gateway defaults)
APISERVER_NODE_PORT="${APISERVER_NODE_PORT:-30080}"
APISERVER_OBS_NODE_PORT="${APISERVER_OBS_NODE_PORT:-30081}"
PROCESSOR_NODE_PORT="${PROCESSOR_NODE_PORT:-30090}"
LOCAL_PORT="${LOCAL_PORT:-8000}"
LOCAL_OBS_PORT="${LOCAL_OBS_PORT:-8081}"
LOCAL_PROCESSOR_PORT="${LOCAL_PROCESSOR_PORT:-9090}"

# ── Logging ──────────────────────────────────────────────────────────────────

log()  { echo "  [INFO]  $*"; }
step() { echo ""; echo "==> $*"; }
warn() { echo "  [WARN]  $*" >&2; }
die()  { echo "  [FATAL] $*" >&2; exit 1; }

# ── Prerequisites ────────────────────────────────────────────────────────────

CONTAINER_TOOL=""

detect_CONTAINER_TOOL() {
    if command -v docker &>/dev/null && docker info &>/dev/null 2>&1; then
        echo "docker"
    elif command -v podman &>/dev/null; then
        echo "podman"
    else
        die "Neither docker (running) nor podman found."
    fi
}

check_prerequisites() {
    step "Checking prerequisites..."
    if ! command -v kustomize &>/dev/null && [ -x "${OPERATOR_DIR}/bin/kustomize" ]; then
        export PATH="${OPERATOR_DIR}/bin:${PATH}"
    fi
    local missing=()
    for cmd in kubectl helm kind kustomize curl; do
        command -v "$cmd" &>/dev/null || missing+=("$cmd")
    done
    if [ ${#missing[@]} -gt 0 ]; then
        die "Missing required tools: ${missing[*]}"
    fi
    CONTAINER_TOOL="$(detect_CONTAINER_TOOL)"
    if [ "${CONTAINER_TOOL}" = "podman" ]; then
        export KIND_EXPERIMENTAL_PROVIDER=podman
    fi
    log "Container tool: ${CONTAINER_TOOL}"
}

# ── Kind Cluster ─────────────────────────────────────────────────────────────

ensure_cluster() {
    step "Ensuring Kind cluster '${KIND_CLUSTER_NAME}'..."

    if kind get clusters 2>/dev/null | grep -qx "${KIND_CLUSTER_NAME}"; then
        log "Cluster already exists. Switching context..."
        kubectl config use-context "kind-${KIND_CLUSTER_NAME}"
    else
        kind create cluster --name "${KIND_CLUSTER_NAME}" --config=- <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  extraPortMappings:
  - containerPort: ${APISERVER_NODE_PORT}
    hostPort: ${LOCAL_PORT}
    protocol: TCP
  - containerPort: ${APISERVER_OBS_NODE_PORT}
    hostPort: ${LOCAL_OBS_PORT}
    protocol: TCP
  - containerPort: ${PROCESSOR_NODE_PORT}
    hostPort: ${LOCAL_PROCESSOR_PORT}
    protocol: TCP
EOF
    fi

    log "Kind cluster ready."
}

# ── Dependencies ─────────────────────────────────────────────────────────────

install_prereqs() {
    NAMESPACE="${NAMESPACE}" \
    POSTGRESQL_PASSWORD="${POSTGRESQL_PASSWORD}" \
    MINIO_ROOT_USER="${MINIO_ACCESS_KEY}" \
    MINIO_ROOT_PASSWORD="${MINIO_SECRET_KEY}" \
        bash "${SCRIPT_DIR}/setup-prereqs.sh"
}

install_gateway_api_crds() {
    step "Installing Gateway API CRDs..."

    local version="${GATEWAY_API_VERSION:-}"
    if [ -z "${version}" ]; then
        version=$(cd "${OPERATOR_DIR}" && go list -m -f '{{.Version}}' sigs.k8s.io/gateway-api)
    fi

    if [ -z "${version}" ]; then
        die "Could not determine Gateway API version from go.mod (set GATEWAY_API_VERSION to override)."
    fi

    log "Gateway API version: ${version}"

    kubectl apply -f "https://github.com/kubernetes-sigs/gateway-api/releases/download/${version}/standard-install.yaml"

    log "Gateway API CRDs installed."
}

install_prometheus_operator_crds() {
    step "Installing Prometheus Operator CRDs..."

    local version="${PROMETHEUS_OPERATOR_VERSION:-}"
    if [ -z "${version}" ]; then
        version=$(cd "${OPERATOR_DIR}" && go list -m -f '{{.Version}}' github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring)
    fi

    if [ -z "${version}" ]; then
        die "Could not determine Prometheus Operator version from go.mod (set PROMETHEUS_OPERATOR_VERSION to override)."
    fi

    log "Prometheus Operator version: ${version}"

    kubectl apply --server-side -f "https://github.com/prometheus-operator/prometheus-operator/releases/download/${version}/stripped-down-crds.yaml"

    log "Prometheus Operator CRDs installed."
}

install_vllm_sim() {
    step "Installing vLLM simulator..."

    if kubectl get deployment vllm-sim -n "${NAMESPACE}" &>/dev/null; then
        log "vLLM simulator already exists. Skipping."
        return
    fi

    kubectl apply -n "${NAMESPACE}" -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: vllm-sim
spec:
  replicas: 1
  selector:
    matchLabels:
      app: vllm-sim
  template:
    metadata:
      labels:
        app: vllm-sim
    spec:
      containers:
      - name: vllm-sim
        image: ${VLLM_SIM_IMG}
        imagePullPolicy: IfNotPresent
        args:
        - --model
        - sim-model
        - --port
        - "8000"
        - --time-to-first-token=50ms
        - --inter-token-latency=100ms
        ports:
        - containerPort: 8000
          name: http
        resources:
          requests:
            cpu: 10m
---
apiVersion: v1
kind: Service
metadata:
  name: vllm-sim
spec:
  selector:
    app: vllm-sim
  ports:
  - name: http
    port: 8000
    targetPort: 8000
EOF

    kubectl rollout status deployment vllm-sim -n "${NAMESPACE}" --timeout=120s
    log "vLLM simulator installed."
}

# ── Operator ─────────────────────────────────────────────────────────────────

build_operator() {
    step "Building operator image '${OPERATOR_IMG}'..."
    cd "${OPERATOR_DIR}"
    local build_args=(-t "${OPERATOR_IMG}" -f Dockerfile)
    if [ "${CONTAINER_TOOL}" = "podman" ]; then
        build_args+=(--ignorefile Dockerfile.dockerignore)
    fi
    ${CONTAINER_TOOL} build "${build_args[@]}" .
    log "Operator image built."
}

load_operator() {
    step "Loading operator image into Kind..."
    if [ "${CONTAINER_TOOL}" = "podman" ]; then
        podman save "${OPERATOR_IMG}" | kind load image-archive /dev/stdin --name "${KIND_CLUSTER_NAME}"
    else
        kind load docker-image "${OPERATOR_IMG}" --name "${KIND_CLUSTER_NAME}"
    fi
    log "Operator image loaded."
}

deploy_operator() {
    step "Installing CRD and deploying operator..."
    cd "${OPERATOR_DIR}"

    kubectl create namespace "${OPERATOR_NAMESPACE}" 2>/dev/null || true
    make install
    IMG="${OPERATOR_IMG}" make deploy

    # override the env vars on the deployment to pin the images dev wants (defaults read from params.env.
    step "Setting 3 component images on the operator deployment as env variable..."
    kubectl set env deployment/llm-d-batch-gateway-operator -n "${OPERATOR_NAMESPACE}" \
        LLM_D_BATCH_GATEWAY_APISERVER_IMAGE="${APISERVER_IMG}" \
        LLM_D_BATCH_GATEWAY_PROCESSOR_IMAGE="${PROCESSOR_IMG}" \
        LLM_D_BATCH_GATEWAY_GC_IMAGE="${GC_IMG}"

    kubectl rollout status deployment/llm-d-batch-gateway-operator \
        -n "${OPERATOR_NAMESPACE}" --timeout=120s

    log "Operator deployed."
}

apply_cr() {
    step "Applying dev LLMBatchGateway CR..."
    cd "${OPERATOR_DIR}"

    kubectl apply -f config/samples/dev.yaml -n "${NAMESPACE}"

    log "CR applied. Operator will reconcile and create batch-gateway components."
}

# ── NodePort Services ────────────────────────────────────────────────────────

create_nodeport_services() {
    step "Creating NodePort services for local access..."

    kubectl apply -n "${NAMESPACE}" -f - <<EOF
apiVersion: v1
kind: Service
metadata:
  name: batch-gateway-apiserver-nodeport
spec:
  type: NodePort
  selector:
    app.kubernetes.io/name: batch-gateway-apiserver
    app.kubernetes.io/instance: batch-gateway-dev
    app.kubernetes.io/component: apiserver
  ports:
  - name: http
    protocol: TCP
    port: 8000
    targetPort: http
    nodePort: ${APISERVER_NODE_PORT}
  - name: observability
    protocol: TCP
    port: 8081
    targetPort: observability
    nodePort: ${APISERVER_OBS_NODE_PORT}
---
apiVersion: v1
kind: Service
metadata:
  name: batch-gateway-processor-nodeport
spec:
  type: NodePort
  selector:
    app.kubernetes.io/name: batch-gateway-processor
    app.kubernetes.io/instance: batch-gateway-dev
    app.kubernetes.io/component: processor
  ports:
  - name: metrics
    protocol: TCP
    port: 9090
    targetPort: metrics
    nodePort: ${PROCESSOR_NODE_PORT}
EOF

    log "NodePort services created."
}

# ── Wait & Verify ────────────────────────────────────────────────────────────

wait_for_batch_gateway() {
    step "Waiting for batch-gateway components..."

    local timeout=180
    local elapsed=0
    while [ $elapsed -lt $timeout ]; do
        local ready
        ready=$(kubectl get deployments -n "${NAMESPACE}" \
            -l "app.kubernetes.io/instance=batch-gateway-dev" \
            -o jsonpath='{range .items[*]}{.status.readyReplicas}{"\n"}{end}' 2>/dev/null | grep -c "^[1-9]" || true)

        if [ "$ready" -ge 3 ]; then
            log "All batch-gateway components are ready."
            return
        fi

        sleep 5
        elapsed=$((elapsed + 5))
        log "Waiting... ($elapsed/${timeout}s, $ready/3 deployments ready)"
    done

    warn "Timed out waiting for batch-gateway components. Showing current state:"
    kubectl get pods -n "${NAMESPACE}"
}

print_status() {
    step "Deployment complete!"

    echo "----------------------------------------"
    echo "  Operator (${OPERATOR_NAMESPACE}):"
    kubectl get all -n "${OPERATOR_NAMESPACE}"

    echo "----------------------------------------"
    echo "  CR Status:"
    kubectl get llmbatchgateway -n "${NAMESPACE}"

    echo "----------------------------------------"
    echo "  Workloads (${NAMESPACE}):"
    kubectl get all -n "${NAMESPACE}"

    echo "----------------------------------------"
    echo "  Access:"
    echo "    API Server:  http://localhost:${LOCAL_PORT}"
    echo "    Observability: http://localhost:${LOCAL_OBS_PORT}"
    echo "    Processor:   http://localhost:${LOCAL_PROCESSOR_PORT}"

    echo "----------------------------------------"
    echo "  Cleanup:"
    echo "    make dev-clean        # remove operator + deps"
    echo "    make dev-rm-cluster   # delete Kind cluster"
}

# ── Main ─────────────────────────────────────────────────────────────────────

main() {
    echo ""
    echo "  ╔══════════════════════════════════════════════╗"
    echo "  ║   Batch Gateway Operator - Dev Deployment    ║"
    echo "  ╚══════════════════════════════════════════════╝"
    echo ""

    check_prerequisites
    build_operator
    ensure_cluster
    install_gateway_api_crds
    install_prometheus_operator_crds
    install_prereqs
    install_vllm_sim
    load_operator
    deploy_operator
    apply_cr
    wait_for_batch_gateway
    create_nodeport_services
    print_status
}

main "$@"
