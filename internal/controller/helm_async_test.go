package controller

import (
	"testing"

	batchv1alpha1 "github.com/opendatahub-io/llm-d-batch-gateway-operator/api/v1alpha1"
)

func minimalAsyncGateway() *batchv1alpha1.LLMBatchGateway {
	gw := minimalGateway()
	concurrency := int32(8)
	gw.Spec.Processor.DispatchMode = dispatchModeAsync
	gw.Spec.Processor.AsyncConfig = &batchv1alpha1.AsyncProcessorSpec{
		Concurrency:  &concurrency,
		DrainTimeout: "2m",
		InferenceGateway: &batchv1alpha1.InferenceGatewaySpec{
			URL: "http://epp:8081",
		},
		Redis: &batchv1alpha1.AsyncRedisSpec{},
	}
	return gw
}

func TestSpecToAsyncHelmValues(t *testing.T) {
	gw := minimalAsyncGateway()
	vals := specToAsyncHelmValues(gw, testSecretName(gw), testImages())

	ap, ok := vals["ap"].(map[string]any)
	if !ok {
		t.Fatal("vals[\"ap\"] missing or wrong type")
	}

	t.Run("image", func(t *testing.T) {
		wantRepo, wantTag := splitImage(testImages().Async)
		img := ap["image"].(map[string]any)
		if got := img["repository"]; got != wantRepo {
			t.Errorf("image.repository = %v, want %s", got, wantRepo)
		}
		if got := img["tag"]; got != wantTag {
			t.Errorf("image.tag = %v, want %s", got, wantTag)
		}
	})

	t.Run("concurrency", func(t *testing.T) {
		if got := ap["concurrency"]; got != int64(8) {
			t.Errorf("concurrency = %v, want 8", got)
		}
	})

	t.Run("igwBaseURL", func(t *testing.T) {
		if got := ap["igwBaseURL"]; got != "http://epp:8081" {
			t.Errorf("igwBaseURL = %v, want http://epp:8081", got)
		}
	})

	t.Run("drainTimeout", func(t *testing.T) {
		if got := ap["drainTimeout"]; got != "2m" {
			t.Errorf("drainTimeout = %v, want 2m", got)
		}
	})

	t.Run("redis", func(t *testing.T) {
		redis := ap["redis"].(map[string]any)
		if got := redis["enabled"]; got != true {
			t.Errorf("redis.enabled = %v, want true", got)
		}
		if got := redis["secretName"]; got != testSecretName(gw) {
			t.Errorf("redis.secretName = %v, want %s", got, testSecretName(gw))
		}
		if got := redis["secretKey"]; got != "redis-url" {
			t.Errorf("redis.secretKey = %v, want redis-url", got)
		}
	})
}

func TestSpecToAsyncHelmValues_NilAsyncConfig(t *testing.T) {
	gw := minimalGateway()
	gw.Spec.Processor.AsyncConfig = nil
	vals := specToAsyncHelmValues(gw, testSecretName(gw), testImages())

	if len(vals) != 0 {
		t.Errorf("expected empty map for nil AsyncConfig, got %v", vals)
	}
}

func TestSpecToAsyncHelmValues_Monitoring(t *testing.T) {
	gw := minimalAsyncGateway()
	gw.Spec.Monitoring = &batchv1alpha1.MonitoringSpec{Enabled: true}
	vals := specToAsyncHelmValues(gw, testSecretName(gw), testImages())

	ap, ok := vals["ap"].(map[string]any)
	if !ok {
		t.Fatal("vals[\"ap\"] missing or wrong type")
	}
	pm, ok := ap["podMonitor"].(map[string]any)
	if !ok {
		t.Fatal("ap.podMonitor not set when monitoring is enabled")
	}
	if got := pm["enabled"]; got != true {
		t.Errorf("podMonitor.enabled = %v, want true", got)
	}
	labels, ok := pm["labels"].(map[string]any)
	if !ok {
		t.Fatal("podMonitor.labels missing")
	}
	if got := labels[odhMonitoringScrapeLabel]; got != odhMonitoringScrapeValue {
		t.Errorf("podMonitor.labels[%s] = %v, want %s", odhMonitoringScrapeLabel, got, odhMonitoringScrapeValue)
	}
}

