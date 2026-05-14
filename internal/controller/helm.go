package controller

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"

	batchv1alpha1 "github.com/opendatahub-io/llm-d-batch-gateway-operator/api/v1alpha1"
)

type HelmRenderer struct {
	chart *chart.Chart
}

func NewHelmRenderer(chartPath string) (*HelmRenderer, error) {
	c, err := loader.Load(chartPath)
	if err != nil {
		return nil, fmt.Errorf("loading chart from %s: %w", chartPath, err)
	}
	return &HelmRenderer{chart: c}, nil
}

func (h *HelmRenderer) RenderChart(gw *batchv1alpha1.LLMBatchGateway, secretName string) ([]*unstructured.Unstructured, error) {
	vals := specToHelmValues(gw, secretName)

	releaseOpts := chartutil.ReleaseOptions{
		Name:      gw.Name,
		Namespace: gw.Namespace,
		IsInstall: true,
	}

	renderVals, err := chartutil.ToRenderValues(h.chart, vals, releaseOpts, nil)
	if err != nil {
		return nil, fmt.Errorf("preparing render values: %w", err)
	}

	rendered, err := engine.Render(h.chart, renderVals)
	if err != nil {
		return nil, fmt.Errorf("rendering templates: %w", err)
	}

	var objects []*unstructured.Unstructured
	for name, content := range rendered {
		if len(strings.TrimSpace(content)) == 0 || filepath.Base(name) == "NOTES.txt" {
			continue
		}

		decoder := yaml.NewYAMLToJSONDecoder(bytes.NewBufferString(content))
		for {
			obj := &unstructured.Unstructured{}
			if err := decoder.Decode(obj); err != nil {
				if err == io.EOF {
					break
				}
				return nil, fmt.Errorf("decoding template %s: %w", name, err)
			}
			if len(obj.Object) == 0 {
				continue
			}
			objects = append(objects, obj)
		}
	}

	return objects, nil
}

