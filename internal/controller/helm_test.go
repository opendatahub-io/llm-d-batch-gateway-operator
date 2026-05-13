package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	batchv1alpha1 "github.com/opendatahub-io/llm-d-batch-gateway-operator/api/v1alpha1"
)

func TestSplitImage(t *testing.T) {
	tests := []struct {
		name     string
		image    string
		wantRepo string
		wantTag  string
	}{
		{
			name:     "standard image with tag",
			image:    "ghcr.io/llm-d-incubation/batch-gateway-apiserver:v0.1.0",
			wantRepo: "ghcr.io/llm-d-incubation/batch-gateway-apiserver",
			wantTag:  "v0.1.0",
		},
		{
			name:     "image with latest tag",
			image:    "ghcr.io/llm-d-incubation/batch-gateway-apiserver:latest",
			wantRepo: "ghcr.io/llm-d-incubation/batch-gateway-apiserver",
			wantTag:  "latest",
		},
		{
			name:     "image without tag",
			image:    "ghcr.io/llm-d-incubation/batch-gateway-apiserver",
			wantRepo: "ghcr.io/llm-d-incubation/batch-gateway-apiserver",
			wantTag:  "latest",
		},
		{
			name:     "image with registry port and tag",
			image:    "registry.example.com:5000/batch-gateway-apiserver:v1.0",
			wantRepo: "registry.example.com:5000/batch-gateway-apiserver",
			wantTag:  "v1.0",
		},
		{
			name:     "image with registry port no tag",
			image:    "registry.example.com:5000/batch-gateway-apiserver",
			wantRepo: "registry.example.com:5000/batch-gateway-apiserver",
			wantTag:  "latest",
		},
		{
			name:     "image with sha tag prefix",
			image:    "ghcr.io/llm-d-incubation/batch-gateway-apiserver:sha-abc123",
			wantRepo: "ghcr.io/llm-d-incubation/batch-gateway-apiserver",
			wantTag:  "sha-abc123",
		},
		{
			name:     "digest splits so chart reconstructs repo@sha256:hex correctly",
			image:    "ghcr.io/llm-d-incubation/batch-gateway-apiserver@sha256:abc123def456",
			wantRepo: "ghcr.io/llm-d-incubation/batch-gateway-apiserver@sha256",
			wantTag:  "abc123def456",
		},
		{
			name:     "digest with registry port",
			image:    "registry.example.com:5000/batch-gateway-apiserver@sha256:abc123def456",
			wantRepo: "registry.example.com:5000/batch-gateway-apiserver@sha256",
			wantTag:  "abc123def456",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, tag := splitImage(tt.image)
			if repo != tt.wantRepo {
				t.Errorf("repo = %q, want %q", repo, tt.wantRepo)
			}
			if tag != tt.wantTag {
				t.Errorf("tag = %q, want %q", tag, tt.wantTag)
			}
		})
	}
}

