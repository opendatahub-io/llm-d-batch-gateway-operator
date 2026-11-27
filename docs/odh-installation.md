# ODH Installation Guide

Deploy LLM Batch Gateway through the ODH platform on OpenShift.

For upgrades, read the [Async Dispatch and Core Storage Migration](async-default-migration.md) guide first. It covers the removal of synchronous dispatch (an existing synchronous deployment must be drained and re-deployed as async), the separate PostgreSQL migration for Batch Gateway metadata, and the transport choice for async queues.

## Architecture

```
opendatahub-operator (platform orchestrator)
  → ai-gateway-operator (module operator)
    → llm-d-batch-gateway-operator (sub-module operator)
      → operands: apiserver, processor, gc
```

1. **User** creates a DataScienceCluster CR with `aigateway.managementState: Managed` and `aigateway.batchGateway.managementState: Managed`
2. **ODH operator** renders ai-gateway-operator's kustomize manifests and deploys it (Deployment, RBAC, CRD) into the applications namespace
3. **ODH operator** creates an AIGateway CR with the batch gateway config projected from DSC
4. **ai-gateway-operator** reconciles the AIGateway CR and deploys llm-d-batch-gateway-operator (Deployment, RBAC, CRD)
5. **User** creates an LLMBatchGateway CR in a workload namespace with database, storage, and inference gateway config
6. **llm-d-batch-gateway-operator** reconciles the LLMBatchGateway CR and deploys the operands: API server, request processor, and garbage collector

## Prerequisites

- OpenShift cluster with ODH operator installed
- `oc` / `kubectl` CLI connected to the cluster
- `helm` (for installing PostgreSQL and optional Redis test dependencies)
- `kubectl` (for installing the SeaweedFS test dependency)
- External dependencies for LLMBatchGateway: PostgreSQL and S3-compatible storage. Async dispatch needs either Redis/Valkey for the default sorted-set transport or PostgreSQL for the experimental SQL transport.

## Installation

### Step 1: Create DSCI and DSC with Batch Gateway enabled

```bash
# DSCInitialization
oc apply -f - <<'EOF'
apiVersion: dscinitialization.opendatahub.io/v2
kind: DSCInitialization
metadata:
  name: default-dsci
spec:
  applicationsNamespace: opendatahub
  monitoring:
    managementState: Removed
    namespace: opendatahub
  trustedCABundle:
    managementState: Removed
EOF

# DataScienceCluster with aigateway + batchGateway enabled
oc apply -f - <<'EOF'
apiVersion: datasciencecluster.opendatahub.io/v2
kind: DataScienceCluster
metadata:
  name: default-dsc
spec:
  components:
    aigateway:
      managementState: Managed
      batchGateway:
        managementState: Managed
EOF
```

Check AI Gateway status:

```bash
$ oc get deployments -n opendatahub
NAME                           READY   UP-TO-DATE   AVAILABLE   AGE
ai-gateway-operator            1/1     1            1           2m
llm-d-batch-gateway-operator   1/1     1            1           90s

$ oc get aigateway
NAME                READY   REASON   VERSION
default-aigateway   True             0.0.0-dev

$ oc get aigateway default-aigateway -o yaml
apiVersion: components.platform.opendatahub.io/v1alpha1
kind: AIGateway
metadata:
  name: default-aigateway
  labels:
    platform.opendatahub.io/part-of: datasciencecluster
  ownerReferences:
  - apiVersion: datasciencecluster.opendatahub.io/v2
    kind: DataScienceCluster
    name: default-dsc
spec:
  batchGateway:
    managementState: Managed
status:
  conditions:
  - status: "True"
    type: Ready
  - status: "True"
    type: ProvisioningSucceeded
  - status: "True"
    type: DeploymentsAvailable
  phase: Ready
```

### Step 2: Set up external dependencies

LLMBatchGateway requires PostgreSQL and S3-compatible storage. Async dispatch
uses either Redis/Valkey sorted-set or PostgreSQL SQL queues. Use the provided
script:

```bash
export NAMESPACE=batch-api
export SEAWEEDFS_ACCESS_KEY="batch-gateway-$(openssl rand -hex 16)"
export SEAWEEDFS_SECRET_KEY="$(openssl rand -hex 32)"
INSTALL_REDIS=true bash hack/setup-prereqs.sh
```

