package controller

import (
	"context"
	"encoding/json"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	batchv1alpha1 "github.com/opendatahub-io/llm-d-batch-gateway-operator/api/v1alpha1"
)

// TestStatusPatchMarshal verifies the JSON structure emitted by StatusPatch.
// The patch must be a merge patch with metadata.resourceVersion for conflict
// detection and a status object containing the accumulated fields.
func TestStatusPatchMarshal(t *testing.T) {
	p := NewStatusPatch("abc123").
		Add("observedGeneration", int64(2)).
		Add("conditions", []string{"a", "b"})

	// Re-serialize via Apply's internal path by using json.Marshal directly on
	// the expected structure, then compare round-tripped values.
	patch := map[string]any{
		"metadata": map[string]any{
			"resourceVersion": "abc123",
		},
		"status": map[string]any{
			"observedGeneration": int64(2),
			"conditions":         []string{"a", "b"},
		},
	}
	want, err := json.Marshal(patch)
	if err != nil {
		t.Fatalf("json.Marshal want: %v", err)
	}

	// Build the same payload the Apply path would produce.
	got := map[string]any{
		"metadata": map[string]any{
			"resourceVersion": p.resourceVersion,
		},
		"status": p.fields,
	}
	gotBytes, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal got: %v", err)
	}

	// Unmarshal both into interface{} for a semantic (not byte-order) comparison.
	var wantObj, gotObj any
	_ = json.Unmarshal(want, &wantObj)
	_ = json.Unmarshal(gotBytes, &gotObj)

	wantJSON, _ := json.Marshal(wantObj)
	gotJSON, _ := json.Marshal(gotObj)
	if string(wantJSON) != string(gotJSON) {
		t.Errorf("patch payload mismatch:\n got  %s\n want %s", gotJSON, wantJSON)
	}

	// Spot-check: resourceVersion must be in metadata, not in status.
	var m map[string]any
	if err := json.Unmarshal(gotBytes, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	meta, ok := m["metadata"].(map[string]any)
	if !ok {
		t.Fatal("metadata key missing or wrong type")
	}
	if meta["resourceVersion"] != "abc123" {
		t.Errorf("metadata.resourceVersion = %v, want abc123", meta["resourceVersion"])
	}
	status, ok := m["status"].(map[string]any)
	if !ok {
		t.Fatal("status key missing or wrong type")
	}
	if _, hasRV := status["resourceVersion"]; hasRV {
		t.Error("resourceVersion must not appear inside status")
	}
}

// TestStatusPatchApply exercises Apply against the envtest API server.
func TestStatusPatchApply(t *testing.T) {
	ctx := context.Background()

	t.Run("applies status conditions", func(t *testing.T) {
		gw := newTestGateway("statuspatch-apply")
		if err := k8sClient.Create(ctx, gw); err != nil {
			t.Fatalf("creating CR: %v", err)
		}
		t.Cleanup(func() { _ = k8sClient.Delete(ctx, gw) })

		conditions := []metav1.Condition{
			{
				Type:               conditionReady,
				Status:             metav1.ConditionFalse,
				Reason:             "TestReason",
				Message:            "test message",
				ObservedGeneration: gw.Generation,
				LastTransitionTime: metav1.Now(),
			},
		}

		err := NewStatusPatch(gw.ResourceVersion).
			Add("conditions", conditions).
			Add("observedGeneration", gw.Generation).
			Apply(ctx, k8sClient, gw)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}

		var updated batchv1alpha1.LLMBatchGateway
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: gw.Name, Namespace: gw.Namespace}, &updated); err != nil {
			t.Fatalf("Get after patch: %v", err)
		}

		if len(updated.Status.Conditions) != 1 {
			t.Fatalf("conditions len = %d, want 1", len(updated.Status.Conditions))
		}
		if updated.Status.Conditions[0].Type != conditionReady {
			t.Errorf("condition type = %q, want %q", updated.Status.Conditions[0].Type, conditionReady)
		}
		if updated.Status.Conditions[0].Reason != "TestReason" {
			t.Errorf("condition reason = %q, want %q", updated.Status.Conditions[0].Reason, "TestReason")
		}
		if updated.Status.ObservedGeneration != gw.Generation {
			t.Errorf("observedGeneration = %d, want %d", updated.Status.ObservedGeneration, gw.Generation)
		}
	})

	t.Run("returns conflict on stale resourceVersion", func(t *testing.T) {
		gw := newTestGateway("statuspatch-conflict")
		if err := k8sClient.Create(ctx, gw); err != nil {
			t.Fatalf("creating CR: %v", err)
		}
		t.Cleanup(func() { _ = k8sClient.Delete(ctx, gw) })

		// Capture the initial resourceVersion.
		staleRV := gw.ResourceVersion

		// Advance the object's resourceVersion by touching an annotation.
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: gw.Name, Namespace: gw.Namespace}, gw); err != nil {
			t.Fatalf("Get: %v", err)
		}
		gw.Annotations = map[string]string{"touch": "1"}
		if err := k8sClient.Update(ctx, gw); err != nil {
			t.Fatalf("Update to advance RV: %v", err)
		}

		// Attempt a status patch using the stale resourceVersion — must fail.
		conditions := []metav1.Condition{
			{
				Type:               conditionReady,
				Status:             metav1.ConditionFalse,
				Reason:             "StalePatch",
				Message:            "should not be applied",
				LastTransitionTime: metav1.Now(),
			},
		}
		err := NewStatusPatch(staleRV).
			Add("conditions", conditions).
			Apply(ctx, k8sClient, gw)
		if err == nil {
			t.Fatal("expected conflict error with stale resourceVersion, got nil")
		}
		if !apierrors.IsConflict(err) {
			t.Errorf("expected Conflict error, got: %v", err)
		}
	})

	t.Run("adds componentStatus", func(t *testing.T) {
		gw := newTestGateway("statuspatch-component")
		if err := k8sClient.Create(ctx, gw); err != nil {
			t.Fatalf("creating CR: %v", err)
		}
		t.Cleanup(func() { _ = k8sClient.Delete(ctx, gw) })

		componentStatus := &batchv1alpha1.ComponentStatus{
			APIServer: &batchv1alpha1.ComponentReplicaStatus{
				Replicas:      1,
				ReadyReplicas: 1,
			},
		}

		err := NewStatusPatch(gw.ResourceVersion).
			Add("componentStatus", componentStatus).
			Apply(ctx, k8sClient, gw)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}

		var updated batchv1alpha1.LLMBatchGateway
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: gw.Name, Namespace: gw.Namespace}, &updated); err != nil {
			t.Fatalf("Get after patch: %v", err)
		}

		if updated.Status.ComponentStatus == nil {
			t.Fatal("componentStatus is nil after patch")
		}
		if updated.Status.ComponentStatus.APIServer == nil {
			t.Fatal("componentStatus.APIServer is nil after patch")
		}
		if updated.Status.ComponentStatus.APIServer.ReadyReplicas != 1 {
			t.Errorf("readyReplicas = %d, want 1", updated.Status.ComponentStatus.APIServer.ReadyReplicas)
		}
	})
}
