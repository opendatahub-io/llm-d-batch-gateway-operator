package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// AsyncProcessorSpec configures the optional async-processor component that dispatches
// batch inference requests via a message queue to inference gateways.
// Deployment-level fields (replicas, resources, imagePullPolicy) are typed so the
// operator can manage the Deployment directly. All application-level configuration
// for the llm-d-async chart is passed through as an opaque JSON/YAML blob in Values,
// decoupling the CRD schema from the upstream chart's evolving config surface.
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

	// Values are raw Helm values passed through to the llm-d-async chart.
	// The operator does not interpret or validate this content — it is merged
	// directly into the chart's values map under the "ap" key.
	// See the llm-d-async chart's values.yaml for the full schema.
	// +optional
	Values *runtime.RawExtension `json:"values,omitempty"`
}
