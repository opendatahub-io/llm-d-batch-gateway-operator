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
		Redis: &batchv1alpha1.AsyncRedisSpec{
			RequestQueueName: "request-sortedset",
			ResultQueueName:  "result-sortedset",
		},
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
		if got := redis["requestQueueName"]; got != "request-sortedset" {
			t.Errorf("redis.requestQueueName = %v, want request-sortedset", got)
		}
		if got := redis["resultQueueName"]; got != "result-sortedset" {
			t.Errorf("redis.resultQueueName = %v, want result-sortedset", got)
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

	pm, ok := vals["podMonitor"].(map[string]any)
	if !ok {
		t.Fatal("podMonitor not set when monitoring is enabled")
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
