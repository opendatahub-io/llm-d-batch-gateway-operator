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
- `helm` (for installing PostgreSQL and optional Valkey test dependencies)
- `kubectl` (for installing the SeaweedFS test dependency)
- External dependencies for LLMBatchGateway: PostgreSQL and S3-compatible storage. Async dispatch needs a Redis-protocol server such as Valkey for the default sorted-set transport, or PostgreSQL for the experimental SQL transport.

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
uses the Redis sorted-set transport with Valkey, or PostgreSQL SQL queues. Use
the provided script:

```bash
export NAMESPACE=batch-api
export SEAWEEDFS_ACCESS_KEY="batch-gateway-$(openssl rand -hex 16)"
export SEAWEEDFS_SECRET_KEY="$(openssl rand -hex 32)"
INSTALL_VALKEY=true bash hack/setup-prereqs.sh
```

The helper installs Valkey from the official chart at version `1.0.0`, which
pins the Valkey image to `9.1.3`. The Secret key remains `redis-url` because
the async transport uses the Redis protocol.

Use unique values for `SEAWEEDFS_ACCESS_KEY` and `SEAWEEDFS_SECRET_KEY`. The
setup script requires both values and stores the secret key in
`batch-gateway-secrets`. For production, use credentials from a managed Secret
and do not use the local SeaweedFS setup script.


This creates the `$NAMESPACE` namespace and installs:
- PostgreSQL (`postgresql.$NAMESPACE.svc.cluster.local:5432`)
- SeaweedFS S3 (`seaweedfs.$NAMESPACE.svc.cluster.local:8333`, bucket: `batch-gateway`)
- Valkey (`redis.$NAMESPACE.svc.cluster.local:6379`; its Redis-protocol URL is added to the `redis-url` key in `batch-gateway-secrets`)
- `batch-gateway-secrets` Secret

For production, replace PostgreSQL and S3 with your own endpoints, plus a
Redis-compatible server such as Redis or Valkey when you use the Redis async
transport. For the SQL async transport, ensure the
referenced Secret contains `postgresql-url` and configure
`spec.processor.asyncConfig.sql`.

#### Google Cloud Storage

Set exactly one of `spec.fileStorage.s3`, `spec.fileStorage.gcs`, or
`spec.fileStorage.fs`. For GCS, use:

```yaml
spec:
  fileStorage:
    gcs:
      bucket: batch-files
      prefix: batch/
      autoCreateBucket: false
      verifyBucketOnStart: true
```

GCS uses Application Default Credentials or GKE Workload Identity. Do not put a
GCS service-account key in the `LLMBatchGateway` CR or its Secret.

For Workload Identity, bind the Google service account to the Kubernetes
ServiceAccounts used by the API server, processor, and garbage collector, then
annotate those three ServiceAccounts with
`iam.gke.io/gcp-service-account: GSA_EMAIL`. The operator creates them after
the `LLMBatchGateway` CR is applied.

Grant the Google service account these permissions on the bucket:
`storage.objects.create`, `storage.objects.get`, `storage.objects.delete`, and
`storage.objects.list`. Use an IAM condition to limit `storage.objects.list` to
object names that begin with the configured prefix. Use:
`resource.name.startsWith('projects/_/buckets/<bucket>/objects/<prefix>') || api.getAttribute('storage.googleapis.com/objectListPrefix', '').startsWith('<prefix>')`.
With
`verifyBucketOnStart: true`, also grant `storage.buckets.get`. If
`autoCreateBucket: true`, set `spec.fileStorage.gcs.projectId` to the Google
Cloud project that will own the bucket, keep `verifyBucketOnStart: true`, and
grant `storage.buckets.create`.

To cap each shared PostgreSQL pool used by the API server, processor, and GC,
set `spec.dbClient.postgresql.pool.maxConnections`. A positive value overrides
`pool_max_conns` in the PostgreSQL URL; each pool uses at least two connections.
Zero or omission preserves the URL's `pool_max_conns` when set, otherwise the
pgx default. This is separate from
`spec.processor.asyncConfig.sql.maxConnections`, which caps the async SQL queue
pool.

#### API server tenant and proxy token checks

The API server can require a shared token in `X-Gateway-Token` and a tenant
header (`X-MaaS-Username` by default). The operator renders
`spec.apiServer.config.allowAnonymousTenant: true` when the field is unset, to
preserve the API server's previous behavior and keep existing gateways running
after an upgrade. With this setting, the proxy token is not checked; requests
without a tenant header use the `default` tenant, while a supplied tenant
header is still used.

> **Warning:** when these checks are off, the API server trusts the tenant
> header from any client that can reach its Service. Keep it reachable only
> through the authenticated proxy, and have that proxy replace the tenant
> header with the value from the authenticated identity.

To enable both checks, first add a token under the `apiserver-proxy-token` key
in the Secret named by `spec.secretRef`, then configure the auth proxy to send
the same value in `X-Gateway-Token` and set the tenant header from the
authenticated identity. After both are ready, set:

```yaml
spec:
  apiServer:
    config:
      allowAnonymousTenant: false
```

The API server reads the token at startup. Restart it after rotating the token
in the Secret and auth proxy. The local development samples enable anonymous
tenants because they do not set up an auth proxy.

This option requires a Batch Gateway chart revision with API-server
NetworkPolicy support. The operator creates no API-server `NetworkPolicy` unless
`spec.apiServer.networkPolicy.enabled` is `true`; when omitted or false, this
feature does not restrict ingress. When enabled, `api.from` must list allowed
peers. An empty or omitted `observability.from` allows no peers to reach the
observability port. Each peer follows native Kubernetes `NetworkPolicyPeer`
rules.

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
