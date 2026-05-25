package monitoring

import (
	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var ReconcileErrors = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Namespace: "llmbatchgateway",
		Subsystem: "controller",
		Name:      "reconcile_errors_total",
		Help:      "Total number of reconciliation errors, partitioned by error type.",
	},
	[]string{"reason"},
)

func init() {
	ctrlmetrics.Registry.MustRegister(ReconcileErrors)
}