func specToHelmValues(gw *batchv1alpha1.LLMBatchGateway, secretName string) map[string]interface{} {
	vals := map[string]interface{}{}

	// --- Global ---
	global := map[string]interface{}{
		"secretName": secretName,
		"dbClient": map[string]interface{}{
			"type": gw.Spec.DBBackend,
		},
	}

	if gw.Spec.FileStorage != nil {
		fc := map[string]interface{}{}
		if gw.Spec.FileStorage.S3 != nil {
			fc["type"] = "s3"
			s3 := gw.Spec.FileStorage.S3
			s3Vals := map[string]interface{}{}
			setIfNotEmpty(s3Vals, "region", s3.Region)
			setIfNotEmpty(s3Vals, "endpoint", s3.Endpoint)
			setIfNotEmpty(s3Vals, "accessKeyId", s3.AccessKeyID)
			if s3.Prefix != "" {
				s3Vals["prefix"] = s3.Prefix
			}
			s3Vals["usePathStyle"] = s3.UsePathStyle
			s3Vals["autoCreateBucket"] = s3.AutoCreateBucket
			fc["s3"] = s3Vals
		}
		if gw.Spec.FileStorage.FS != nil {
			fc["type"] = "fs"
			fs := gw.Spec.FileStorage.FS
			fsVals := map[string]interface{}{}
			setIfNotEmpty(fsVals, "basePath", fs.BasePath)
			setIfNotEmpty(fsVals, "pvcName", fs.ClaimName)
			fc["fs"] = fsVals
		}
		if gw.Spec.FileStorage.Retry != nil {
			r := gw.Spec.FileStorage.Retry
			retryVals := map[string]interface{}{}
			if r.MaxRetries != 0 {
				retryVals["maxRetries"] = int64(r.MaxRetries)
			}
			setIfNotEmpty(retryVals, "initialBackoff", r.InitialBackoff)
			setIfNotEmpty(retryVals, "maxBackoff", r.MaxBackoff)
			if len(retryVals) > 0 {
				fc["retry"] = retryVals
			}
		}
		global["fileClient"] = fc
	}

	if gw.Spec.OTEL != nil {
		otel := gw.Spec.OTEL
		otelVals := map[string]interface{}{}
		setIfNotEmpty(otelVals, "endpoint", otel.Endpoint)
		otelVals["insecure"] = otel.Insecure
		setIfNotEmpty(otelVals, "sampler", otel.Sampler)
		setIfNotEmpty(otelVals, "samplerArg", otel.SamplerArg)
		otelVals["redisTracing"] = otel.RedisTracing
		otelVals["postgresqlTracing"] = otel.PostgresqlTracing
		global["otel"] = otelVals
	}

	vals["global"] = global

	// --- API Server ---
	apiRepo, apiTag := splitImage(gw.Spec.APIServer.Image)
	apiserver := map[string]interface{}{
		"enabled": true,
		"image": map[string]interface{}{
			"repository": apiRepo,
			"tag":        apiTag,
		},
		"serviceAccount": map[string]interface{}{
			"create": true,
		},
	}
	if gw.Spec.APIServer.Replicas != nil {
		apiserver["replicaCount"] = int64(*gw.Spec.APIServer.Replicas)
	}
	if gw.Spec.APIServer.Resources != nil {
		apiserver["resources"] = resourceRequirementsToMap(gw.Spec.APIServer.Resources)
	}
	if gw.Spec.APIServer.Config != nil {
		apiserver["config"] = apiServerConfigToMap(gw.Spec.APIServer.Config)
	}

	// TLS
	if gw.Spec.TLS != nil && gw.Spec.TLS.Enabled {
		tls := map[string]interface{}{
			"enabled": true,
		}
		if gw.Spec.TLS.SecretName != "" {
			tls["secretName"] = gw.Spec.TLS.SecretName
		}
		if gw.Spec.TLS.CertManager != nil {
			cm := map[string]interface{}{
				"enabled": true,
			}
			setIfNotEmpty(cm, "issuerName", gw.Spec.TLS.CertManager.IssuerName)
			setIfNotEmpty(cm, "issuerKind", gw.Spec.TLS.CertManager.IssuerKind)
			if len(gw.Spec.TLS.CertManager.DNSNames) > 0 {
				cm["dnsNames"] = toInterfaceSlice(gw.Spec.TLS.CertManager.DNSNames)
			}
			tls["certManager"] = cm
		}
		apiserver["tls"] = tls
	}

	// HTTPRoute
	if gw.Spec.HTTPRoute != nil && gw.Spec.HTTPRoute.Enabled {
		hr := map[string]interface{}{
			"enabled": true,
		}
		if len(gw.Spec.HTTPRoute.Annotations) > 0 {
			hr["annotations"] = toStringInterfaceMap(gw.Spec.HTTPRoute.Annotations)
		}
		if len(gw.Spec.HTTPRoute.ParentRefs) > 0 {
			var refs []interface{}
			for _, ref := range gw.Spec.HTTPRoute.ParentRefs {
				r := map[string]interface{}{
					"name": ref.Name,
				}
				setIfNotEmpty(r, "namespace", ref.Namespace)
				setIfNotEmpty(r, "sectionName", ref.SectionName)
				refs = append(refs, r)
			}
			hr["parentRefs"] = refs
		}
		apiserver["httpRoute"] = hr
	}

	// ServiceMonitor
	if gw.Spec.Monitoring != nil && gw.Spec.Monitoring.Enabled {
		apiserver["serviceMonitor"] = map[string]interface{}{
			"enabled": true,
		}
	}

	vals["apiserver"] = apiserver

	// --- Processor ---
	procRepo, procTag := splitImage(gw.Spec.Processor.Image)
	processor := map[string]interface{}{
		"enabled": true,
		"image": map[string]interface{}{
			"repository": procRepo,
			"tag":        procTag,
		},
		"serviceAccount": map[string]interface{}{
			"create": true,
		},
	}
	if gw.Spec.Processor.Replicas != nil {
		processor["replicaCount"] = int64(*gw.Spec.Processor.Replicas)
	}
	if gw.Spec.Processor.Resources != nil {
		processor["resources"] = resourceRequirementsToMap(gw.Spec.Processor.Resources)
	}

	procConfig := map[string]interface{}{}
	if gw.Spec.Processor.GlobalInferenceGateway != nil {
		procConfig["globalInferenceGateway"] = inferenceGatewayToMap(gw.Spec.Processor.GlobalInferenceGateway)
	}
	if len(gw.Spec.Processor.ModelGateways) > 0 {
		mg := map[string]interface{}{}
		for model, spec := range gw.Spec.Processor.ModelGateways {
			mg[model] = inferenceGatewayToMap(&spec)
		}
		procConfig["modelGateways"] = mg
	}
	if gw.Spec.Processor.Config != nil {
		mergeProcessorConfig(procConfig, gw.Spec.Processor.Config)
	}
	if len(procConfig) > 0 {
		processor["config"] = procConfig
	}

	// PodMonitor
	if gw.Spec.Monitoring != nil && gw.Spec.Monitoring.Enabled {
		processor["podMonitor"] = map[string]interface{}{
			"enabled": true,
		}
	}

	vals["processor"] = processor

	// --- GC ---
	gcRepo, gcTag := splitImage(gw.Spec.GC.Image)
	gc := map[string]interface{}{
		"enabled": true,
		"image": map[string]interface{}{
			"repository": gcRepo,
			"tag":        gcTag,
		},
		"serviceAccount": map[string]interface{}{
			"create": true,
		},
	}
	gcConfig := map[string]interface{}{}
	setIfNotEmpty(gcConfig, "interval", gw.Spec.GC.Interval)
	if gw.Spec.GC.Config != nil {
		if gw.Spec.GC.Config.DryRun {
			gcConfig["dryRun"] = true
		}
		if gw.Spec.GC.Config.MaxConcurrency != 0 {
			gcConfig["maxConcurrency"] = int64(gw.Spec.GC.Config.MaxConcurrency)
		}
		if gw.Spec.GC.Config.Logging != nil && gw.Spec.GC.Config.Logging.Verbosity != 0 {
			gcConfig["logging"] = map[string]interface{}{
				"verbosity": int64(gw.Spec.GC.Config.Logging.Verbosity),
			}
		}
	}
	if len(gcConfig) > 0 {
		gc["config"] = gcConfig
	}

	vals["gc"] = gc

	// --- Grafana ---
	if gw.Spec.Grafana != nil {
		vals["grafana"] = map[string]interface{}{
			"dashboards": map[string]interface{}{
				"enabled": gw.Spec.Grafana.Enabled,
			},
		}
	}

	// --- PrometheusRule ---
	if gw.Spec.PrometheusRule != nil {
		pr := map[string]interface{}{
			"enabled": gw.Spec.PrometheusRule.Enabled,
		}
		if len(gw.Spec.PrometheusRule.Labels) > 0 {
			pr["labels"] = toStringInterfaceMap(gw.Spec.PrometheusRule.Labels)
		}
		vals["prometheusRule"] = pr
	}

	return vals
}

