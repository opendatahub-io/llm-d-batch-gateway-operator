package monitoring_test

import (
	"context"
	"testing"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/opendatahub-io/llm-d-batch-gateway-operator/internal/monitoring"
	"github.com/opendatahub-io/llm-d-batch-gateway-operator/internal/utils"
)

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("adding clientgoscheme: %v", err)
	}
	if err := monitoringv1.AddToScheme(s); err != nil {
		t.Fatalf("adding monitoringv1: %v", err)
	}
	return s
}

func newMetricsController(t *testing.T, namespace string) *monitoring.MetricsController {
	t.Helper()
	c := fake.NewClientBuilder().
		WithScheme(newScheme(t)).
		Build()
	return &monitoring.MetricsController{
		Client:    c,
		Namespace: namespace,
		Recorder:  record.NewFakeRecorder(10),
	}
}

func TestReconcileOperatorMonitoring(t *testing.T) {
	const namespace = "test-ns"
	const httpsPort = "https"
	ctx := context.Background()

	r := newMetricsController(t, namespace)

	if _, err := r.Reconcile(ctx, reconcile.Request{}); err != nil {
		t.Fatalf("Reconcile() error: %v", err)
	}

	t.Run("creates metrics Service", func(t *testing.T) {
		var svc corev1.Service
		if err := r.Get(ctx, types.NamespacedName{Name: utils.OperatorName + "-metrics", Namespace: namespace}, &svc); err != nil {
			t.Fatalf("getting Service: %v", err)
		}
		if len(svc.Spec.Ports) != 1 {
			t.Fatalf("expected 1 port, got %d", len(svc.Spec.Ports))
		}
		if svc.Spec.Ports[0].Name != httpsPort {
			t.Errorf("port name = %q, want %q", svc.Spec.Ports[0].Name, httpsPort)
		}
		if svc.Spec.Ports[0].Port != 8443 {
			t.Errorf("port = %d, want 8443", svc.Spec.Ports[0].Port)
		}
		if svc.Spec.Selector["app.kubernetes.io/name"] != utils.OperatorName {
			t.Errorf("selector app.kubernetes.io/name = %q, want %q", svc.Spec.Selector["app.kubernetes.io/name"], utils.OperatorName)
		}

		wantAnnotation := utils.OperatorName + "-metrics-tls"
		if got := svc.Annotations["service.beta.openshift.io/serving-cert-secret-name"]; got != wantAnnotation {
			t.Errorf("serving-cert annotation = %q, want %q", got, wantAnnotation)
		}
	})

	t.Run("creates ServiceMonitor", func(t *testing.T) {
		var sm monitoringv1.ServiceMonitor
		if err := r.Get(ctx, types.NamespacedName{Name: utils.OperatorName + "-metrics", Namespace: namespace}, &sm); err != nil {
			t.Fatalf("getting ServiceMonitor: %v", err)
		}
		if len(sm.Spec.Endpoints) != 1 {
			t.Fatalf("expected 1 endpoint, got %d", len(sm.Spec.Endpoints))
		}
		ep := sm.Spec.Endpoints[0]
		if ep.Port != httpsPort {
			t.Errorf("endpoint port = %q, want %q", ep.Port, httpsPort)
		}
		if ep.Scheme == nil || ep.Scheme.String() != httpsPort {
			t.Errorf("endpoint scheme = %v, want https", ep.Scheme)
		}
		if ep.BearerTokenFile != "/var/run/secrets/kubernetes.io/serviceaccount/token" { //nolint:staticcheck // testing deprecated field
			t.Errorf("endpoint bearerTokenFile = %q, want SA token path", ep.BearerTokenFile) //nolint:staticcheck // testing deprecated field
		}
		if ep.TLSConfig == nil || ep.TLSConfig.CAFile != "/etc/prometheus/configmaps/serving-certs-ca-bundle/service-ca.crt" {
			caFile := ""
			if ep.TLSConfig != nil {
				caFile = ep.TLSConfig.CAFile
			}
			t.Errorf("endpoint TLSConfig.CAFile = %q, want service-ca.crt path", caFile)
		}
		if sm.Spec.Selector.MatchLabels["app.kubernetes.io/name"] != utils.OperatorName {
			t.Errorf("selector app.kubernetes.io/name = %q, want %q", sm.Spec.Selector.MatchLabels["app.kubernetes.io/name"], utils.OperatorName)
		}
	})

	t.Run("creates PrometheusRule", func(t *testing.T) {
		var pr monitoringv1.PrometheusRule
		if err := r.Get(ctx, types.NamespacedName{Name: utils.OperatorName + "-alerts", Namespace: namespace}, &pr); err != nil {
			t.Fatalf("getting PrometheusRule: %v", err)
		}
		if len(pr.Spec.Groups) != 1 {
			t.Fatalf("expected 1 rule group, got %d", len(pr.Spec.Groups))
		}
		rules := pr.Spec.Groups[0].Rules
		if len(rules) != 2 {
			t.Fatalf("expected 2 rules, got %d", len(rules))
		}
		if rules[0].Alert != "LLMBatchGatewayOperatorHighReconcileErrorRate" {
			t.Errorf("rule[0].Alert = %q, want LLMBatchGatewayOperatorHighReconcileErrorRate", rules[0].Alert)
		}
		if rules[1].Alert != "LLMBatchGatewayOperatorLeaderElectionLost" {
			t.Errorf("rule[1].Alert = %q, want LLMBatchGatewayOperatorLeaderElectionLost", rules[1].Alert)
		}
	})

	t.Run("is idempotent", func(t *testing.T) {
		if _, err := r.Reconcile(ctx, reconcile.Request{}); err != nil {
			t.Fatalf("second Reconcile() error: %v", err)
		}
	})
}
