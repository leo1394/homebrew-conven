package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/config"
	"github.com/leo1394/homebrew-conven/internal/materialize"
)

func TestValidateLoopbackListener(t *testing.T) {
	tests := []struct {
		name  string
		value string
		port  int
		valid bool
	}{
		{name: "ipv4 host", value: "127.0.0.1", valid: true},
		{name: "ipv4 listener", value: "127.0.0.1:18081", port: 18081, valid: true},
		{name: "ipv4 loopback range", value: "127.0.0.2:18081", port: 18081, valid: true},
		{name: "ipv6 host", value: "::1", valid: true},
		{name: "ipv6 listener", value: "[::1]:18081", port: 18081, valid: true},
		{name: "all interfaces", value: "0.0.0.0:18081", port: 18081},
		{name: "hostname", value: "localhost:18081", port: 18081},
		{name: "wrong port", value: "127.0.0.1:18082", port: 18081},
		{name: "surrounding whitespace", value: " 127.0.0.1:18081 ", port: 18081},
		{name: "empty", value: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateLoopbackListener(test.value, test.port)
			if test.valid && err != nil {
				t.Fatalf("valid listener rejected: %v", err)
			}
			if !test.valid && err == nil {
				t.Fatalf("unsafe listener %q accepted", test.value)
			}
		})
	}
}

func TestValidatePlannedIsolationFailsClosedForRPC(t *testing.T) {
	if err := validatePlannedIsolation("partner", "rpc", nil); err == nil || !strings.Contains(err.Error(), "no policy-backed") {
		t.Fatalf("missing policy error = %v", err)
	}
	if err := validatePlannedIsolation("portal", "http", nil); err == nil || !strings.Contains(err.Error(), "no policy-backed") {
		t.Fatalf("missing HTTP policy error = %v", err)
	}
	config := &PlannedConfig{Isolation: PlannedIsolation{
		RegistrationMode: "not-applicable",
		ListenerGuard:    materialize.Guard{File: "application.yaml", Path: "listenOn", Value: "127.0.0.1:18081"},
		ListenerPort:     18081,
	}}
	if err := validatePlannedIsolation("partner", "rpc", config); err == nil || !strings.Contains(err.Error(), "must verify disabled registration") {
		t.Fatalf("not-applicable RPC registration error = %v", err)
	}
}

func TestValidatePlannedIsolationRejectsUntrustedDisabledValue(t *testing.T) {
	registration := materialize.Guard{File: "application.yaml", Path: "discovType", Value: "consul"}
	config := &PlannedConfig{
		Framework: "go-zero",
		Discovery: "consul",
		Plan: materialize.Plan{
			Driver:      materialize.DriverYAMLOverlay,
			Application: "application.yaml",
		},
		Isolation: PlannedIsolation{
			RegistrationMode:  "config",
			RegistrationGuard: &registration,
			ListenerGuard:     materialize.Guard{File: "application.yaml", Path: "listenOn", Value: "127.0.0.1:18081"},
			ListenerPort:      18081,
		},
	}
	if err := validatePlannedIsolation("partner", "rpc", config); err == nil || !strings.Contains(err.Error(), "empty string") {
		t.Fatalf("unsafe disabled value error = %v", err)
	}
}

func TestValidatePlannedIsolationEnforcesTrustedListenerFieldsAndShape(t *testing.T) {
	registration := materialize.Guard{File: "application.yaml", Path: "discovType", Value: ""}
	config := &PlannedConfig{
		Framework: "go-zero",
		Discovery: "consul",
		Plan: materialize.Plan{
			Driver:      materialize.DriverYAMLOverlay,
			Application: "application.yaml",
		},
		Isolation: PlannedIsolation{
			RegistrationMode:  "config",
			RegistrationGuard: &registration,
			ListenerGuard:     materialize.Guard{File: "application.yaml", Path: "harmlessField", Value: "127.0.0.1:18081"},
			ListenerPort:      18081,
		},
	}
	if err := validatePlannedIsolation("partner", "rpc", config); err == nil || !strings.Contains(err.Error(), "application.yaml:listenOn") {
		t.Fatalf("wrong listener path error = %v", err)
	}
	config.Isolation.ListenerGuard.Path = "listenOn"
	config.Isolation.ListenerGuard.Value = "127.0.0.1"
	if err := validatePlannedIsolation("partner", "rpc", config); err == nil || !strings.Contains(err.Error(), "include its declared port") {
		t.Fatalf("RPC listener shape error = %v", err)
	}
	httpConfig := &PlannedConfig{
		Framework: "go-zero",
		Discovery: "consul",
		Plan: materialize.Plan{
			Driver:      materialize.DriverYAMLOverlay,
			Application: "application.yaml",
		},
		Isolation: PlannedIsolation{
			RegistrationMode: "not-applicable",
			ListenerGuard:    materialize.Guard{File: "application.yaml", Path: "host", Value: "127.0.0.1:18080"},
			ListenerPort:     18080,
		},
	}
	if err := validatePlannedIsolation("portal", "http", httpConfig); err == nil || !strings.Contains(err.Error(), "without a port") {
		t.Fatalf("HTTP listener shape error = %v", err)
	}
}

