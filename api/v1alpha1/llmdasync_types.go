package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
)

// --- Async Processor ---

// AsyncProcessorSpec configures the optional async-processor component that dispatches
// batch inference requests via a message queue (Redis or GCP Pub/Sub) to
// inference gateways. When set on LLMBatchGatewaySpec, the controller deploys
// an additional async-processor Deployment alongside the core components.
// +kubebuilder:validation:XValidation:rule="!(has(self.redis) && has(self.gcpPubSub))",message="redis and gcpPubSub are mutually exclusive"
type AsyncProcessorSpec struct {
	// Replicas is the desired number of async-processor pods.
	// Setting this to 0 suspends the async-processor; the Ready condition will be False.
	// NOTE: the upstream async-processor chart currently hardcodes replicas to 1;
	// this field will take effect once the upstream chart templates it from values.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	Replicas *int32 `json:"replicas,omitempty"`

	// Resources defines CPU and memory requests/limits for the async-processor container.
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// ImagePullPolicy overrides the image pull policy for the async-processor container.
	// +kubebuilder:validation:Enum=Always;Never;IfNotPresent
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	// InferenceGateway configures the connection to the inference gateway
	// EPP (EndpointPicker) service where inference requests are sent.
	InferenceGateway *InferenceGatewaySpec `json:"inferenceGateway,omitempty"`

	// Concurrency is the number of concurrent inference requests the processor dispatches.
	// +kubebuilder:default=8
	// +kubebuilder:validation:Minimum=1
	Concurrency *int32 `json:"concurrency,omitempty"`

	// DrainTimeout is the maximum time to wait for in-flight requests during shutdown (e.g. "2m").
	// +kubebuilder:default="2m"
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ms|s|m|h))+$`
	DrainTimeout string `json:"drainTimeout,omitempty"`

	// MessageQueueImpl selects the message queue implementation.
	// When empty, auto-selection occurs based on which backend is enabled.
	// Valid values: "redis-pubsub", "redis-sortedset", "gcp-pubsub", "gcp-pubsub-gated".
	// +kubebuilder:validation:Enum="";redis-pubsub;redis-sortedset;gcp-pubsub;gcp-pubsub-gated
	MessageQueueImpl string `json:"messageQueueImpl,omitempty"`

	// Redis configures the Redis message queue backend.
	// Mutually exclusive with GCPPubSub.
	Redis *AsyncRedisSpec `json:"redis,omitempty"`

	// GCPPubSub configures the GCP Pub/Sub message queue backend.
	// Mutually exclusive with Redis.
	GCPPubSub *AsyncGCPPubSubSpec `json:"gcpPubSub,omitempty"`

	// WorkerPools defines named worker pool configurations.
	// +optional
	WorkerPools []AsyncWorkerPool `json:"workerPools,omitempty"`

	// PrometheusURL is the URL of a Prometheus instance for gate metrics queries.
	// +kubebuilder:validation:MaxLength=2048
	PrometheusURL string `json:"prometheusURL,omitempty"`

	// PrometheusCacheTTL is the cache duration for Prometheus query results (e.g. "30s").
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ms|s|m|h))+$`
	PrometheusCacheTTL string `json:"prometheusCacheTTL,omitempty"`

	// TLS configures TLS for connections to inference gateways.
	TLS *AsyncTLSSpec `json:"tls,omitempty"`

	// TransformConfig modifies request bodies before forwarding to the inference gateway.
	// +optional
	TransformConfig *AsyncTransformConfig `json:"transformConfig,omitempty"`

	// ModelServerMonitor configs a PodMonitor that scrapes model server metrics.
	ModelServerMonitor *AsyncModelServerMonitorSpec `json:"modelServerMonitor,omitempty"`
}

// AsyncRedisSpec configures the Redis message queue backend for the async-processor.
// Presence of this field (non-nil pointer) enables Redis as the message queue backend.
type AsyncRedisSpec struct {
	// RequestPathURL is the path appended to the inference gateway URL for requests.
	// +kubebuilder:default="/v1/completions"
	// +kubebuilder:validation:MaxLength=2048
	RequestPathURL string `json:"requestPathURL,omitempty"`

	// PollIntervalMs is the polling interval in milliseconds for the Redis queue.
	// +kubebuilder:default=1000
	// +kubebuilder:validation:Minimum=100
	PollIntervalMs *int32 `json:"pollIntervalMs,omitempty"`

	// BatchSize is the number of requests to dequeue per poll cycle.
	// +kubebuilder:default=10
	// +kubebuilder:validation:Minimum=1
	BatchSize *int32 `json:"batchSize,omitempty"`

	// GateType specifies the dispatch gating mechanism (e.g. "prometheus-saturation", "redis-quota").
	// +kubebuilder:validation:MaxLength=253
	GateType string `json:"gateType,omitempty"`

	// GateParams provides parameters for the gating mechanism.
	GateParams map[string]string `json:"gateParams,omitempty"`

	// QueuesConfig is a list of per-queue configurations.
	// When set, overrides the single-queue settings (RequestPathURL, GateType, etc.).
	// +optional
	QueuesConfig []AsyncQueueConfig `json:"queuesConfig,omitempty"`
}

