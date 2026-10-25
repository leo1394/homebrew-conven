package materialize

import (
	"strings"
	"testing"
)

func TestVerifyEnvironmentPlanGuardsDoesNotRequireFiles(t *testing.T) {
	if err := VerifyPlanGuards("", DriverEnvironment, nil); err != nil {
		t.Fatalf("environment delivery has no file directory: %v", err)
	}
}

func TestVerifyEnvironmentPlanGuardsRejectsFileGuards(t *testing.T) {
	err := VerifyPlanGuards(t.TempDir(), DriverEnvironment, []Guard{{File: "application.yaml", Path: "host", Value: "127.0.0.1"}})
	if err == nil || !strings.Contains(err.Error(), "environment") {
		t.Fatalf("environment driver must not silently skip file guards: %v", err)
	}
}

func TestVerifyFilePlanGuardsStillRequiresDirectory(t *testing.T) {
	if err := VerifyPlanGuards("", DriverYAMLOverlay, nil); err == nil {
		t.Fatal("file delivery must still validate its target directory")
	}
}