// splitImage splits an image reference into (repository, tag).
// For digest references like "repo@sha256:abc", it splits on the last ":"
// producing ("repo@sha256", "abc"). The chart template reconstructs this
// via printf "%s:%s" back into the correct "repo@sha256:abc" format.
func splitImage(image string) (string, string) {
	lastColon := strings.LastIndex(image, ":")
	if lastColon == -1 {
		return image, "latest"
	}
	afterColon := image[lastColon+1:]
	if strings.Contains(afterColon, "/") {
		return image, "latest"
	}
	return image[:lastColon], afterColon
}

func inferenceGatewayToMap(gw *batchv1alpha1.InferenceGatewaySpec) map[string]interface{} {
	m := map[string]interface{}{
		"url": gw.URL,
	}
	setIfNotEmpty(m, "requestTimeout", gw.RequestTimeout)
	if gw.MaxRetries != nil {
		m["maxRetries"] = int64(*gw.MaxRetries)
	}
	setIfNotEmpty(m, "initialBackoff", gw.InitialBackoff)
	setIfNotEmpty(m, "maxBackoff", gw.MaxBackoff)
	if gw.TLSInsecureSkipVerify {
		m["tlsInsecureSkipVerify"] = true
	}
	setIfNotEmpty(m, "tlsCaCertFile", gw.TLSCACertFile)
	setIfNotEmpty(m, "tlsClientCertFile", gw.TLSClientCertFile)
	setIfNotEmpty(m, "tlsClientKeyFile", gw.TLSClientKeyFile)
	return m
}