// AsyncQueueConfig configures a single Redis queue for the async-processor.
type AsyncQueueConfig struct {
	// Name identifies this queue configuration.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// IGWBaseURL overrides the top-level asyncConfig.inferenceGateway URL for this queue.
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^https?://.+$`
	IGWBaseURL string `json:"igwBaseURL,omitempty"`

	// RequestPathURL overrides the request path for this queue.
	// +kubebuilder:validation:MaxLength=2048
	RequestPathURL string `json:"requestPathURL,omitempty"`

	// GateType overrides the gating mechanism for this queue.
	// +kubebuilder:validation:MaxLength=253
	GateType string `json:"gateType,omitempty"`

	// GateParams overrides the gate parameters for this queue.
	GateParams map[string]string `json:"gateParams,omitempty"`
}

// AsyncGCPPubSubSpec configures the GCP Pub/Sub message queue backend for the async-processor.
// Presence of this field (non-nil pointer) enables GCP Pub/Sub as the message queue backend.
type AsyncGCPPubSubSpec struct {
	// ProjectID is the GCP project ID. Required when enabled.
	// +kubebuilder:validation:MaxLength=253
	ProjectID string `json:"projectId,omitempty"`

	// RequestSubscriberID is the Pub/Sub subscription ID for incoming requests.
	// +kubebuilder:validation:MaxLength=253
	RequestSubscriberID string `json:"requestSubscriberId,omitempty"`

	// ResultTopicID is the Pub/Sub topic ID for publishing results.
	// +kubebuilder:validation:MaxLength=253
	ResultTopicID string `json:"resultTopicId,omitempty"`

	// RequestPathURL is the path appended to the inference gateway URL for requests.
	// +kubebuilder:default="/v1/completions"
	// +kubebuilder:validation:MaxLength=2048
	RequestPathURL string `json:"requestPathURL,omitempty"`

	// TopicsConfig is a list of per-topic configurations.
	// When set, overrides the single-topic settings.
	// +optional
	TopicsConfig []AsyncTopicConfig `json:"topicsConfig,omitempty"`
}

// AsyncTopicConfig configures a single GCP Pub/Sub topic for the async-processor.
type AsyncTopicConfig struct {
	// SubscriptionID is the Pub/Sub subscription for this topic.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	SubscriptionID string `json:"subscriptionId"`

	// IGWBaseURL overrides the global inference gateway URL for this topic.
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^https?://.+$`
	IGWBaseURL string `json:"igwBaseURL,omitempty"`

	// RequestPathURL overrides the request path for this topic.
	// +kubebuilder:validation:MaxLength=2048
	RequestPathURL string `json:"requestPathURL,omitempty"`

	// ResultTopicID overrides the result topic for this topic.
	// +kubebuilder:validation:MaxLength=253
	ResultTopicID string `json:"resultTopicId,omitempty"`

	// GateType specifies the gating mechanism for this topic.
	// +kubebuilder:validation:MaxLength=253
	GateType string `json:"gateType,omitempty"`

	// GateParams provides parameters for the gating mechanism of this topic.
	GateParams map[string]string `json:"gateParams,omitempty"`
}

type AsyncTLSSpec struct {
	// +kubebuilder:validation:MaxLength=253
	SecretName string `json:"secretName,omitempty"`

	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`

	// +kubebuilder:validation:MaxLength=253
	CACertKey string `json:"caCertKey,omitempty"`

	// +kubebuilder:validation:MaxLength=253
	CertKey string `json:"certKey,omitempty"`

	// KeyKey is the key in the Secret holding the private key.
	// +kubebuilder:validation:MaxLength=253
	KeyKey string `json:"keyKey,omitempty"`
}

type AsyncTransformConfig struct {
	// +optional
	RequestTransforms []AsyncRequestTransform `json:"requestTransforms,omitempty"`
}

type AsyncRequestTransform struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	Type string `json:"type"`

	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`
}

type AsyncModelServerMonitorSpec struct {
	Enabled bool `json:"enabled,omitempty"`

	// +optional
	Selector map[string]string `json:"selector,omitempty"`

	// +kubebuilder:default="modelserver"
	// +kubebuilder:validation:MaxLength=253
	Port string `json:"port,omitempty"`

	// +kubebuilder:default="/metrics"
	// +kubebuilder:validation:MaxLength=2048
	Path string `json:"path,omitempty"`

	// +kubebuilder:default="15s"
	// +kubebuilder:validation:Pattern=`^([0-9]+(\.[0-9]+)?(ms|s|m|h))+$`
	Interval string `json:"interval,omitempty"`
}

// AsyncWorkerPool defines a named worker pool configuration for the async-processor.
type AsyncWorkerPool struct {
	// Name identifies this worker pool.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Workers is the number of concurrent workers in this pool.
	// +kubebuilder:validation:Minimum=1
	Workers int32 `json:"workers,omitempty"`

	// GateType specifies the dispatch gating mechanism for this pool
	// (e.g. "prometheus-saturation", "prometheus-budget", "redis-quota").
	// +kubebuilder:validation:MaxLength=253
	GateType string `json:"gateType,omitempty"`

	// GateParams provides parameters for the gating mechanism.
	GateParams map[string]string `json:"gateParams,omitempty"`
}
