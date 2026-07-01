package controller

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	batchv1alpha1 "github.com/opendatahub-io/llm-d-batch-gateway-operator/api/v1alpha1"
)

func (h *HelmRenderer) RenderAsyncChart(gw *batchv1alpha1.LLMBatchGateway, secretName string) ([]*unstructured.Unstructured, error) {
	return h.renderChart(gw, specToAsyncHelmValues(gw, secretName, h.images), func(obj *unstructured.Unstructured) {
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
// secretName is used only by the Redis backend (redis.secretName); GCP Pub/Sub
// authenticates via Workload Identity and does not need a secret.
func specToAsyncHelmValues(gw *batchv1alpha1.LLMBatchGateway, secretName string, images ComponentImages) map[string]any {
	ac := gw.Spec.Processor.AsyncConfig
	if ac == nil {
		return map[string]any{}
	}

	asyncRepo, asyncTag := splitImage(images.Async)
	ap := map[string]any{
		"image": map[string]any{
			"repository": asyncRepo,
			"tag":        asyncTag,
		},
	}
	if ac.Replicas != nil {
		// upstream chart currently hardcodes replicas: 1 in the template;
		// pass the value so it takes effect once upstream templates it from values.
		ap["replicaCount"] = int64(*ac.Replicas)
	}
	if ac.ImagePullPolicy != "" {
		ap["imagePullPolicy"] = string(ac.ImagePullPolicy)
	}

	if ac.Concurrency != nil {
		ap["concurrency"] = int64(*ac.Concurrency)
	}

	if ac.InferenceGateway != nil {
		ap["igwBaseURL"] = ac.InferenceGateway.URL
	}

	if ac.Resources != nil {
		ap["resources"] = resourceRequirementsToMap(ac.Resources)
	}

	setIfNotEmpty(ap, "drainTimeout", ac.DrainTimeout)

	setIfNotEmpty(ap, "prometheusURL", ac.PrometheusURL)
	setIfNotEmpty(ap, "prometheusCacheTTL", ac.PrometheusCacheTTL)

	if ac.Redis != nil {
		redis := map[string]any{
			"enabled":    true,
			"secretName": secretName,
			"secretKey":  "redis-url",
		}
		setIfNotEmpty(redis, "requestQueueName", ac.Redis.RequestQueueName)
		setIfNotEmpty(redis, "resultQueueName", ac.Redis.ResultQueueName)
		setIfNotEmpty(redis, "requestPathURL", ac.Redis.RequestPathURL)
		if ac.Redis.PollIntervalMs != nil {
			redis["pollIntervalMs"] = int64(*ac.Redis.PollIntervalMs)
		}
		if ac.Redis.BatchSize != nil {
			redis["batchSize"] = int64(*ac.Redis.BatchSize)
		}
		setIfNotEmpty(redis, "gateType", ac.Redis.GateType)
		if len(ac.Redis.GateParams) > 0 {
			redis["gateParams"] = toStringInterfaceMap(ac.Redis.GateParams)
		}
		if len(ac.Redis.QueuesConfig) > 0 {
			var queues []any
			for _, q := range ac.Redis.QueuesConfig {
				qm := map[string]any{
					"name": q.Name,
				}
				setIfNotEmpty(qm, "igw_base_url", q.IGWBaseURL)
				setIfNotEmpty(qm, "request_queue_name", q.RequestQueueName)
				setIfNotEmpty(qm, "result_queue_name", q.ResultQueueName)
				setIfNotEmpty(qm, "request_path_url", q.RequestPathURL)
				setIfNotEmpty(qm, "gate_type", q.GateType)
				if len(q.GateParams) > 0 {
					qm["gate_params"] = toStringInterfaceMap(q.GateParams)
				}
				queues = append(queues, qm)
			}
			redis["queuesConfig"] = queues
		}
		ap["redis"] = redis
	}
	if ac.GCPPubSub != nil {
		pubsub := map[string]any{
			"enabled": true,
		}
		setIfNotEmpty(pubsub, "projectId", ac.GCPPubSub.ProjectID)
		setIfNotEmpty(pubsub, "requestSubscriberId", ac.GCPPubSub.RequestSubscriberID)
		setIfNotEmpty(pubsub, "resultTopicId", ac.GCPPubSub.ResultTopicID)
		setIfNotEmpty(pubsub, "requestPathURL", ac.GCPPubSub.RequestPathURL)
		if len(ac.GCPPubSub.TopicsConfig) > 0 {
			var topics []any
			for _, t := range ac.GCPPubSub.TopicsConfig {
				tm := map[string]any{
					"subscription_id": t.SubscriptionID,
				}
				setIfNotEmpty(tm, "igw_base_url", t.IGWBaseURL)
				setIfNotEmpty(tm, "request_path_url", t.RequestPathURL)
				setIfNotEmpty(tm, "result_topic_id", t.ResultTopicID)
				setIfNotEmpty(tm, "gate_type", t.GateType)
				if len(t.GateParams) > 0 {
					tm["gate_params"] = toStringInterfaceMap(t.GateParams)
				}
				topics = append(topics, tm)
			}
			pubsub["topicsConfig"] = topics
		}
		ap["gcpPubSub"] = pubsub
	}
	setIfNotEmpty(ap, "messageQueueImpl", ac.MessageQueueImpl)

	if len(ac.WorkerPools) > 0 {
		var pools []any
		for _, wp := range ac.WorkerPools {
			pm := map[string]any{
				"id":      wp.Name,
				"workers": int64(wp.Workers),
			}
			setIfNotEmpty(pm, "gate_type", wp.GateType)
			if len(wp.GateParams) > 0 {
				pm["gate_params"] = toStringInterfaceMap(wp.GateParams)
			}
			pools = append(pools, pm)
		}
		ap["workerPools"] = pools
	}

	vals := map[string]any{
		"ap": ap,
	}

	// reuse the existing monitoring block
	if gw.Spec.Monitoring != nil && gw.Spec.Monitoring.Enabled {
		vals["podMonitor"] = map[string]any{
			"enabled": true,
			"labels": map[string]any{
				odhMonitoringScrapeLabel: odhMonitoringScrapeValue,
			},
		}
	}

	return vals
}
