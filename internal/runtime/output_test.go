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
		DeclaredRemote:  []string{"catalog"},
		Order:           []string{"partner", "api"},
		Groups:          [][]string{{"partner"}, {"api"}},
		RunDir:          "/workspace/.conven/runtime/current",
		Services: map[string]PlannedService{
			"partner": {Name: "partner"},
			"api": {
				Name: "api",
				ConsumerIsolation: map[string]ConsumerIsolationEvidence{
					"kafka": {Driver: "kafka", Mode: "guarded", Env: "SERVICE_KAFKA_CONSUMERS_ENABLED", Status: "enabled"},
				},
				Config: &PlannedConfig{
					Policy:    "retail",
					Framework: "go-zero",
					Discovery: "consul",
					Isolation: PlannedIsolation{
						RegistrationMode: "not-applicable",
						ListenerGuard:    materialize.Guard{File: "application.yaml", Path: "host", Value: "127.0.0.1"},
						ListenerPort:     18080,
						RuntimeConfigDir: true,
					},
					Plan: materialize.Plan{
						SourceDriver:     materialize.SourceApollo,
						Driver:           materialize.DriverYAMLOverlay,
						TargetDir:        "/workspace/.conven/runtime/current/configs/api",
						Application:      "application.yaml",
						RuntimeBootstrap: "config-local.yaml",
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
		"  - Declared remote dependencies: catalog\n" +
		"  - Start groups: partner -> api\n" +
		"  - Connection: ktctl context=dev-cluster namespace=retail\n" +
		"==> Config api\n" +
		"  - Drivers: policy=retail, framework=go-zero, source=apollo, discovery=consul, materializer=yaml-overlay\n" +
		"  - Output: /workspace/.conven/runtime/current/configs/api\n" +
		"  - Local isolation: registration=not-applicable; listener=loopback(127.0.0.1:18080); runtime-config=guarded-bootstrap(config-local.yaml->application.yaml)\n" +
		"  - Consumer guard: kafka=enabled via SERVICE_KAFKA_CONSUMERS_ENABLED (mode=guarded)\n" +
		"  - Local route: partner via partnerRpc (replace)\n" +
		"✓ Dry run complete; no connection, config fetch/materialization, build, process, or state changes were made.\n"
	if output.String() != want {
		t.Fatalf("plan output:\n%s\nwant:\n%s", output.String(), want)
	}
	if strings.Contains(output.String(), "\x1b[") {
		t.Fatalf("non-terminal output contains ANSI: %q", output.String())
	}
	plan.Services["api"].Config.Isolation.ListenerMode = "all-interfaces"
	plan.Services["api"].Config.Isolation.ListenerGuard.Value = "0.0.0.0"
	output.Reset()
	printPlan(&output, plan, true)
	if !strings.Contains(output.String(), "Warning: api listens on 0.0.0.0 across all network interfaces; LAN access is still controlled by the host firewall.") {
		t.Fatalf("all-interfaces warning is missing: %q", output.String())
	}
}

func TestPlannedIsolationDescriptionKeepsRPCListenerAddress(t *testing.T) {
	registration := materialize.Guard{File: "application.yaml", Path: "discovType", Value: ""}
	config := &PlannedConfig{
		Isolation: PlannedIsolation{
			RegistrationMode:  "config",
			RegistrationGuard: &registration,
			ListenerGuard:     materialize.Guard{File: "application.yaml", Path: "listenOn", Value: "127.0.0.1:18081"},
			ListenerPort:      18081,
			RuntimeConfigDir:  true,
		},
		Plan: materialize.Plan{Application: "application.yaml", RuntimeBootstrap: "config-local.yaml"},
	}
	want := "registration=disabled via application.yaml:discovType; listener=loopback(127.0.0.1:18081); runtime-config=guarded-bootstrap(config-local.yaml->application.yaml)"
	if got := plannedIsolationDescription(config); got != want {
		t.Fatalf("isolation description = %q, want %q", got, want)
	}
}

func TestPlannedIsolationDescriptionShowsAllInterfaces(t *testing.T) {
	config := &PlannedConfig{
		Contract: "go-zero-consul-yaml-overlay",
		Isolation: PlannedIsolation{
			RegistrationMode: "not-applicable",
			ListenerMode:     "all-interfaces",
			ListenerGuard:    materialize.Guard{File: "application.yaml", Path: "host", Value: "0.0.0.0"},
			ListenerPort:     18080,
		},
	}
	want := "registration=not-applicable; listener=all-interfaces(0.0.0.0:18080); runtime-config=unverified"
	if got := plannedIsolationDescription(config); got != want {
		t.Fatalf("isolation description = %q, want %q", got, want)
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
