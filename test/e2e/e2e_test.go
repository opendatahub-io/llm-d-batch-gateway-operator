package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

var (
	testNamespace = getEnvOrDefault("TEST_NAMESPACE", "default")
	testCRName    = getEnvOrDefault("TEST_CR_NAME", "batch-gateway")
)

func TestE2E(t *testing.T) {
	t.Run("Operator", func(t *testing.T) {
		t.Run("StatusConditions", testStatusConditions)
		t.Run("OrphanCleanup", testOrphanCleanup)
		t.Run("SpecUpdate", testSpecUpdate)
		t.Run("ProcessorReplicasUpdate", testProcessorReplicasUpdate)
		t.Run("ConfigChangeRollout", testConfigChangeRollout)
		t.Run("ResourcesUpdate", testResourcesUpdate)
	})
}

func testStatusConditions(t *testing.T) {
	expected := []string{"Ready", "APIServerAvailable", "ProcessorAvailable"}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		conditions := getCRConditions(t, testCRName, testNamespace)
		found := map[string]bool{}
		for _, c := range conditions {
			if typ, ok := c["type"].(string); ok {
				found[typ] = true
			}
		}
		allPresent := true
		for _, e := range expected {
			if !found[e] {
				allPresent = false
				break
			}
		}
		if allPresent {
			return
		}
		time.Sleep(pollInterval)
	}
	t.Fatalf("timed out waiting for status conditions: %v", expected)
}

func testOrphanCleanup(t *testing.T) {
	dashboardCM := testCRName + "-batch-gateway-dashboards"

	kubectlPatch(t, "llmbatchgateway", testCRName, testNamespace,
		`{"spec":{"grafana":{"enabled":true}}}`)
	t.Cleanup(func() {
		kubectlPatch(t, "llmbatchgateway", testCRName, testNamespace,
			`{"spec":{"grafana":{"enabled":false}}}`)
		waitForResourceGone(t, "configmap", dashboardCM, testNamespace, 60*time.Second)
	})

	waitForResourceExists(t, "configmap", dashboardCM, testNamespace, 60*time.Second)

	kubectlPatch(t, "llmbatchgateway", testCRName, testNamespace,
		`{"spec":{"grafana":{"enabled":false}}}`)

	waitForResourceGone(t, "configmap", dashboardCM, testNamespace, 60*time.Second)
}

func testSpecUpdate(t *testing.T) {
	deploymentName := findDeploymentByComponent(t, testNamespace, testCRName, "apiserver")

	original := getDeploymentReplicas(t, deploymentName, testNamespace)
	target := original + 1

	kubectlPatch(t, "llmbatchgateway", testCRName, testNamespace,
		fmt.Sprintf(`{"spec":{"apiServer":{"replicas":%d}}}`, target))
	t.Cleanup(func() {
		kubectlPatch(t, "llmbatchgateway", testCRName, testNamespace,
			fmt.Sprintf(`{"spec":{"apiServer":{"replicas":%d}}}`, original))
	})

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if getDeploymentReplicas(t, deploymentName, testNamespace) == target {
			return
		}
		time.Sleep(pollInterval)
	}
	t.Fatalf("deployment %s replicas did not update to %d", deploymentName, target)
}

func testProcessorReplicasUpdate(t *testing.T) {
	deploymentName := findDeploymentByComponent(t, testNamespace, testCRName, "processor")

	original := getDeploymentReplicas(t, deploymentName, testNamespace)
	target := original + 1

	kubectlPatch(t, "llmbatchgateway", testCRName, testNamespace,
		fmt.Sprintf(`{"spec":{"processor":{"replicas":%d}}}`, target))
	t.Cleanup(func() {
		kubectlPatch(t, "llmbatchgateway", testCRName, testNamespace,
			fmt.Sprintf(`{"spec":{"processor":{"replicas":%d}}}`, original))
	})

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if getDeploymentReplicas(t, deploymentName, testNamespace) == target {
			return
		}
		time.Sleep(pollInterval)
	}
	t.Fatalf("deployment %s replicas did not update to %d", deploymentName, target)
}

func testConfigChangeRollout(t *testing.T) {
	components := []struct {
		name     string
		patch    string
		cmSubstr string // expected substring in the ConfigMap config.yaml
	}{
		{
			name:     "apiserver",
			patch:    `{"spec":{"apiServer":{"config":{"readTimeoutSeconds":999}}}}`,
			cmSubstr: "read_timeout_seconds: 999",
		},
		{
			name:     "processor",
			patch:    `{"spec":{"processor":{"config":{"numWorkers":99}}}}`,
			cmSubstr: "num_workers: 99",
		},
		{
			name:     "gc",
			patch:    `{"spec":{"gc":{"interval":"59m"}}}`,
			cmSubstr: `interval: "59m"`,
		},
	}

	for _, tc := range components {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(snapshotCRSpec(t, testCRName, testNamespace))
			deploymentName := findDeploymentByComponent(t, testNamespace, testCRName, tc.name)
			configMapName := findConfigMapByComponent(t, testNamespace, testCRName, tc.name)
			checksumBefore := getDeploymentPodAnnotation(t, deploymentName, testNamespace, "checksum/config")

			kubectlPatch(t, "llmbatchgateway", testCRName, testNamespace, tc.patch)

			deadline := time.Now().Add(60 * time.Second)
			for time.Now().Before(deadline) {
				// Verify the ConfigMap data contains the expected value.
				cmData := getConfigMapData(t, configMapName, testNamespace)
				if !strings.Contains(cmData, tc.cmSubstr) {
					time.Sleep(pollInterval)
					continue
				}

				// Verify the Deployment pod template was updated.
				checksumAfter := getDeploymentPodAnnotation(t, deploymentName, testNamespace, "checksum/config")
				if checksumAfter != checksumBefore {
					return
				}
				time.Sleep(pollInterval)
			}
			t.Fatalf("deployment %s or configmap %s did not update after config change", deploymentName, configMapName)
		})
	}
}

func testResourcesUpdate(t *testing.T) {
	components := []struct {
		name      string
		specField string
	}{
		{name: "apiserver", specField: "apiServer"},
		{name: "processor", specField: "processor"},
	}

	for _, tc := range components {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(snapshotCRSpec(t, testCRName, testNamespace))
			deploymentName := findDeploymentByComponent(t, testNamespace, testCRName, tc.name)

			kubectlPatch(t, "llmbatchgateway", testCRName, testNamespace,
				`{"spec":{"`+tc.specField+`":{"resources":{"requests":{"cpu":"111m","memory":"99Mi"}}}}}`)

			deadline := time.Now().Add(60 * time.Second)
			for time.Now().Before(deadline) {
				resources := getContainerResources(t, deploymentName, testNamespace)
				requests, _ := resources["requests"].(map[string]any)
				if requests != nil && requests["cpu"] == "111m" && requests["memory"] == "99Mi" {
					return
				}
				time.Sleep(pollInterval)
			}
			t.Fatalf("deployment %s container resources did not update", deploymentName)
		})
	}
}

