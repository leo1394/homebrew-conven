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

func TestValidateAllInterfacesListener(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		port  int
		valid bool
	}{
		{name: "ipv4 host", value: "0.0.0.0", valid: true},
		{name: "ipv4 listener", value: "0.0.0.0:18081", port: 18081, valid: true},
		{name: "loopback", value: "127.0.0.1:18081", port: 18081},
		{name: "specific interface", value: "192.168.1.10:18081", port: 18081},
		{name: "ipv6 all interfaces", value: "[::]:18081", port: 18081},
		{name: "wrong port", value: "0.0.0.0:18082", port: 18081},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateServiceListener(test.value, test.port, "all-interfaces")
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
	config := &PlannedConfig{Contract: "go-zero-consul-yaml-overlay", Isolation: PlannedIsolation{
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
		Contract: "go-zero-consul-yaml-overlay",
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

func TestValidateSpringBootIsolationAndRuntimeArguments(t *testing.T) {
	target := filepath.Join(t.TempDir(), "configs", "data-mart-service")
	registration := materialize.Guard{File: "application.yml", Path: "service.registration.enabled", Value: false, AllowCreate: true}
	config := &PlannedConfig{
		Framework: "spring-boot",
		Discovery: "consul",
		Plan: materialize.Plan{
			Driver:       materialize.DriverYAMLOverlay,
			SourceDriver: materialize.SourceRepository,
			TargetDir:    target,
			Application:  "application.yml",
		},
		Isolation: PlannedIsolation{
			RegistrationMode:  "config",
			RegistrationGuard: &registration,
			ListenerGuard:     materialize.Guard{File: "application.yml", Path: "grpc.server.address", Value: "127.0.0.1", AllowCreate: true},
			ListenerPort:      18087,
		},
	}
	if err := validatePlannedIsolation("data-mart-service", "rpc", config); err != nil {
		t.Fatal(err)
	}
	run := []string{
		"java", "-jar", "/workspace/data-mart-service/build/libs/datamart.jar",
		"--spring.profiles.active=dev",
		"--spring.config.location=file:" + target + string(filepath.Separator),
		"--service.registration.enabled=false",
		"--spring.cloud.consul.discovery.register=false",
		"--grpc.server.address=127.0.0.1",
		"--grpc.server.port=18087",
	}
	if err := validateRuntimeConfigConsumption("data-mart-service", config, run, nil); err != nil {
		t.Fatal(err)
	}
	if config.Isolation.RuntimeConfigRef != "spring-config-location(application.yml)" {
		t.Fatalf("runtime config reference = %q", config.Isolation.RuntimeConfigRef)
	}

	config.Isolation.ListenerMode = "all-interfaces"
	config.Isolation.ListenerGuard.Value = "0.0.0.0"
	lanRun := append([]string(nil), run...)
	for index, argument := range lanRun {
		if argument == "--grpc.server.address=127.0.0.1" {
			lanRun[index] = "--grpc.server.address=0.0.0.0"
		}
	}
	if err := validatePlannedIsolation("data-mart-service", "rpc", config); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimeConfigConsumption("data-mart-service", config, lanRun, nil); err != nil {
		t.Fatal(err)
	}
	config.Isolation.ListenerMode = ""
	config.Isolation.ListenerGuard.Value = "127.0.0.1"

	tests := []struct {
		name    string
		replace string
		with    string
		message string
	}{
		{name: "registration enabled", replace: "--service.registration.enabled=false", with: "--service.registration.enabled=true", message: "conflicts"},
		{name: "non-loopback", replace: "--grpc.server.address=127.0.0.1", with: "--grpc.server.address=0.0.0.0", message: "conflicts"},
		{name: "wrong port", replace: "--grpc.server.port=18087", with: "--grpc.server.port=9898", message: "conflicts"},
		{name: "wrong config", replace: "--spring.config.location=file:" + target + string(filepath.Separator), with: "--spring.config.location=file:/workspace/resources/", message: "conflicts"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := append([]string(nil), run...)
			for index, argument := range changed {
				if argument == test.replace {
					changed[index] = test.with
				}
			}
			err := validateRuntimeConfigConsumption("data-mart-service", config, changed, nil)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("runtime argument error = %v", err)
			}
		})
	}
	duplicated := append(append([]string(nil), run...), "--grpc.server.port=18087")
	if err := validateRuntimeConfigConsumption("data-mart-service", config, duplicated, nil); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("duplicate protected argument error = %v", err)
	}
}

func TestValidateSpringBootIsolationRejectsUnsafeContract(t *testing.T) {
	registration := materialize.Guard{File: "application.yml", Path: "service.registration.enabled", Value: true}
	config := &PlannedConfig{
		Framework: "spring-boot",
		Discovery: "consul",
		Plan: materialize.Plan{Driver: materialize.DriverYAMLOverlay, SourceDriver: materialize.SourceRepository, Application: "application.yml"},
		Isolation: PlannedIsolation{
			RegistrationMode:  "config",
			RegistrationGuard: &registration,
			ListenerGuard:     materialize.Guard{File: "application.yml", Path: "server.address", Value: "127.0.0.1"},
			ListenerPort:      18080,
		},
	}
	if err := validatePlannedIsolation("api", "http", config); err == nil || !strings.Contains(err.Error(), "to false") {
		t.Fatalf("enabled registration error = %v", err)
	}
	registration.Value = false
	config.Plan.TargetDir = "/runtime/configs/api"
	if err := validatePlannedIsolation("api", "http", config); err != nil {
		t.Fatal(err)
	}
	httpRun := []string{
		"java", "-jar", "/workspace/api.jar",
		"--spring.config.location=file:/runtime/configs/api/",
		"--service.registration.enabled=false",
		"--spring.cloud.consul.discovery.register=false",
		"--server.address=127.0.0.1",
		"--server.port=18080",
	}
	if err := validateRuntimeConfigConsumption("api", config, httpRun, nil); err != nil {
		t.Fatal(err)
	}
	config.Isolation.ListenerGuard.Value = "0.0.0.0"
	if err := validatePlannedIsolation("api", "http", config); err == nil || !strings.Contains(err.Error(), "not a loopback") {
		t.Fatalf("non-loopback error = %v", err)
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