func TestValidateRuntimeConfigConsumptionRequiresVerifiedRuntimePath(t *testing.T) {
	target := filepath.Join(t.TempDir(), "configs", "api")
	config := &PlannedConfig{
		Plan: materialize.Plan{
			TargetDir:        target,
			Application:      "application.yaml",
			RuntimeBootstrap: "config-local.yaml",
		},
		Isolation: PlannedIsolation{RegistrationMode: "not-applicable", RuntimeConfigDir: true},
	}
	localEnvironment := map[string]string{"PROFILE_ACTIVE": "local"}
	if err := validateRuntimeConfigConsumption("api", config, []string{"api", "-f", target}, localEnvironment); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimeConfigConsumption("api", config, []string{"api"}, localEnvironment); err == nil || !strings.Contains(err.Error(), "does not pass") {
		t.Fatalf("missing runtime config reference error = %v", err)
	}
	if err := validateRuntimeConfigConsumption("api", config, []string{"api", "-f", "/workspace/api/resources", "-f", target}, localEnvironment); err == nil || !strings.Contains(err.Error(), "unverified") {
		t.Fatalf("unsafe runtime config reference error = %v", err)
	}
	for _, run := range [][]string{
		{"api", "--", "-f", target},
		{"api", "positional", "-f", target},
		{"api", "-name", "value", "-f", target},
	} {
		if err := validateRuntimeConfigConsumption("api", config, run, localEnvironment); err == nil {
			t.Fatalf("unparsed runtime config flag accepted: %#v", run)
		}
	}
	if err := validateRuntimeConfigConsumption("api", config, []string{"api", "-f", target}, map[string]string{"PROFILE_ACTIVE": "dev"}); err == nil || !strings.Contains(err.Error(), "unverified") {
		t.Fatalf("non-local profile runtime config error = %v", err)
	}
}

func TestPlanRuntimeConfigGuardsRequiresLocalBootstrapFilename(t *testing.T) {
	plan := materialize.Plan{
		TargetDir:        "/runtime/configs/api",
		Application:      "application.yaml",
		RuntimeBootstrap: "config-dev.yaml",
	}
	guards, err := planRuntimeConfigGuards(plan, config.ExpandContext{})
	if err != nil {
		t.Fatal(err)
	}
	if len(guards) != 0 {
		t.Fatalf("non-local bootstrap guards = %#v", guards)
	}
	plan.RuntimeBootstrap = "config-local.yaml"
	guards, err = planRuntimeConfigGuards(plan, config.ExpandContext{})
	if err != nil {
		t.Fatal(err)
	}
	if len(guards) != 2 || guards[0].Path != "localConfigEnable" || guards[0].Value != true || guards[1].Path != "localConfigPath" || guards[1].Value != "/runtime/configs/api/application.yaml" {
		t.Fatalf("local bootstrap guards = %#v", guards)
	}
}

func TestValidateInboundRoutingRejectsUnverifiableCommand(t *testing.T) {
	if err := validateInboundRouting(ConnectionConfig{Driver: "ktctl"}); err != nil {
		t.Fatal(err)
	}
	if err := validateInboundRouting(ConnectionConfig{Driver: "command"}); err == nil || !strings.Contains(err.Error(), "cannot prove") {
		t.Fatalf("command driver error = %v", err)
	}
}

func TestAppendIsolationEvidenceRecordsNoConfigurationValues(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "service.log")
	registration := materialize.Guard{File: "application.yaml", Path: "discovType", Value: ""}
	service := PlannedService{
		Name:    "partner",
		LogPath: logPath,
		Config: &PlannedConfig{
			Isolation: PlannedIsolation{
				RegistrationMode:  "config",
				RegistrationGuard: &registration,
				ListenerGuard:     materialize.Guard{File: "application.yaml", Path: "listenOn", Value: "127.0.0.1:18081"},
				RuntimeConfigDir:  true,
			},
			Plan: materialize.Plan{Application: "application.yaml", RuntimeBootstrap: "config-local.yaml"},
		},
	}
	if err := appendIsolationEvidence(service, ConnectionConfig{Driver: "ktctl"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, expected := range []string{"registration=disabled(application.yaml:discovType)", "listener=loopback(127.0.0.1:18081)", "runtime-config=guarded-bootstrap(config-local.yaml->application.yaml)", "remote-inbound=disabled(connection=ktctl-connect-only)"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("isolation evidence %q is missing %q", text, expected)
		}
	}
}