func TestSpecToHelmValues(t *testing.T) {
	replicas := int32(2)
	gw := &batchv1alpha1.LLMBatchGateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-gw",
			Namespace: "test-ns",
		},
		Spec: batchv1alpha1.LLMBatchGatewaySpec{
			SecretRef: batchv1alpha1.SecretReference{
				Name: "my-secret",
			},
			DBBackend: "postgresql",
			FileStorage: &batchv1alpha1.FileStorageSpec{
				S3: &batchv1alpha1.S3StorageSpec{
					Region:   "us-east-1",
					Endpoint: "https://s3.example.com",
				},
			},
			APIServer: batchv1alpha1.APIServerSpec{
				Replicas: &replicas,
				Image:    "ghcr.io/llm-d-incubation/batch-gateway-apiserver:v0.1.0",
				Resources: &corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1000m"),
						corev1.ResourceMemory: resource.MustParse("512Mi"),
					},
				},
			},
			Processor: batchv1alpha1.ProcessorSpec{
				Replicas: &replicas,
				Image:    "ghcr.io/llm-d-incubation/batch-gateway-processor:v0.1.0",
				GlobalInferenceGateway: &batchv1alpha1.InferenceGatewaySpec{
					URL:            "http://inference-gateway:8000",
					RequestTimeout: "5m",
				},
			},
			GC: batchv1alpha1.GCSpec{
				Image:    "ghcr.io/llm-d-incubation/batch-gateway-gc:v0.1.0",
				Interval: "30m",
			},
		},
	}

	vals, err := specToHelmValues(gw)
	if err != nil {
		t.Fatalf("specToHelmValues() error: %v", err)
	}

	t.Run("global secret name", func(t *testing.T) {
		global, ok := vals["global"].(map[string]interface{})
		if !ok {
			t.Fatal("global not found")
		}
		if got := global["secretName"]; got != "my-secret" {
			t.Errorf("secretName = %v, want %q", got, "my-secret")
		}
	})

	t.Run("global db client type", func(t *testing.T) {
		global := vals["global"].(map[string]interface{})
		dbClient := global["dbClient"].(map[string]interface{})
		if got := dbClient["type"]; got != "postgresql" {
			t.Errorf("dbClient.type = %v, want %q", got, "postgresql")
		}
	})

	t.Run("file storage s3", func(t *testing.T) {
		global := vals["global"].(map[string]interface{})
		fc := global["fileClient"].(map[string]interface{})
		if got := fc["type"]; got != "s3" {
			t.Errorf("fileClient.type = %v, want %q", got, "s3")
		}
		s3 := fc["s3"].(map[string]interface{})
		if got := s3["region"]; got != "us-east-1" {
			t.Errorf("s3.region = %v, want %q", got, "us-east-1")
		}
	})

	t.Run("apiserver image split", func(t *testing.T) {
		apiserver := vals["apiserver"].(map[string]interface{})
		img := apiserver["image"].(map[string]interface{})
		if got := img["repository"]; got != "ghcr.io/llm-d-incubation/batch-gateway-apiserver" {
			t.Errorf("image.repository = %v", got)
		}
		if got := img["tag"]; got != "v0.1.0" {
			t.Errorf("image.tag = %v", got)
		}
	})

	t.Run("apiserver replicas", func(t *testing.T) {
		apiserver := vals["apiserver"].(map[string]interface{})
		if got := apiserver["replicaCount"]; got != int64(2) {
			t.Errorf("replicaCount = %v, want 2", got)
		}
	})

	t.Run("apiserver enabled", func(t *testing.T) {
		apiserver := vals["apiserver"].(map[string]interface{})
		if got := apiserver["enabled"]; got != true {
			t.Errorf("enabled = %v, want true", got)
		}
	})

	t.Run("processor inference gateway", func(t *testing.T) {
		processor := vals["processor"].(map[string]interface{})
		config := processor["config"].(map[string]interface{})
		gig := config["globalInferenceGateway"].(map[string]interface{})
		if got := gig["url"]; got != "http://inference-gateway:8000" {
			t.Errorf("url = %v", got)
		}
		if got := gig["requestTimeout"]; got != "5m" {
			t.Errorf("requestTimeout = %v", got)
		}
	})

	t.Run("gc config", func(t *testing.T) {
		gc := vals["gc"].(map[string]interface{})
		config := gc["config"].(map[string]interface{})
		if got := config["interval"]; got != "30m" {
			t.Errorf("interval = %v, want %q", got, "30m")
		}
	})
}

func TestSpecToHelmValues_Monitoring(t *testing.T) {
	gw := minimalGateway()
	gw.Spec.Monitoring = &batchv1alpha1.MonitoringSpec{Enabled: true}

	vals, err := specToHelmValues(gw)
	if err != nil {
		t.Fatalf("specToHelmValues() error: %v", err)
	}

	t.Run("service monitor enabled", func(t *testing.T) {
		apiserver := vals["apiserver"].(map[string]interface{})
		sm := apiserver["serviceMonitor"].(map[string]interface{})
		if got := sm["enabled"]; got != true {
			t.Errorf("serviceMonitor.enabled = %v, want true", got)
		}
	})

	t.Run("pod monitor enabled", func(t *testing.T) {
		processor := vals["processor"].(map[string]interface{})
		pm := processor["podMonitor"].(map[string]interface{})
		if got := pm["enabled"]; got != true {
			t.Errorf("podMonitor.enabled = %v, want true", got)
		}
	})
}

