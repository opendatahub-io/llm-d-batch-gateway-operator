package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"

	batchv1alpha1 "github.com/opendatahub-io/llm-d-batch-gateway-operator/api/v1alpha1"
	"github.com/opendatahub-io/llm-d-batch-gateway-operator/internal/controller"
	"github.com/opendatahub-io/llm-d-batch-gateway-operator/internal/monitoring"
	"github.com/opendatahub-io/llm-d-batch-gateway-operator/internal/utils"
)

// version is stamped at build time via -ldflags "-X main.version=<version>".
// It defaults to "dev" when built without the flag (e.g. go run).
var version = "dev"

// the way for batch-gateway-operator to know which exactly are the 3 images are via env variable set in the deployment
// since it cannot read params.env which is updated by opendatahub-operator
const (
	envImageAPIServer = "LLM_D_BATCH_GATEWAY_APISERVER_IMAGE"
	envImageProcessor = "LLM_D_BATCH_GATEWAY_PROCESSOR_IMAGE"
	envImageGC        = "LLM_D_BATCH_GATEWAY_GC_IMAGE"
)

// componentImagesFromEnv reads the pinned component images from the environment
// and fails if any are missing, since the operator cannot render workloads
// without them.
func componentImagesFromEnv() (controller.ComponentImages, error) {
	images := controller.ComponentImages{
		APIServer: os.Getenv(envImageAPIServer),
		Processor: os.Getenv(envImageProcessor),
		GC:        os.Getenv(envImageGC),
	}

	var missing []string
	if images.APIServer == "" {
		missing = append(missing, envImageAPIServer)
	}
	if images.Processor == "" {
		missing = append(missing, envImageProcessor)
	}
	if images.GC == "" {
		missing = append(missing, envImageGC)
	}
	if len(missing) > 0 {
		return controller.ComponentImages{}, fmt.Errorf("required image environment variables are not set: %s", strings.Join(missing, ", "))
	}

	return images, nil
}

// +kubebuilder:rbac:groups="",resources=services,verbs=get;create;update;patch
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=servicemonitors;prometheusrules,verbs=get;create;update;patch

var (
	scheme                  = runtime.NewScheme()
	syncPeriodDefault       = 5 * time.Minute
	reconcileTimeoutDefault = 30 * time.Second
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(batchv1alpha1.AddToScheme(scheme))
	utilruntime.Must(gatewayv1.Install(scheme))
	utilruntime.Must(gatewayv1beta1.Install(scheme))
	utilruntime.Must(monitoringv1.AddToScheme(scheme))
}

func main() {
	var chartPath string
	var metricsAddr string
	var probeAddr string
	var enableLeaderElection bool
	var syncPeriod time.Duration
	var reconcileTimeout time.Duration

	flag.StringVar(&chartPath, "chart-path", "/charts/batch-gateway", "Path to the batch-gateway Helm chart directory")
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8443", "Address the metrics endpoint binds to")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Address the health probe endpoint binds to")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election for controller manager")
	flag.DurationVar(&syncPeriod, "sync-period", syncPeriodDefault, "How often to re-sync all LLMBatchGateway resources to catch out-of-band drift")
	flag.DurationVar(&reconcileTimeout, "reconcile-timeout", reconcileTimeoutDefault, "Maximum duration for a single reconcile")

	klog.InitFlags(nil)
	flag.Parse()

	logger := klog.NewKlogr()
	ctrl.SetLogger(logger)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       utils.LeaderElectionID,
	})
	if err != nil {
		logger.Error(err, "unable to create manager")
		os.Exit(1)
	}

	images, err := componentImagesFromEnv()
	if err != nil {
		logger.Error(err, "unable to resolve 3 component images from env variables")
		os.Exit(1)
	}

	helmRenderer, err := controller.NewHelmRenderer(chartPath, images)
	if err != nil {
		logger.Error(err, "unable to create helm renderer", "chartPath", chartPath)
		os.Exit(1)
	}

	recorder := mgr.GetEventRecorderFor("llmbatchgateway-controller") //nolint:staticcheck

	if err := controller.NewLLMBatchGatewayReconciler(mgr.GetClient(), mgr.GetScheme(), helmRenderer, recorder, syncPeriod, reconcileTimeout).SetupWithManager(mgr); err != nil {
		logger.Error(err, "unable to create controller", "controller", "LLMBatchGateway")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		logger.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	operatorNamespace := os.Getenv("POD_NAMESPACE")
	if operatorNamespace != "" {
		metricsController := &monitoring.MetricsController{
			Client:    mgr.GetClient(),
			Namespace: operatorNamespace,
			Recorder:  recorder,
		}
		if err := metricsController.SetupWithManager(mgr); err != nil {
			logger.Error(err, "unable to create controller", "controller", "MetricsController")
			os.Exit(1)
		}
	} else {
		logger.Info("POD_NAMESPACE not set, skipping metrics controller reconciliation")
	}

	logger.Info("starting manager", "version", version)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error(err, "problem running manager")
		os.Exit(1)
	}
}
