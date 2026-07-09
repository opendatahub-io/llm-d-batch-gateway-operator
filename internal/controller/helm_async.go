package controller

import (
	"encoding/json"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	batchv1alpha1 "github.com/opendatahub-io/llm-d-batch-gateway-operator/api/v1alpha1"
)

func (h *HelmRenderer) RenderAsyncChart(gw *batchv1alpha1.LLMBatchGateway, secretName string) ([]*unstructured.Unstructured, error) {
	vals, err := specToAsyncHelmValues(gw, secretName, h.images)
	if err != nil {
		return nil, err
	}
	return h.renderChart(gw, vals, func(obj *unstructured.Unstructured) {
		// async does not use this label to indicate component name, workaround to append it for update status
		labels := obj.GetLabels()
		if labels == nil {
			labels = make(map[string]string)
		}
		labels[labelKeyComponent] = componentAsyncProcessor
		obj.SetLabels(labels)
	})
}

// specToAsyncHelmValues maps the CRD spec to upstream async-processor Helm values.
func specToAsyncHelmValues(gw *batchv1alpha1.LLMBatchGateway, secretName string, images ComponentImages) (map[string]any, error) {
	ac := gw.Spec.Processor.AsyncConfig
	if ac == nil {
		return map[string]any{}, nil
	}

	ap := map[string]any{}

	// Merge opaque values first so operator-managed fields can override.
	if ac.Values != nil && ac.Values.Raw != nil {
		var raw map[string]any
		if err := json.Unmarshal(ac.Values.Raw, &raw); err != nil {
			return nil, fmt.Errorf("decoding asyncConfig.values: %w", err)
		}
		for k, v := range raw {
			ap[k] = v
		}
	}

	// Operator-managed fields (always override values).
	asyncRepo, asyncTag := splitImage(images.Async)
	ap["image"] = map[string]any{
		"repository": asyncRepo,
		"tag":        asyncTag,
	}
	if ac.Replicas != nil {
		ap["replicaCount"] = int64(*ac.Replicas)
	}
	if ac.ImagePullPolicy != "" {
		ap["imagePullPolicy"] = string(ac.ImagePullPolicy)
	}
	if ac.Resources != nil {
		ap["resources"] = resourceRequirementsToMap(ac.Resources)
	}

	// Validate redis is configured and enabled — it is the only supported message queue backend.
	redis, ok := ap["redis"].(map[string]any)
	if !ok {
		return nil, errors.New("asyncConfig.values.redis is required")
	}
	if enabled, _ := redis["enabled"].(bool); !enabled {
		return nil, errors.New("asyncConfig.values.redis.enabled must be true")
	}
	redis["secretName"] = secretName
	redis["secretKey"] = "redis-url"
	if v, set := ap["messageQueueImpl"]; !set || v != "redis-sortedset" {
		return nil, fmt.Errorf("asyncConfig.values.messageQueueImpl must be \"redis-sortedset\", got %v", v)
	}

	// Operator-owned cross-cutting concerns from the top-level spec.
	if gw.Spec.OTEL != nil {
		otelVals := map[string]any{}
		setIfNotEmpty(otelVals, "endpoint", gw.Spec.OTEL.Endpoint)
		otelVals["insecure"] = gw.Spec.OTEL.Insecure
		setIfNotEmpty(otelVals, "sampler", gw.Spec.OTEL.Sampler)
		setIfNotEmpty(otelVals, "samplerArg", gw.Spec.OTEL.SamplerArg)
		otelVals["redisTracing"] = gw.Spec.OTEL.RedisTracing
		ap["otel"] = otelVals
	}

	if gw.Spec.Monitoring != nil && gw.Spec.Monitoring.Enabled {
		ap["podMonitor"] = map[string]any{
			"enabled": true,
			"labels": map[string]any{
				odhMonitoringScrapeLabel: odhMonitoringScrapeValue,
			},
		}
	}

	if gw.Spec.Grafana != nil && gw.Spec.Grafana.Enabled {
		ap["grafana"] = map[string]any{
			"dashboards": map[string]any{
				"enabled": true,
			},
		}
	}

	if gw.Spec.PrometheusRule != nil && gw.Spec.PrometheusRule.Enabled {
		pr := map[string]any{
			"enabled": true,
		}
		if len(gw.Spec.PrometheusRule.Labels) > 0 {
			pr["labels"] = toStringInterfaceMap(gw.Spec.PrometheusRule.Labels)
		}
		ap["prometheusRule"] = pr
	}

	return map[string]any{
		"ap": ap,
	}, nil
}
