package v1alpha1

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGCSStorageSpecOmitsUnsetBooleans(t *testing.T) {
	data, err := json.Marshal(GCSStorageSpec{Bucket: "batch-files"})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	got := string(data)
	for _, field := range []string{"autoCreateBucket", "verifyBucketOnStart"} {
		if strings.Contains(got, field) {
			t.Errorf("marshaled GCSStorageSpec contains %q: %s", field, got)
		}
	}
}

func TestComponentExtraEndpointsPreservesEmptyList(t *testing.T) {
	empty := []string{}
	input := struct {
		APIServer BatchAPIConfig      `json:"apiServer"`
		Processor ProcessorConfigSpec `json:"processor"`
	}{
		APIServer: BatchAPIConfig{ExtraEndpoints: &empty},
		Processor: ProcessorConfigSpec{ExtraEndpoints: &empty},
	}

	data, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var output struct {
		APIServer BatchAPIConfig      `json:"apiServer"`
		Processor ProcessorConfigSpec `json:"processor"`
	}
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if output.APIServer.ExtraEndpoints == nil || len(*output.APIServer.ExtraEndpoints) != 0 {
		t.Fatalf("API server extraEndpoints = %v, want an explicit empty list", output.APIServer.ExtraEndpoints)
	}
	if output.Processor.ExtraEndpoints == nil || len(*output.Processor.ExtraEndpoints) != 0 {
		t.Fatalf("processor extraEndpoints = %v, want an explicit empty list", output.Processor.ExtraEndpoints)
	}
}