func apiServerConfigToMap(cfg *batchv1alpha1.APIServerConfigSpec) map[string]interface{} {
	m := map[string]interface{}{}
	if cfg.Port != 0 {
		m["port"] = int64(cfg.Port)
	}
	if cfg.ObservabilityPort != 0 {
		m["observabilityPort"] = int64(cfg.ObservabilityPort)
	}
	if cfg.ReadTimeoutSeconds != 0 {
		m["readTimeoutSeconds"] = int64(cfg.ReadTimeoutSeconds)
	}
	if cfg.WriteTimeoutSeconds != 0 {
		m["writeTimeoutSeconds"] = int64(cfg.WriteTimeoutSeconds)
	}
	if cfg.IdleTimeoutSeconds != 0 {
		m["idleTimeoutSeconds"] = int64(cfg.IdleTimeoutSeconds)
	}
	if cfg.BatchAPI != nil {
		ba := map[string]interface{}{}
		if cfg.BatchAPI.EventTTLSeconds != 0 {
			ba["eventTTLSeconds"] = int64(cfg.BatchAPI.EventTTLSeconds)
		}
		if len(cfg.BatchAPI.PassThroughHeaders) > 0 {
			ba["passThroughHeaders"] = toInterfaceSlice(cfg.BatchAPI.PassThroughHeaders)
		}
		if len(ba) > 0 {
			m["batchAPI"] = ba
		}
	}
	if cfg.FileAPI != nil {
		fa := map[string]interface{}{}
		if cfg.FileAPI.DefaultExpirationSeconds != 0 {
			fa["defaultExpirationSeconds"] = cfg.FileAPI.DefaultExpirationSeconds
		}
		if cfg.FileAPI.MaxSizeBytes != 0 {
			fa["maxSizeBytes"] = cfg.FileAPI.MaxSizeBytes
		}
		if cfg.FileAPI.MaxLineCount != 0 {
			fa["maxLineCount"] = cfg.FileAPI.MaxLineCount
		}
		if len(fa) > 0 {
			m["fileAPI"] = fa
		}
	}
	if cfg.EnablePprof {
		m["enablePprof"] = true
	}
	if cfg.Logging != nil && cfg.Logging.Verbosity != 0 {
		m["logging"] = map[string]interface{}{
			"verbosity": int64(cfg.Logging.Verbosity),
		}
	}
	return m
}

func mergeProcessorConfig(m map[string]interface{}, cfg *batchv1alpha1.ProcessorConfigSpec) {
	if cfg.NumWorkers != 0 {
		m["numWorkers"] = int64(cfg.NumWorkers)
	}
	if cfg.GlobalConcurrency != 0 {
		m["globalConcurrency"] = int64(cfg.GlobalConcurrency)
	}
	if cfg.PerModelMaxConcurrency != 0 {
		m["perModelMaxConcurrency"] = int64(cfg.PerModelMaxConcurrency)
	}
	if cfg.RecoveryMaxConcurrency != 0 {
		m["recoveryMaxConcurrency"] = int64(cfg.RecoveryMaxConcurrency)
	}
	setIfNotEmpty(m, "inferenceObjective", cfg.InferenceObjective)
	if cfg.DefaultOutputExpirationSeconds != 0 {
		m["defaultOutputExpirationSeconds"] = cfg.DefaultOutputExpirationSeconds
	}
	if cfg.ProgressTTLSeconds != 0 {
		m["progressTTLSeconds"] = cfg.ProgressTTLSeconds
	}
	if cfg.EnablePprof {
		m["enablePprof"] = true
	}
	if cfg.Logging != nil && cfg.Logging.Verbosity != 0 {
		m["logging"] = map[string]interface{}{
			"verbosity": int64(cfg.Logging.Verbosity),
		}
	}
}

func resourceRequirementsToMap(r *corev1.ResourceRequirements) map[string]interface{} {
	m := map[string]interface{}{}
	if r.Limits != nil {
		limits := map[string]interface{}{}
		if cpu, ok := r.Limits[corev1.ResourceCPU]; ok {
			limits["cpu"] = cpu.String()
		}
		if mem, ok := r.Limits[corev1.ResourceMemory]; ok {
			limits["memory"] = mem.String()
		}
		m["limits"] = limits
	}
	if r.Requests != nil {
		requests := map[string]interface{}{}
		if cpu, ok := r.Requests[corev1.ResourceCPU]; ok {
			requests["cpu"] = cpu.String()
		}
		if mem, ok := r.Requests[corev1.ResourceMemory]; ok {
			requests["memory"] = mem.String()
		}
		m["requests"] = requests
	}
	return m
}

func setIfNotEmpty(m map[string]interface{}, key, value string) {
	if value != "" {
		m[key] = value
	}
}

func toInterfaceSlice(ss []string) []interface{} {
	result := make([]interface{}, len(ss))
	for i, s := range ss {
		result[i] = s
	}
	return result
}

func toStringInterfaceMap(m map[string]string) map[string]interface{} {
	result := make(map[string]interface{}, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}