func TestSpecToHelmValues_TLS(t *testing.T) {
	gw := minimalGateway()
	gw.Spec.TLS = &batchv1alpha1.TLSSpec{
		Enabled: true,
		CertManager: &batchv1alpha1.CertManagerSpec{
			IssuerName: "letsencrypt",
			IssuerKind: "ClusterIssuer",
		},
	}

	vals, err := specToHelmValues(gw)
	if err != nil {
		t.Fatalf("specToHelmValues() error: %v", err)
	}

	apiserver := vals["apiserver"].(map[string]interface{})
	tls := apiserver["tls"].(map[string]interface{})

	t.Run("tls enabled", func(t *testing.T) {
		if got := tls["enabled"]; got != true {
			t.Errorf("tls.enabled = %v, want true", got)
		}
	})

	t.Run("cert manager", func(t *testing.T) {
		cm := tls["certManager"].(map[string]interface{})
		if got := cm["enabled"]; got != true {
			t.Errorf("certManager.enabled = %v, want true", got)
		}
		if got := cm["issuerName"]; got != "letsencrypt" {
			t.Errorf("issuerName = %v, want %q", got, "letsencrypt")
		}
	})
}

func TestSpecToHelmValues_HTTPRoute(t *testing.T) {
	gw := minimalGateway()
	gw.Spec.HTTPRoute = &batchv1alpha1.HTTPRouteSpec{
		Enabled: true,
		ParentRefs: []batchv1alpha1.ParentReference{
			{Name: "my-gateway", Namespace: "gateway-ns"},
		},
	}

	vals, err := specToHelmValues(gw)
	if err != nil {
		t.Fatalf("specToHelmValues() error: %v", err)
	}

	apiserver := vals["apiserver"].(map[string]interface{})
	hr := apiserver["httpRoute"].(map[string]interface{})

	t.Run("http route enabled", func(t *testing.T) {
		if got := hr["enabled"]; got != true {
			t.Errorf("httpRoute.enabled = %v, want true", got)
		}
	})

	t.Run("parent refs", func(t *testing.T) {
		refs := hr["parentRefs"].([]interface{})
		if len(refs) != 1 {
			t.Fatalf("parentRefs length = %d, want 1", len(refs))
		}
		ref := refs[0].(map[string]interface{})
		if got := ref["name"]; got != "my-gateway" {
			t.Errorf("parentRef.name = %v, want %q", got, "my-gateway")
		}
	})
}

func TestNewHelmRenderer(t *testing.T) {
	t.Run("valid chart path", func(t *testing.T) {
		renderer, err := NewHelmRenderer("../../batch-gateway/charts/batch-gateway")
		if err != nil {
			t.Fatalf("NewHelmRenderer() error: %v", err)
		}
		if renderer == nil {
			t.Fatal("renderer is nil")
		}
		if renderer.chart == nil {
			t.Fatal("chart is nil")
		}
	})

	t.Run("invalid chart path", func(t *testing.T) {
		_, err := NewHelmRenderer("/nonexistent/path")
		if err == nil {
			t.Fatal("expected error for invalid chart path")
		}
	})
}

func TestRenderChart(t *testing.T) {
	renderer, err := NewHelmRenderer("../../batch-gateway/charts/batch-gateway")
	if err != nil {
		t.Fatalf("NewHelmRenderer() error: %v", err)
	}

	gw := minimalGateway()
	objects, err := renderer.RenderChart(gw)
	if err != nil {
		t.Fatalf("RenderChart() error: %v", err)
	}

	kinds := map[string]int{}
	for _, obj := range objects {
		kinds[obj.GetKind()]++
	}

	t.Run("renders deployments", func(t *testing.T) {
		if got := kinds["Deployment"]; got != 3 {
			t.Errorf("Deployment count = %d, want 3", got)
		}
	})

	t.Run("renders configmaps", func(t *testing.T) {
		if got := kinds["ConfigMap"]; got < 3 {
			t.Errorf("ConfigMap count = %d, want >= 3", got)
		}
	})

	t.Run("renders service accounts", func(t *testing.T) {
		if got := kinds["ServiceAccount"]; got != 3 {
			t.Errorf("ServiceAccount count = %d, want 3", got)
		}
	})

	t.Run("renders service", func(t *testing.T) {
		if got := kinds["Service"]; got != 1 {
			t.Errorf("Service count = %d, want 1", got)
		}
	})

	t.Run("no optional resources by default", func(t *testing.T) {
		for _, kind := range []string{"Certificate", "HTTPRoute", "ServiceMonitor", "PodMonitor", "PrometheusRule"} {
			if got := kinds[kind]; got != 0 {
				t.Errorf("%s count = %d, want 0", kind, got)
			}
		}
	})
}

