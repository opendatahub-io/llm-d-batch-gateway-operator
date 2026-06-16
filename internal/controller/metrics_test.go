package controller

import (
	"context"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	batchv1alpha1 "github.com/opendatahub-io/llm-d-batch-gateway-operator/api/v1alpha1"
	"github.com/opendatahub-io/llm-d-batch-gateway-operator/internal/monitoring"
)

func TestReconcileErrorMetricIncrement(t *testing.T) {
	ctx := context.Background()

	helmRenderer, err := NewHelmRenderer("../../batch-gateway/charts/batch-gateway", testImages())
	if err != nil {
		t.Fatalf("NewHelmRenderer() error: %v", err)
	}
	reconciler := NewLLMBatchGatewayReconciler(k8sClient, k8sClient.Scheme(), helmRenderer, record.NewFakeRecorder(10), 5*time.Minute, 30*time.Second)

	// Create a ReferenceGrant permitting access to the secret namespace but do NOT
	// create the actual secret — resolveSecret will fail with a transient (non-nil)
	// error causing Reconcile to return an error and increment the counter.
	const (
		gwName     = "test-metric-increment"
		secretNS   = "metric-test-ns"
		secretName = "missing-secret"
	)

	grant := &gatewayv1beta1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "grant-metric-test",
			Namespace: secretNS,
		},
		Spec: gatewayv1beta1.ReferenceGrantSpec{
			From: []gatewayv1beta1.ReferenceGrantFrom{{
				Group:     gatewayv1beta1.Group(batchv1alpha1.GroupVersion.Group),
				Kind:      llmBatchGatewayKind,
				Namespace: gatewayv1beta1.Namespace("default"),
			}},
			To: []gatewayv1beta1.ReferenceGrantTo{{
				Group: "",
				Kind:  secretKind,
				Name:  (*gatewayv1beta1.ObjectName)(&[]string{secretName}[0]),
			}},
		},
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: secretNS}}
	if err := k8sClient.Create(ctx, ns); err != nil {
		t.Fatalf("creating namespace: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, ns) })

	if err := k8sClient.Create(ctx, grant); err != nil {
		t.Fatalf("creating ReferenceGrant: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, grant) })

	gw := newTestGateway(gwName)
	gw.Spec.SecretRef = corev1.SecretReference{Name: secretName, Namespace: secretNS}
	if err := k8sClient.Create(ctx, gw); err != nil {
		t.Fatalf("creating CR: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, gw) })

	before := counterValue(t, "Other")

	_, err = reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: gwName, Namespace: "default"},
	})
	if err == nil {
		t.Fatal("expected Reconcile to return an error, got nil")
	}

	after := counterValue(t, "Other")
	if after != before+1 {
		t.Errorf("reconcile error counter: before=%v after=%v, want increment of 1", before, after)
	}
}

// counterValue reads the current value of the reconcile_errors_total counter for the given reason label.
func counterValue(t *testing.T, reason string) float64 {
	t.Helper()
	counter, err := monitoring.ReconcileErrors.GetMetricWithLabelValues(reason)
	if err != nil {
		t.Fatalf("getting metric with label %q: %v", reason, err)
	}
	var m dto.Metric
	if err := counter.Write(&m); err != nil {
		t.Fatalf("reading metric: %v", err)
	}
	return m.GetCounter().GetValue()
}
