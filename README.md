# LLM Batch Gateway Operator

A Kubernetes operator that manages the lifecycle of [batch-gateway](https://github.com/opendatahub-io/batch-gateway) deployments. It reconciles a single `LLMBatchGateway` custom resource into the full set of Kubernetes resources by rendering the upstream Helm chart at runtime.

[Design Document](https://docs.google.com/document/d/1cYrR-GRnFEfaG-D2WwJqK5c8m6iq3QHyPHs6KMjou0o/edit?usp=sharing)

```
LLMBatchGateway CR
       |
       v
  Controller (Reconcile)
       |
       v
  specToHelmValues()  ->  Helm values map
       |
       v
  HelmRenderer.RenderChart()  ->  []unstructured.Unstructured
       |
       v
  Server-Side Apply  ->  K8s resources (Deployments, Services, etc.)
```

## 1. Prerequisites

- Go 1.25+
- kubectl
- [kustomize](https://kubectl.docs.kubernetes.io/installation/kustomize/)
- Docker or Podman

For local end-to-end testing:

- [Kind](https://kind.sigs.k8s.io/)
- [Helm](https://helm.sh/)

## 2. Quick Start

```bash
git clone --recurse-submodules https://github.com/opendatahub-io/llm-d-batch-gateway-operator.git
cd llm-d-batch-gateway-operator

# Build
make build

# Run tests
make test
```

If you already cloned without `--recurse-submodules`:

```bash
git submodule update --init
```

## 3. Project Structure

```
.
├── api/v1alpha1/                # CRD type definitions
│   ├── llmbatchgateway_types.go # LLMBatchGateway spec/status structs
│   └── zz_generated.deepcopy.go # Generated DeepCopy methods
├── cmd/main.go                  # Operator entrypoint
├── internal/controller/
│   ├── llmbatchgateway_controller.go  # Reconcile loop
│   ├── helm.go                        # CRD spec → Helm values → K8s objects
│   └── *_test.go                      # Unit and integration tests
├── config/
│   ├── crd/bases/               # Generated CRD YAML
│   ├── manager/                 # Operator Deployment manifest
│   ├── rbac/                    # RBAC roles and bindings
│   └── samples/                 # Example LLMBatchGateway CRs
├── hack/                        # Dev scripts (Kind cluster setup)
├── batch-gateway/               # Git submodule (upstream Helm chart)
├── Makefile
└── Dockerfile
```

## 4. Development

### 4.1 Modifying the CRD

To add or change fields in the `LLMBatchGateway` custom resource:

1. Edit the Go structs in `api/v1alpha1/llmbatchgateway_types.go`
2. Regenerate DeepCopy methods and CRD manifests:

```bash
make generate   # updates zz_generated.deepcopy.go
make manifests  # updates config/crd/bases/batch.llm-d.ai_llmbatchgateways.yaml
```

3. If the new field needs to be passed to the Helm chart, update `specToHelmValues()` in `internal/controller/helm.go`
4. Update sample CRs in `config/samples/` to include the new field

### 4.2 Modifying the Controller

The reconcile loop lives in `internal/controller/llmbatchgateway_controller.go`. The flow is:

1. Fetch the `LLMBatchGateway` CR
2. Call `HelmRenderer.RenderChart()` to produce unstructured K8s objects
3. Set owner references and apply each object via Server-Side Apply
4. Update status conditions (`Ready`, `APIServerAvailable`, `ProcessorAvailable`)

### 4.3 Modifying the Helm Values Mapping

The mapping from CRD spec to Helm values is in `internal/controller/helm.go`, function `specToHelmValues()`. When the upstream chart adds new values or the CRD adds new fields, update this function accordingly.

### 4.4 Updating the Helm Chart Submodule

The upstream batch-gateway Helm chart is pulled in as a git submodule at `batch-gateway/`.

Update to the latest upstream commit:

```bash
make update-submodule
git add batch-gateway
git commit -m "chore: update batch-gateway submodule"
```

Switch to a different branch:

```bash
git submodule set-branch -b <branch> batch-gateway
git submodule update --remote
git add batch-gateway .gitmodules
```

Switch to a specific commit or tag:

```bash
cd batch-gateway
git fetch
git checkout <commit-or-tag>
cd ..
git add batch-gateway
```

## 5. Testing

### 5.1 Unit and Integration Tests

```bash
make test
```

This runs all tests using [envtest](https://book.kubebuilder.io/reference/envtest) (a local control plane without a real cluster). Tests cover:

- `specToHelmValues()` mapping correctness
- Helm chart rendering (requires the submodule to be initialized)
- Controller reconciliation: resource creation, owner references, status conditions, spec updates

### 5.2 E2E Tests with Kind

One command sets up a full local environment:

```bash
make dev-deploy
```

This creates a Kind cluster and deploys PostgreSQL, Redis, MinIO, a vLLM simulator, the operator, and applies a dev `LLMBatchGateway` CR. Configurable via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `KIND_CLUSTER_NAME` | `batch-gateway-dev` | Kind cluster name |
| `NAMESPACE` | `default` | Target namespace |
| `OPERATOR_IMG` | `localhost/batch-gateway-operator:dev` | Operator image |
| `POSTGRESQL_PASSWORD` | `postgres` | PostgreSQL password |
| `MINIO_ACCESS_KEY` | `minioadmin` | MinIO access key |
| `MINIO_SECRET_KEY` | `minioadmin` | MinIO secret key |
| `APISERVER_IMG` | `ghcr.io/llm-d-incubation/batch-gateway-apiserver:latest` | API server image |
| `PROCESSOR_IMG` | `ghcr.io/llm-d-incubation/batch-gateway-processor:latest` | Processor image |
| `GC_IMG` | `ghcr.io/llm-d-incubation/batch-gateway-gc:latest` | GC image |
| `APISERVER_NODE_PORT` | `30080` | NodePort for API server |

Once the environment is up, run the upstream e2e tests:

```bash
make test-e2e
```

See `batch-gateway/test/e2e/README.md` for the full list of `TEST_*` environment variables.

Cleanup:

```bash
make dev-clean       # remove operator and dependencies, keep cluster
make dev-rm-cluster  # delete the Kind cluster
```

## 6. Building and Pushing the Image

```bash
make docker-build                           # build with default tag
make docker-build IMG=my-registry/operator:v0.1.0  # custom image
make docker-push IMG=my-registry/operator:v0.1.0
```

