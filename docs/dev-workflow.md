# Batch Gateway Operator Development Workflow

Three-layer operator chain for AI Gateway + Batch Gateway:

```
opendatahub-operator → ai-gateway-operator → batch-gateway-operator
```

When upstream code changes, updates flow bottom-up: batch-gateway-operator → ai-gateway-operator → opendatahub-operator.


| Repo | URL |
|---|---|
| batch-gateway-operator | https://github.com/opendatahub-io/llm-d-batch-gateway-operator |
| ai-gateway-operator | https://github.com/opendatahub-io/ai-gateway-operator |
| opendatahub-operator | https://github.com/opendatahub-io/opendatahub-operator |

## 1. Update batch-gateway-operator

The upstream [batch-gateway](https://github.com/opendatahub-io/batch-gateway) repo is a git submodule. When it has new changes:

```bash
cd llm-d-batch-gateway-operator

# Update submodule to latest main
git submodule update --remote batch-gateway

# Check what changed
git -C batch-gateway log --oneline <old-sha>..HEAD

# Commit
git add batch-gateway
git commit -m "chore: update batch-gateway submodule to <short-sha>"
```

After merging to midstream [opendatahub-io/llm-d-batch-gateway-operator](https://github.com/opendatahub-io/llm-d-batch-gateway-operator), the change will be automatically synced to downstream [red-hat-data-services/llm-d-batch-gateway-operator](https://github.com/red-hat-data-services/llm-d-batch-gateway-operator). Wait for the sync to complete before proceeding. Check status via [upstream-auto-merge](https://github.com/red-hat-data-services/rhods-devops-infra/actions/workflows/upstream-auto-merge.yaml).
```bash
git ls-remote https://github.com/red-hat-data-services/llm-d-batch-gateway-operator.git refs/heads/main
```

## 2. Update ai-gateway-operator

ai-gateway-operator vendors batch-gateway-operator manifests via `hack/scripts/get-manifests.sh`.

```bash
cd ai-gateway-operator

# 1. Update the commit SHA in get-manifests.sh
#    Edit hack/scripts/get-manifests.sh — change the odh_commit and rhoai_commit in fetch_component call
#    Get latest SHA (ODH):   git ls-remote https://github.com/opendatahub-io/llm-d-batch-gateway-operator.git refs/heads/main
#    Get latest SHA (RHOAI): git ls-remote https://github.com/red-hat-data-services/llm-d-batch-gateway-operator.git refs/heads/main
vi hack/scripts/get-manifests.sh

# 2. Download manifests
make get-manifests

# 3. Verify
ls config/manifests/batchgateway/

# 4. Commit
git add hack/scripts/get-manifests.sh config/manifests/
git commit -m "chore: update batch-gateway manifests to <short-sha>"
```

After merging to midstream [opendatahub-io/ai-gateway-operator](https://github.com/opendatahub-io/ai-gateway-operator), the change will be automatically synced to downstream [red-hat-data-services/ai-gateway-operator](https://github.com/red-hat-data-services/ai-gateway-operator). Wait for the sync to complete before proceeding. Check status via [upstream-auto-merge](https://github.com/red-hat-data-services/rhods-devops-infra/actions/workflows/upstream-auto-merge.yaml).
```bash
git ls-remote https://github.com/red-hat-data-services/ai-gateway-operator.git refs/heads/main
```

## 3. Update opendatahub-operator

opendatahub-operator downloads the ai-gateway-operator Helm chart via `get_all_manifests.sh`.

```bash
cd opendatahub-operator

# 1. Update the commit SHA in get_all_manifests.sh
#    Edit the ai-gateway-operator entry in ODH_COMPONENT_CHARTS and RHOAI_COMPONENT_CHARTS
#    Get latest SHA (ODH):   git ls-remote https://github.com/opendatahub-io/ai-gateway-operator.git refs/heads/main
#    Get latest SHA (RHOAI): git ls-remote https://github.com/red-hat-data-services/ai-gateway-operator.git refs/heads/main
vi get_all_manifests.sh

# 2. Run codegen (if Go types changed)
make generate manifests api-docs

# 3. Update cloudmanager RBAC (if chart resources changed, requires bash 5+)
bash hack/update-cloudmanager-rbac.sh "$(which yq)" "$(which helm)"

# 4. Lint
make lint

# 5. Unit tests
go test ./internal/controller/modules/aigateway/...

# 6. Commit
git add -A
git commit -m "chore: update ai-gateway-operator chart to <short-sha>"
```

