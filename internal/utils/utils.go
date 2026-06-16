package utils

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/json"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ServerSideApply marshals obj to JSON and applies it via server-side apply.
func ServerSideApply(ctx context.Context, c client.Client, obj client.Object) error {
	data, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("marshalling %s %s: %w", obj.GetObjectKind().GroupVersionKind().Kind, obj.GetName(), err)
	}
	return c.Patch(ctx, obj, client.RawPatch(types.ApplyPatchType, data), client.FieldOwner(FieldOwner), client.ForceOwnership)
}

// IsCRDInstalled reports whether the CRD for obj is registered in the cluster.
// Any mapper error is treated as "not installed" so that optional watches are
// skipped rather than preventing the operator from starting.
func IsCRDInstalled(mapper meta.RESTMapper, scheme *runtime.Scheme, obj client.Object) bool {
	gvks, _, err := scheme.ObjectKinds(obj)
	if err != nil || len(gvks) == 0 {
		return false
	}
	_, err = mapper.RESTMapping(gvks[0].GroupKind(), gvks[0].Version)
	return err == nil
}

const (
	// OperatorName is the app.kubernetes.io/name label value for the operator.
	OperatorName = "llm-d-batch-gateway-operator"

	// FieldOwner is the server-side apply field manager name used by the operator.
	FieldOwner = "llmbatchgateway-controller"

	// LeaderElectionID is the resource name used for leader election.
	LeaderElectionID = "llmbatchgateway.batch.llm-d.ai"
)
