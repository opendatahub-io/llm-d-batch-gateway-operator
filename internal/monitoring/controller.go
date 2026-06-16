package monitoring

import (
	"context"
	"fmt"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/opendatahub-io/llm-d-batch-gateway-operator/internal/utils"
)

const (
	metricsPort    = "https"
	metricsPortNum = 8443
)

var operatorSelectorLabels = map[string]string{
	"control-plane":          "llm-d-batch-gateway-controller-manager",
	"app.kubernetes.io/name": utils.OperatorName,
}

var operatorLabels = map[string]string{
	"control-plane":                "llm-d-batch-gateway-controller-manager",
	"app.kubernetes.io/name":       utils.OperatorName,
	"app.kubernetes.io/managed-by": utils.FieldOwner,
}

// MetricsController ensures the operator's own metrics Service,
// ServiceMonitor and PrometheusRule are always present and up to date.
// It is triggered by watch events on those resources and re-applies them via SSA.
type MetricsController struct {
	client.Client
	Namespace string
	Recorder  record.EventRecorder
}

var _ reconcile.Reconciler = (*MetricsController)(nil)

func (r *MetricsController) Reconcile(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx)

	if svc, err := r.reconcileMetricsService(ctx); err != nil {
		ReconcileErrors.WithLabelValues("ServiceApplyFailed").Inc()
		r.Recorder.Eventf(svc, corev1.EventTypeWarning, "ReconcileFailed", "Failed to reconcile Service: %s", err)
		logger.Error(err, "failed to reconcile operator metrics Service")
		return reconcile.Result{}, fmt.Errorf("reconciling operator metrics Service: %w", err)
	}
	if sm, err := r.reconcileServiceMonitor(ctx); err != nil {
		ReconcileErrors.WithLabelValues("ServiceMonitorApplyFailed").Inc()
		r.Recorder.Eventf(sm, corev1.EventTypeWarning, "ReconcileFailed", "Failed to reconcile ServiceMonitor: %s", err)
		logger.Error(err, "failed to reconcile operator ServiceMonitor")
		return reconcile.Result{}, fmt.Errorf("reconciling operator ServiceMonitor: %w", err)
	}
	if pr, err := r.reconcilePrometheusRule(ctx); err != nil {
		ReconcileErrors.WithLabelValues("PrometheusRuleApplyFailed").Inc()
		r.Recorder.Eventf(pr, corev1.EventTypeWarning, "ReconcileFailed", "Failed to reconcile PrometheusRule: %s", err)
		logger.Error(err, "failed to reconcile operator PrometheusRule")
		return reconcile.Result{}, fmt.Errorf("reconciling operator PrometheusRule: %w", err)
	}
	return reconcile.Result{}, nil
}

// SetupWithManager registers watches on the operator's own monitoring resources.
// Any change or deletion of these resources triggers a reconcile that re-applies them.
func (r *MetricsController) SetupWithManager(mgr ctrl.Manager) error {
	// Only enqueue a fixed empty request — we always reconcile all three resources together.
	enqueue := handler.EnqueueRequestsFromMapFunc(
		func(_ context.Context, _ client.Object) []reconcile.Request {
			return []reconcile.Request{{}}
		},
	)

	// nameFilter returns a predicate matching only the operator's resource with the given name suffix.
	nameFilter := func(suffix string) predicate.Predicate {
		return predicate.NewPredicateFuncs(func(obj client.Object) bool {
			return obj.GetNamespace() == r.Namespace && obj.GetName() == utils.OperatorName+suffix
		})
	}

	// Enqueue one reconcile on startup to bootstrap resources that don't exist yet.
	bootstrap := source.TypedFunc[reconcile.Request](func(_ context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
		q.Add(reconcile.Request{})
		return nil
	})

	b := ctrl.NewControllerManagedBy(mgr).
		WatchesRawSource(bootstrap).
		Watches(&corev1.Service{}, enqueue, builder.WithPredicates(nameFilter("-metrics"))).
		Named("metrics-controller")

	mapper := mgr.GetRESTMapper()
	scheme := mgr.GetScheme()

	if utils.IsCRDInstalled(mapper, scheme, &monitoringv1.ServiceMonitor{}) {
		b = b.Watches(&monitoringv1.ServiceMonitor{}, enqueue,
			builder.WithPredicates(nameFilter("-metrics")))
	}
	if utils.IsCRDInstalled(mapper, scheme, &monitoringv1.PrometheusRule{}) {
		b = b.Watches(&monitoringv1.PrometheusRule{}, enqueue,
			builder.WithPredicates(nameFilter("-alerts")))
	}

	return b.Complete(r)
}