Use unique values for `SEAWEEDFS_ACCESS_KEY` and `SEAWEEDFS_SECRET_KEY`. The
setup script requires both values and stores the secret key in
`batch-gateway-secrets`. For production, use credentials from a managed Secret
and do not use the local SeaweedFS setup script.


This creates the `$NAMESPACE` namespace and installs:
- PostgreSQL (`postgresql.$NAMESPACE.svc.cluster.local:5432`)
- SeaweedFS S3 (`seaweedfs.$NAMESPACE.svc.cluster.local:8333`, bucket: `batch-gateway`)
- Redis (`redis-master.$NAMESPACE.svc.cluster.local:6379`; its URL is added to `batch-gateway-secrets`)
- `batch-gateway-secrets` Secret

For production, replace PostgreSQL and S3 with your own endpoints, plus Redis
when you use the Redis async transport. For the SQL async transport, ensure the
referenced Secret contains `postgresql-url` and configure
`spec.processor.asyncConfig.sql`.

### Step 3: Create LLMBatchGateway CR

```bash
oc apply -f - <<EOF
apiVersion: batch.llm-d.ai/v1alpha1
kind: LLMBatchGateway
metadata:
  name: batch-gateway
  namespace: $NAMESPACE
spec:
  secretRef:
    name: batch-gateway-secrets
  dbBackend: postgresql
  extraEndpoints:
  - /v1/classify
  fileStorage:
    s3:
      region: us-east-1
      endpoint: http://seaweedfs.$NAMESPACE.svc.cluster.local:8333
      accessKeyId: ${SEAWEEDFS_ACCESS_KEY}
      usePathStyle: true
      autoCreateBucket: true
  apiServer:
    replicas: 1
  processor:
    replicas: 1
    config:
      inputFileDiskBudgetPercent: 10
      progressUpdateInterval: 15s
    # Batch requests are always dispatched asynchronously. Each model maps to
    # an inference pool; asyncConfig configures the async processor.
    modelGateways:
      llama-3:
        inferencePoolName: llama-pool
    asyncConfig:
      concurrency: 8
      drainTimeout: 2m
      resultPollTimeout: 30s
      inferenceGateway:
        url: http://inference-gateway.default.svc.cluster.local:8000
      redis:
        requestPathURL: /v1/completions
        pollIntervalMs: 1000
        batchSize: 10
        queuesConfig:
        - name: "llm-d-async:requests:llama-pool"
          workerPoolID: default
  gc:
    interval: 30m
EOF
```

#### API server tenant and proxy token checks

The batch-gateway API server can require two things from the auth proxy in front of it, and answer `401` to requests without them:

- a shared token in `X-Gateway-Token`, proving the request came through the proxy;
- a tenant header (`X-MaaS-Username` by default), naming the tenant whose files and batches the request may use.

`spec.apiServer.config.allowAnonymousTenant` turns both checks on or off.

**Unset or `true` (default): the checks are off.** A request without the tenant header is served as the `default` tenant. This is how the API server behaved before the checks existed, so a gateway upgraded from an earlier release needs no change and keeps serving. The API server logs it at startup:

```text
anonymous tenant allowed: the proxy token is not checked and requests without the tenant header are served as the default tenant
```

> **Warning:** with the checks off, the API server trusts the tenant header from anyone who can reach it. A client that bypasses the auth proxy, or a proxy that passes a client's own tenant header through, can act as any tenant. Keep the API server reachable only through the auth proxy, and have the proxy set the tenant header from the authenticated identity.

**`false`: both checks are on.** Turn them on in this order, so the API server never runs without a token the proxy can send:

1. Add a token to the Secret named in `spec.secretRef`, under the key `apiserver-proxy-token`. With the checks on, the API server does not start without it.

   ```bash
   TOKEN=$(openssl rand -hex 32)
   oc -n $NAMESPACE patch secret batch-gateway-secrets --type merge \
     -p "{\"stringData\":{\"apiserver-proxy-token\":\"$TOKEN\"}}"
   ```