func TestRenderChart_WithMonitoring(t *testing.T) {
	renderer, err := NewHelmRenderer("../../batch-gateway/charts/batch-gateway")
	if err != nil {
		t.Fatalf("NewHelmRenderer() error: %v", err)
	}

	gw := minimalGateway()
	gw.Spec.Monitoring = &batchv1alpha1.MonitoringSpec{Enabled: true}

	objects, err := renderer.RenderChart(gw)
	if err != nil {
		t.Fatalf("RenderChart() error: %v", err)
	}

	kinds := map[string]int{}
	for _, obj := range objects {
		kinds[obj.GetKind()]++
	}

	t.Run("renders service monitor", func(t *testing.T) {
		if got := kinds["ServiceMonitor"]; got != 1 {
			t.Errorf("ServiceMonitor count = %d, want 1", got)
		}
	})

	t.Run("renders pod monitor", func(t *testing.T) {
		if got := kinds["PodMonitor"]; got != 1 {
			t.Errorf("PodMonitor count = %d, want 1", got)
		}
	})
}

func TestSpecToHelmValues_Logging(t *testing.T) {
	gw := minimalGateway()
	gw.Spec.APIServer.Config = &batchv1alpha1.APIServerConfigSpec{
		Logging: &batchv1alpha1.LoggingConfig{Verbosity: 5},
	}
	gw.Spec.Processor.Config = &batchv1alpha1.ProcessorConfigSpec{
		Logging: &batchv1alpha1.LoggingConfig{Verbosity: 3},
	}
	gw.Spec.GC.Config = &batchv1alpha1.GCConfigSpec{
		Logging: &batchv1alpha1.LoggingConfig{Verbosity: 4},
	}

	vals, err := specToHelmValues(gw)
	if err != nil {
		t.Fatalf("specToHelmValues() error: %v", err)
	}

	t.Run("apiserver logging verbosity", func(t *testing.T) {
		apiserver := vals["apiserver"].(map[string]interface{})
		config := apiserver["config"].(map[string]interface{})
		logging := config["logging"].(map[string]interface{})
		if got := logging["verbosity"]; got != int64(5) {
			t.Errorf("apiserver logging.verbosity = %v, want 5", got)
		}
	})

	t.Run("processor logging verbosity", func(t *testing.T) {
		processor := vals["processor"].(map[string]interface{})
		config := processor["config"].(map[string]interface{})
		logging := config["logging"].(map[string]interface{})
		if got := logging["verbosity"]; got != int64(3) {
			t.Errorf("processor logging.verbosity = %v, want 3", got)
		}
	})

	t.Run("gc logging verbosity", func(t *testing.T) {
		gc := vals["gc"].(map[string]interface{})
		config := gc["config"].(map[string]interface{})
		logging := config["logging"].(map[string]interface{})
		if got := logging["verbosity"]; got != int64(4) {
			t.Errorf("gc logging.verbosity = %v, want 4", got)
		}
	})
}

func minimalGateway() *batchv1alpha1.LLMBatchGateway {
	replicas := int32(1)
	return &batchv1alpha1.LLMBatchGateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "batch-gateway",
			Namespace: "default",
		},
		Spec: batchv1alpha1.LLMBatchGatewaySpec{
			SecretRef: batchv1alpha1.SecretReference{
				Name: "batch-gateway-secrets",
			},
			DBBackend: "postgresql",
			FileStorage: &batchv1alpha1.FileStorageSpec{
				S3: &batchv1alpha1.S3StorageSpec{
					Region:   "us-east-1",
					Endpoint: "https://s3.example.com",
				},
			},
			APIServer: batchv1alpha1.APIServerSpec{
				Replicas: &replicas,
				Image:    "ghcr.io/llm-d-incubation/batch-gateway-apiserver:latest",
			},
			Processor: batchv1alpha1.ProcessorSpec{
				Replicas: &replicas,
				Image:    "ghcr.io/llm-d-incubation/batch-gateway-processor:latest",
				GlobalInferenceGateway: &batchv1alpha1.InferenceGatewaySpec{
					URL: "http://inference-gateway:8000",
				},
			},
			GC: batchv1alpha1.GCSpec{
				Image:    "ghcr.io/llm-d-incubation/batch-gateway-gc:latest",
				Interval: "30m",
			},
		},
	}
}