func (r *MetricsController) reconcileMetricsService(ctx context.Context) (*corev1.Service, error) {
	svc := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      utils.OperatorName + "-metrics",
			Namespace: r.Namespace,
			Labels:    operatorLabels,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:       metricsPort,
					Port:       metricsPortNum,
					Protocol:   corev1.ProtocolTCP,
					TargetPort: intstr.FromInt32(metricsPortNum),
				},
			},
			Selector: operatorSelectorLabels,
		},
	}

	return svc, utils.ServerSideApply(ctx, r.Client, svc)
}

func (r *MetricsController) reconcileServiceMonitor(ctx context.Context) (*monitoringv1.ServiceMonitor, error) {
	sm := &monitoringv1.ServiceMonitor{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "monitoring.coreos.com/v1",
			Kind:       "ServiceMonitor",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      utils.OperatorName + "-metrics",
			Namespace: r.Namespace,
			Labels:    operatorLabels,
		},
		Spec: monitoringv1.ServiceMonitorSpec{
			Selector: metav1.LabelSelector{
				MatchLabels: operatorSelectorLabels,
			},
			Endpoints: []monitoringv1.Endpoint{
				{
					Port: metricsPort,
					Path: "/metrics",
				},
			},
		},
	}

	return sm, utils.ServerSideApply(ctx, r.Client, sm)
}

func (r *MetricsController) reconcilePrometheusRule(ctx context.Context) (*monitoringv1.PrometheusRule, error) {
	alertFor := monitoringv1.Duration("5m")
	pr := &monitoringv1.PrometheusRule{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "monitoring.coreos.com/v1",
			Kind:       "PrometheusRule",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      utils.OperatorName + "-alerts",
			Namespace: r.Namespace,
			Labels:    operatorLabels,
		},
		Spec: monitoringv1.PrometheusRuleSpec{
			Groups: []monitoringv1.RuleGroup{
				{
					Name: "llmbatchgateway.operator.rules",
					Rules: []monitoringv1.Rule{
						{
							Alert: "LLMBatchGatewayOperatorHighReconcileErrorRate",
							Expr: intstr.FromString(
								`rate(llmbatchgateway_controller_reconcile_errors_total[5m]) > 0.1`,
							),
							For: &alertFor,
							Labels: map[string]string{
								"severity": "warning",
							},
							Annotations: map[string]string{
								"summary":     "LLMBatchGateway operator reconcile error rate is high",
								"description": "The operator has been experiencing more than 0.1 reconcile errors per second for 5 minutes.",
							},
						},
						{
							Alert: "LLMBatchGatewayOperatorLeaderElectionLost",
							Expr: intstr.FromString(fmt.Sprintf(
								`leader_election_master_status{name="%s",namespace="%s"} == 0`,
								utils.LeaderElectionID,
								r.Namespace,
							)),
							For: &alertFor,
							Labels: map[string]string{
								"severity": "critical",
							},
							Annotations: map[string]string{
								"summary":     "LLMBatchGateway operator has lost leader election",
								"description": fmt.Sprintf("The operator in namespace %s has lost leader election and is not reconciling.", r.Namespace),
							},
						},
					},
				},
			},
		},
	}

	return pr, utils.ServerSideApply(ctx, r.Client, pr)
}