2. Make the auth proxy send the same token in `X-Gateway-Token` and set the tenant header from the authenticated identity, replacing any value the client sent. With Kuadrant, add both to the batch route's AuthPolicy:

   ```yaml
   spec:
     rules:
       response:
         success:
           headers:
             x-maas-username:
               plain:
                 selector: auth.identity.user.username   # depends on the authentication method
             x-gateway-token:
               plain:
                 value: "<TOKEN>"                        # same value as apiserver-proxy-token
   ```

   See the batch-gateway [Kuadrant integration guide](https://github.com/llm-d/llm-d-batch-gateway/blob/main/docs/guides/kuadrant-integration.md#4-authentication--authorization) for complete policies. Anyone who can read AuthPolicies in that namespace can read a token in `plain.value`, so restrict that access like the Secret.

3. Set the field. The operator rolls out the API server with the checks on.

   ```yaml
   spec:
     apiServer:
       config:
         allowAnonymousTenant: false
   ```

4. Check: a request through the auth proxy succeeds, and a request sent to the API server Service directly gets `401`. The startup log line above no longer appears.

To rotate the token, update the Secret and the AuthPolicy, then restart the API server: it reads the token once, at startup.

If you roll the operator back to a release without this field, the gateway serves without the checks again, whatever you set here: the older release does not render the setting.

See the batch-gateway [security guide](https://github.com/llm-d/llm-d-batch-gateway/blob/main/docs/guides/security.md) for how the API server handles the token and the tenant header.

### Step 4: Check Batch Gateway status

```bash
$ oc get llmbatchgateway -n batch-api
NAMESPACE   NAME            READY   API-READY   PROC-READY   GC-READY   ASYNC-READY   AGE
batch-api   batch-gateway   True    1           1            1          1             20s

$ oc get pods -n batch-api
NAME                                          READY   STATUS    RESTARTS   AGE
batch-gateway-apiserver-79b49fc6ff-d9fbh      1/1     Running   0          25s
batch-gateway-gc-64987bc9c4-xqrtb             1/1     Running   0          25s
batch-gateway-processor-7cc44bf4dd-m9jbk      1/1     Running   0          25s
batch-gateway-async-processor-6b7d9c4f5-tl2xw  1/1     Running   0          25s

$ oc get llmbatchgateway batch-gateway -n batch-api -o yaml
apiVersion: batch.llm-d.ai/v1alpha1
kind: LLMBatchGateway
metadata:
  name: batch-gateway
  namespace: batch-api
spec:
  apiServer:
    replicas: 1
  dbBackend: postgresql
  fileStorage:
    s3:
      accessKeyId: <generated-access-key>
      autoCreateBucket: true
      endpoint: http://seaweedfs.batch-api.svc.cluster.local:8333
      region: us-east-1
      usePathStyle: true
  gc:
    interval: 30m
  processor:
    asyncConfig:
      concurrency: 8
      drainTimeout: 2m
      resultPollTimeout: 30s
    modelGateways:
      llama-3:
        inferencePoolName: llama-pool
    replicas: 1
  secretRef:
    name: batch-gateway-secrets
status:
  componentStatus:
    apiServer:
      readyReplicas: 1
      replicas: 1
    gc:
      readyReplicas: 1
      replicas: 1
    processor:
      readyReplicas: 1
      replicas: 1
    asyncProcessor:
      readyReplicas: 1
      replicas: 1
  conditions:
  - message: API server: 1/1 replicas ready
    reason: Available
    status: "True"
    type: APIServerAvailable
  - message: Processor: 1/1 replicas ready
    reason: Available
    status: "True"
    type: ProcessorAvailable
  - message: GC: 1/1 replicas ready
    reason: Available
    status: "True"
    type: GCAvailable
  - message: Async processor: 1/1 replicas ready
    reason: Available
    status: "True"
    type: AsyncProcessorAvailable
  - message: All components are fully rolled out
    reason: AllComponentsReady
    status: "True"
    type: Ready
```

## Disable / Remove

```bash
# Disable AI Gateway (removes operators in reverse order)
oc patch datasciencecluster default-dsc --type merge \
    -p '{"spec":{"components":{"aigateway":{"managementState":"Removed"}}}}'

# Delete LLMBatchGateway CR and namespace
oc delete llmbatchgateway batch-gateway -n batch-api
oc delete namespace batch-api
```

## References

- [ai-gateway-operator architecture](https://github.com/opendatahub-io/ai-gateway-operator/blob/main/docs/architecture.md)
- [Development workflow](dev-workflow.md)