func TestSpecToAsyncHelmValues_TLS(t *testing.T) {
	gw := minimalAsyncGateway()
	gw.Spec.Processor.AsyncConfig.TLS = &batchv1alpha1.AsyncTLSSpec{
		SecretName:         "igw-tls",
		InsecureSkipVerify: true,
		CACertKey:          "ca.crt",
		CertKey:            "tls.crt",
		KeyKey:             "tls.key",
	}
	vals := specToAsyncHelmValues(gw, testSecretName(gw), testImages())
	ap := vals["ap"].(map[string]any)
	tls, ok := ap["tls"].(map[string]any)
	if !ok {
		t.Fatal("ap.tls not set")
	}
	if got := tls["secretName"]; got != "igw-tls" {
		t.Errorf("tls.secretName = %v, want igw-tls", got)
	}
	if got := tls["insecureSkipVerify"]; got != true {
		t.Errorf("tls.insecureSkipVerify = %v, want true", got)
	}
	if got := tls["keyKey"]; got != "tls.key" {
		t.Errorf("tls.keyKey = %v, want tls.key", got)
	}
}

func TestSpecToAsyncHelmValues_TransformConfig(t *testing.T) {
	gw := minimalAsyncGateway()
	gw.Spec.Processor.AsyncConfig.TransformConfig = &batchv1alpha1.AsyncTransformConfig{
		RequestTransforms: []batchv1alpha1.AsyncRequestTransform{
			{Name: "gcs-whisper", Type: "gcs_uri_multipart", Parameters: map[string]string{"providers": "whisper"}},
		},
	}
	vals := specToAsyncHelmValues(gw, testSecretName(gw), testImages())
	ap := vals["ap"].(map[string]any)
	tc, ok := ap["transformConfig"].(map[string]any)
	if !ok {
		t.Fatal("ap.transformConfig not set")
	}
	transforms, ok := tc["requestTransforms"].([]any)
	if !ok || len(transforms) != 1 {
		t.Fatalf("requestTransforms length = %d, want 1", len(transforms))
	}
	tm := transforms[0].(map[string]any)
	if got := tm["name"]; got != "gcs-whisper" {
		t.Errorf("transform.name = %v, want gcs-whisper", got)
	}
	if got := tm["type"]; got != "gcs_uri_multipart" {
		t.Errorf("transform.type = %v, want gcs_uri_multipart", got)
	}
}

func TestSpecToAsyncHelmValues_ModelServerMonitor(t *testing.T) {
	gw := minimalAsyncGateway()
	gw.Spec.Processor.AsyncConfig.ModelServerMonitor = &batchv1alpha1.AsyncModelServerMonitorSpec{
		Enabled:  true,
		Selector: map[string]string{"llm-d.ai/role": "decode"},
		Port:     "modelserver",
		Path:     "/metrics",
		Interval: "15s",
	}
	vals := specToAsyncHelmValues(gw, testSecretName(gw), testImages())
	ap := vals["ap"].(map[string]any)
	msm, ok := ap["modelServerMonitor"].(map[string]any)
	if !ok {
		t.Fatal("ap.modelServerMonitor not set")
	}
	if got := msm["enabled"]; got != true {
		t.Errorf("modelServerMonitor.enabled = %v, want true", got)
	}
	if got := msm["port"]; got != "modelserver" {
		t.Errorf("modelServerMonitor.port = %v, want modelserver", got)
	}
	sel, ok := msm["selector"].(map[string]any)
	if !ok {
		t.Fatal("modelServerMonitor.selector missing")
	}
	if got := sel["llm-d.ai/role"]; got != "decode" {
		t.Errorf("selector[llm-d.ai/role] = %v, want decode", got)
	}
}

