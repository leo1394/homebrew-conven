package runtime

import (
	"strings"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/materialize"
	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestPrintPlanUsesPlainStageAndDetailStructure(t *testing.T) {
	plan := &Plan{
		Workspace: &WorkspaceData{
			Root:     "/workspace",
			Manifest: &model.Manifest{Workspace: model.Workspace{Name: "retail-platform"}},
			Store:    &Store{Root: "/workspace/.conven/runtime"},
		},
		EnvironmentName: "dev",
		Environment:     model.Environment{Registry: "consul"},
		Selected:        []string{"partner", "api"},
		Remote:          []string{"catalog"},
		Order:           []string{"partner", "api"},
		Groups:          [][]string{{"partner"}, {"api"}},
		RunDir:          "/workspace/.conven/runtime/current",
		Services: map[string]PlannedService{
			"partner": {Name: "partner"},
			"api": {
				Name: "api",
				Config: &PlannedConfig{
					Policy:    "retail",
					Framework: "go-zero",
					Discovery: "consul",
					Plan: materialize.Plan{
						SourceDriver: materialize.SourceApollo,
						Driver:       materialize.DriverYAMLOverlay,
						TargetDir:    "/workspace/.conven/runtime/current/configs/api",
					},
					Routes: []PlannedRoute{{Dependency: "partner", Binding: "partnerRpc", Local: true, Mode: "replace"}},
				},
			},
		},
		Connection: ConnectionConfig{Driver: "ktctl", Context: "dev-cluster", Namespace: "retail"},
	}
	var output strings.Builder
	printPlan(&output, plan, true)
	want := "" +
		"==> Service plan\n" +
		"  - Workspace: /workspace\n" +
		"  - Project: retail-platform\n" +
		"  - Runtime: /workspace/.conven/runtime\n" +
		"  - Current: /workspace/.conven/runtime/current\n" +
		"  - Environment: dev\n" +
		"  - Local services: partner, api\n" +
		"  - Remote dependencies via consul: catalog\n" +
		"  - Start groups: partner -> api\n" +
		"  - Connection: ktctl context=dev-cluster namespace=retail\n" +
		"==> Config api\n" +
		"  - Drivers: policy=retail, framework=go-zero, source=apollo, discovery=consul, materializer=yaml-overlay\n" +
		"  - Output: /workspace/.conven/runtime/current/configs/api\n" +
		"  - Local route: partner via partnerRpc (replace)\n" +
		"✓ Dry run complete; no connection, config fetch/materialization, build, process, or state changes were made.\n"
	if output.String() != want {
		t.Fatalf("plan output:\n%s\nwant:\n%s", output.String(), want)
	}
	if strings.Contains(output.String(), "\x1b[") {
		t.Fatalf("non-terminal output contains ANSI: %q", output.String())
	}
}

func TestListServicesUsesPlainStageAndDetails(t *testing.T) {
	workspace := &WorkspaceData{Manifest: &model.Manifest{Services: map[string]model.Service{
		"worker": {Path: "services/worker"},
		"api":    {Path: "services/api"},
	}}}
	var output strings.Builder
	ListServices(workspace, &output)
	if output.String() != "==> Available services\n  - api: services/api\n  - worker: services/worker\n" {
		t.Fatalf("service list output = %q", output.String())
	}
}
