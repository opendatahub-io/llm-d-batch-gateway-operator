package controller

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"

	batchv1alpha1 "github.com/opendatahub-io/llm-d-batch-gateway-operator/api/v1alpha1"
)

const (
	odhMonitoringScrapeLabel = "monitoring.opendatahub.io/scrape"
	odhMonitoringScrapeValue = "true"
)

// ComponentImages holds the container images for the batch-gateway components.
// These are supplied to the operator via environment variables (sourced from
// params.env) rather than the LLMBatchGateway CR, so that the platform — not
// the user creating the CR — controls which images are deployed.
type ComponentImages struct {
	APIServer string
	Processor string
	GC        string
	Async     string
}

type HelmRenderer struct {
	chart  *chart.Chart
	images ComponentImages
}

func NewHelmRenderer(chartPath string, images ComponentImages) (*HelmRenderer, error) {
	c, err := loader.Load(chartPath)
	if err != nil {
		return nil, fmt.Errorf("loading chart from %s: %w", chartPath, err)
	}
	return &HelmRenderer{chart: c, images: images}, nil
}

func (h *HelmRenderer) renderChart(gw *batchv1alpha1.LLMBatchGateway, vals map[string]any, postProcess func(*unstructured.Unstructured)) ([]*unstructured.Unstructured, error) {
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
			if postProcess != nil {
				postProcess(obj)
			}
			objects = append(objects, obj)
		}
	}

	return objects, nil
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