func TestSpecToAsyncHelmValues_OTEL(t *testing.T) {
	gw := minimalAsyncGateway()
	gw.Spec.OTEL = &batchv1alpha1.OTELSpec{
		Endpoint:     "http://jaeger:4317",
		Insecure:     true,
		Sampler:      "parentbased_traceidratio",
		SamplerArg:   "0.1",
		RedisTracing: false,
	}
	vals := specToAsyncHelmValues(gw, testSecretName(gw), testImages())
	ap := vals["ap"].(map[string]any)
	otel, ok := ap["otel"].(map[string]any)
	if !ok {
		t.Fatal("ap.otel not set")
	}
	if got := otel["endpoint"]; got != "http://jaeger:4317" {
		t.Errorf("otel.endpoint = %v, want http://jaeger:4317", got)
	}
	if got := otel["insecure"]; got != true {
		t.Errorf("otel.insecure = %v, want true", got)
	}
	if got := otel["redisTracing"]; got != false {
		t.Errorf("otel.redisTracing = %v, want false", got)
	}
}

func TestSpecToAsyncHelmValues_Grafana(t *testing.T) {
	gw := minimalAsyncGateway()
	gw.Spec.Grafana = &batchv1alpha1.GrafanaSpec{Enabled: true}
	vals := specToAsyncHelmValues(gw, testSecretName(gw), testImages())
	ap := vals["ap"].(map[string]any)
	grafana, ok := ap["grafana"].(map[string]any)
	if !ok {
		t.Fatal("ap.grafana not set")
	}
	dashboards, ok := grafana["dashboards"].(map[string]any)
	if !ok {
		t.Fatal("ap.grafana.dashboards missing")
	}
	if got := dashboards["enabled"]; got != true {
		t.Errorf("grafana.dashboards.enabled = %v, want true", got)
	}
}

func TestSpecToAsyncHelmValues_PrometheusRule(t *testing.T) {
	gw := minimalAsyncGateway()
	gw.Spec.PrometheusRule = &batchv1alpha1.PrometheusRuleSpec{
		Enabled: true,
		Labels:  map[string]string{"team": "ml"},
	}
	vals := specToAsyncHelmValues(gw, testSecretName(gw), testImages())
	ap := vals["ap"].(map[string]any)
	pr, ok := ap["prometheusRule"].(map[string]any)
	if !ok {
		t.Fatal("ap.prometheusRule not set")
	}
	if got := pr["enabled"]; got != true {
		t.Errorf("prometheusRule.enabled = %v, want true", got)
	}
	labels, ok := pr["labels"].(map[string]any)
	if !ok {
		t.Fatal("prometheusRule.labels missing")
	}
	if got := labels["team"]; got != "ml" {
		t.Errorf("prometheusRule.labels[team] = %v, want ml", got)
	}
}

func TestRenderAsyncChart(t *testing.T) {
	renderer, err := NewHelmRenderer("../../llm-d-async/charts/async-processor", testImages())
	if err != nil {
		t.Fatalf("NewHelmRenderer() error: %v", err)
	}

	gw := minimalAsyncGateway()
	objects, err := renderer.RenderAsyncChart(gw, testSecretName(gw))
	if err != nil {
		t.Fatalf("RenderAsyncChart() error: %v", err)
	}

	if len(objects) == 0 {
		t.Fatal("RenderAsyncChart() returned no objects")
	}

	t.Run("component label injected", func(t *testing.T) {
		for _, obj := range objects {
			labels := obj.GetLabels()
			if got := labels[labelKeyComponent]; got != componentAsyncProcessor {
				t.Errorf("%s %s: %s = %q, want %q",
					obj.GetKind(), obj.GetName(), labelKeyComponent, got, componentAsyncProcessor)
			}
		}
	})

	t.Run("has deployment", func(t *testing.T) {
		found := false
		for _, obj := range objects {
			if obj.GetKind() == "Deployment" {
				found = true
				break
			}
		}
		if !found {
			t.Error("no Deployment found in rendered objects")
		}
	})
}
