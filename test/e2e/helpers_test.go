package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const (
	pollInterval       = 2 * time.Second
	llmBatchGatewayKind = "llmbatchgateway"
)

func getEnvOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func kubectl(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	return cmd.CombinedOutput()
}

// snapshotCRSpec captures the full spec of the named LLMBatchGateway CR and
// returns a restore function that applies it back as a merge patch. Pass the
// returned function directly to t.Cleanup.
func snapshotCRSpec(t *testing.T, name, namespace string) func() {
	t.Helper()
	obj := kubectlGetJSON(t, "llmbatchgateway", name, namespace)
	spec, ok := obj["spec"]
	if !ok {
		t.Fatalf("CR %s has no spec", name)
	}
	b, err := json.Marshal(map[string]any{"spec": spec})
	if err != nil {
		t.Fatalf("marshalling spec snapshot for %s: %v", name, err)
	}
	snapshot := string(b)
	return func() {
		kubectlPatch(t, "llmbatchgateway", name, namespace, snapshot)
	}
}

func kubectlPatch(t *testing.T, resource, name, namespace, patchJSON string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out, err := kubectl(ctx, "patch", resource, name, "-n", namespace, "--type", "merge", "-p", patchJSON)
	if err != nil {
		t.Fatalf("kubectl patch %s %s: %v\n%s", resource, name, err, out)
	}
}

func kubectlGetExists(t *testing.T, resource, name, namespace string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out, err := kubectl(ctx, "get", resource, name, "-n", namespace, "--no-headers")
	if err == nil {
		return true
	}
	if strings.Contains(string(out), "NotFound") {
		return false
	}
	t.Fatalf("kubectl get %s %s: %v\n%s", resource, name, err, out)
	return false
}

func kubectlGetJSON(t *testing.T, resource, name, namespace string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out, err := kubectl(ctx, "get", resource, name, "-n", namespace, "-o", "json")
	if err != nil {
		t.Fatalf("kubectl get %s %s: %v\n%s", resource, name, err, out)
	}

	var result map[string]any
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("parsing JSON for %s %s: %v", resource, name, err)
	}
	return result
}

func waitForResourceExists(t *testing.T, resource, name, namespace string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if kubectlGetExists(t, resource, name, namespace) {
			return
		}
		time.Sleep(pollInterval)
	}
	t.Fatalf("timed out waiting for %s/%s to exist", resource, name)
}

func waitForResourceGone(t *testing.T, resource, name, namespace string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !kubectlGetExists(t, resource, name, namespace) {
			return
		}
		time.Sleep(pollInterval)
	}
	t.Fatalf("timed out waiting for %s/%s to be deleted", resource, name)
}

func getCRConditions(t *testing.T, crName, namespace string) []map[string]any {
	t.Helper()
	obj := kubectlGetJSON(t, llmBatchGatewayKind, crName, namespace)

	status, ok := obj["status"].(map[string]any)
	if !ok {
		return nil
	}
	rawConds, ok := status["conditions"].([]any)
	if !ok {
		return nil
	}

	var conditions []map[string]any
	for _, c := range rawConds {
		if m, ok := c.(map[string]any); ok {
			conditions = append(conditions, m)
		}
	}
	return conditions
}

func findDeploymentByComponent(t *testing.T, namespace, instance, component string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	selector := fmt.Sprintf("app.kubernetes.io/instance=%s,app.kubernetes.io/component=%s", instance, component)
	out, err := kubectl(ctx, "get", "deployment", "-n", namespace, "-l", selector, "-o", "jsonpath={.items[0].metadata.name}")
	if err != nil {
		t.Fatalf("finding deployment with component=%s: %v\n%s", component, err, out)
	}
	name := string(out)
	if name == "" {
		t.Fatalf("no deployment found with labels instance=%s,component=%s", instance, component)
	}
	return name
}

func getDeploymentReplicas(t *testing.T, name, namespace string) int64 {
	t.Helper()
	obj := kubectlGetJSON(t, "deployment", name, namespace)

	spec, ok := obj["spec"].(map[string]any)
	if !ok {
		t.Fatalf("deployment %s has no spec", name)
	}
	replicas, ok := spec["replicas"].(float64)
	if !ok {
		t.Fatalf("deployment %s has no spec.replicas", name)
	}
	return int64(replicas)
}

func findConfigMapByComponent(t *testing.T, namespace, instance, component string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	selector := fmt.Sprintf("app.kubernetes.io/instance=%s,app.kubernetes.io/component=%s", instance, component)
	out, err := kubectl(ctx, "get", "configmap", "-n", namespace, "-l", selector, "-o", "jsonpath={.items[0].metadata.name}")
	if err != nil {
		t.Fatalf("finding configmap with component=%s: %v\n%s", component, err, out)
	}
	name := string(out)
	if name == "" {
		t.Fatalf("no configmap found with labels instance=%s,component=%s", instance, component)
	}
	return name
}

func getConfigMapData(t *testing.T, name, namespace string) string {
	t.Helper()
	obj := kubectlGetJSON(t, "configmap", name, namespace)
	data, _ := obj["data"].(map[string]any)
	configYAML, _ := data["config.yaml"].(string)
	return configYAML
}

func getDeploymentPodAnnotation(t *testing.T, name, namespace, annotation string) string {
	t.Helper()
	obj := kubectlGetJSON(t, "deployment", name, namespace)

	spec, _ := obj["spec"].(map[string]any)
	template, _ := spec["template"].(map[string]any)
	metadata, _ := template["metadata"].(map[string]any)
	annotations, _ := metadata["annotations"].(map[string]any)
	val, _ := annotations[annotation].(string)
	return val
}

func getContainerResources(t *testing.T, deploymentName, namespace string) map[string]any {
	t.Helper()
	obj := kubectlGetJSON(t, "deployment", deploymentName, namespace)

	spec, _ := obj["spec"].(map[string]any)
	template, _ := spec["template"].(map[string]any)
	podSpec, _ := template["spec"].(map[string]any)
	containers, _ := podSpec["containers"].([]any)
	if len(containers) == 0 {
		t.Fatalf("deployment %s has no containers", deploymentName)
	}
	container, _ := containers[0].(map[string]any)
	resources, _ := container["resources"].(map[string]any)
	return resources
}
