#!/usr/bin/env bash
# Runs batch-gateway e2e tests and verifies async dispatch via llm-d-async metrics.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OPERATOR_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

BATCH_GATEWAY_DIR="${BATCH_GATEWAY_DIR:-batch-gateway}"
TEST_RUN="${TEST_RUN:-}"
NAMESPACE="${NAMESPACE:-default}"
CR_NAME="${CR_NAME:-batch-gateway-dev}"

log()  { echo "  [INFO]  $*"; }
step() { echo ""; echo "==> $*"; }
die()  { echo "  [FATAL] $*" >&2; exit 1; }

# ── Helpers ──────────────────────────────────────────────────────────────────

METRICS_PF_PID=""

get_async_metrics() {
    # Start port-forward if not already running
    if [ -z "$METRICS_PF_PID" ] || ! kill -0 "$METRICS_PF_PID" 2>/dev/null; then
        kubectl port-forward -n "${NAMESPACE}" \
            "deployment/${CR_NAME}-async-processor" 19090:9090 &>/dev/null &
        METRICS_PF_PID=$!
        # Wait for port-forward to be ready
        local attempt
        for attempt in $(seq 1 30); do
            if curl -sf http://localhost:19090/metrics &>/dev/null; then
                break
            fi
            sleep 1
        done
    fi
    curl -sf http://localhost:19090/metrics
}

cleanup_metrics_pf() {
    if [ -n "$METRICS_PF_PID" ]; then
        kill "$METRICS_PF_PID" 2>/dev/null
        wait "$METRICS_PF_PID" 2>/dev/null || true
    fi
}
trap cleanup_metrics_pf EXIT

get_async_metric() {
    local metric_name="$1"
    # grep may return 1 if metric not yet emitted (no requests processed yet); default to 0
    get_async_metrics | { grep "^${metric_name}" || true; } | awk '{sum+=$2} END {printf "%d", sum}'
}

# ── Detect dispatch mode from processor configmap ────────────────────────────

dispatch_mode=$(kubectl get configmap -n "${NAMESPACE}" \
    -l "app.kubernetes.io/instance=${CR_NAME},app.kubernetes.io/component=processor" \
    -o jsonpath='{.items[0].data.config\.yaml}' | yq '.dispatch_mode // "sync"')
log "Detected dispatch mode: $dispatch_mode"

# ── Record async metrics before tests ────────────────────────────────────────

before_success=0
if [[ "$dispatch_mode" == "async" ]]; then
    step "Recording async metrics before tests..."
    before_success=$(get_async_metric "llm_d_async_async_successful_requests_total")
    log "async_successful_requests_total before: $before_success"
fi

# ── Run batch-gateway e2e tests ──────────────────────────────────────────────

step "Running batch-gateway e2e tests..."
cd "${OPERATOR_DIR}/${BATCH_GATEWAY_DIR}/test/e2e"
go test -v -count=1 -run "${TEST_RUN}" ./...

# ── Verify async dispatch via metrics ────────────────────────────────────────

if [[ "$dispatch_mode" == "async" ]]; then
    step "Verifying requests went through llm-d-async..."

    after_success=$(get_async_metric "llm_d_async_async_successful_requests_total")
    diff=$((after_success - before_success))

    if [ "$diff" -gt 0 ]; then
        log "Confirmed: $diff requests completed via llm-d-async ($before_success → $after_success)"
    else
        die "No requests went through llm-d-async (metric unchanged at $before_success)"
    fi

    # Verify worker pool is active
    pool_metric=$(get_async_metric "llm_d_async_async_pool_worker_limit")
    if [ "$pool_metric" -gt 0 ]; then
        log "Worker pool active: pool_worker_limit=$pool_metric"
    else
        die "Worker pool not active (async_pool_worker_limit=0)"
    fi

    # Verify gate budget if a gate type is configured
    gate_type=""
    gate_type=$(kubectl get deployment "${CR_NAME}-async-processor" -n "${NAMESPACE}" -o json \
        | jq -r '.spec.template.spec.containers[0].args[]' \
        | grep '^\[' | jq -r '.[0].gate_type // empty')
    if [ -n "$gate_type" ]; then
        budget_line=$(get_async_metrics | grep "^llm_d_async_async_dispatch_budget" | head -1)
        if [ -n "$budget_line" ]; then
            log "Gate budget: $budget_line"
        else
            die "Gate not active: async_dispatch_budget metric not found"
        fi
    fi
fi

log "All batch-gateway e2e checks passed."
