package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/model"
)

const validManifestYAML = `version: 2
workspace:
  name: sample
environments:
  dev:
    registry: nacos
    env:
      PROFILE: dev
    connection:
      driver: ktctl
services:
  api:
    path: services/api
    runner:
      runWorkdir: ${runDir}/configs/api
      run: [go, run, .]
    ports:
      rpc: 18080
    dependencies:
      db:
        localEnv:
          DB_ADDRESS: 127.0.0.1:${services.db.ports.tcp}
  db:
    path: services/db
    runner:
      run: [go, run, .]
    ports:
      tcp: 15432
`

const policyManifestYAML = `version: 2
workspace:
  name: sample
  policy: retail
policies:
  retail:
    drivers:
      framework: go-zero
      configSource: repository
      discovery: consul
      materializer: yaml-overlay
    config:
      sourceDir: resources
      application: application.yaml
      patches:
        - file: config-local.yaml
          path: localConfigEnable
          value: true
    process:
      env:
        PROFILE_ACTIVE: local
      args: [-f, "${configDir}"]
    routing:
      servers:
        rpc:
          port: rpc
          isolation:
            registration:
              mode: config
              path: discovType
              disabledValue: ""
            listener:
              path: listenOn
              value: "127.0.0.1:${port.rpc}"
      localDependency:
        mode: replace
        value:
          target: "127.0.0.1:${dependency.port}"
      remoteDependency:
        mode: preserve
services:
  api:
    path: services/api
    kind: rpc
    discovery:
      analyzer: go-subdirectory
      bindings: [dbRpc]
    runner:
      run: [api]
    ports:
      rpc: 18080
    config:
      patches:
        - path: feature.enabled
          value: true
    dependencies:
      db:
        binding: dbRpc
        port: rpc
  db:
    path: services/db
    kind: rpc
    runner:
      run: [db]
    ports:
      rpc: 18081
`

func TestLoadValidManifest(t *testing.T) {
	path := writeManifest(t, validManifestYAML)

	manifest, err := Load(path)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if manifest.Version != 2 {
		t.Fatalf("version = %d, want 2", manifest.Version)
	}
	if manifest.Workspace.Name != "sample" {
		t.Fatalf("workspace name = %q, want sample", manifest.Workspace.Name)
	}
	if got := ServiceNames(manifest); !reflect.DeepEqual(got, []string{"api", "db"}) {
		t.Fatalf("service names = %#v, want [api db]", got)
	}
	if manifest.Services["api"].Runner.RunWorkdir != "${runDir}/configs/api" {
		t.Fatalf("runWorkdir = %q", manifest.Services["api"].Runner.RunWorkdir)
	}
}

func TestLoadAcceptsVersionOneManifest(t *testing.T) {
	manifest, err := Load(writeManifest(t, strings.Replace(validManifestYAML, "version: 2", "version: 1", 1)))
	if err != nil {
		t.Fatalf("Load version 1 manifest: %v", err)
	}
	if manifest.Version != 1 || !reflect.DeepEqual(ServiceNames(manifest), []string{"api", "db"}) {
		t.Fatalf("legacy manifest = %#v", manifest)
	}
}

func TestLoadRejectsUnknownYAMLField(t *testing.T) {
	yaml := strings.Replace(validManifestYAML, "      run: [go, run, .]", "      run: [go, run, .]\n      unknownRunnerField: true", 1)

	_, err := Load(writeManifest(t, yaml))
	if err == nil || !strings.Contains(err.Error(), "field unknownRunnerField not found") {
		t.Fatalf("error = %v, want strict unknown field error", err)
	}
}

func TestLoadPolicyAndCentralServiceDescriptions(t *testing.T) {
	manifest, err := Load(writeManifest(t, policyManifestYAML))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if manifest.Workspace.Policy != "retail" {
		t.Fatalf("workspace policy = %q", manifest.Workspace.Policy)
	}
	policy := manifest.Policies["retail"]
	if policy.Drivers.ConfigSource != "repository" || policy.Drivers.Materializer != "yaml-overlay" {
		t.Fatalf("policy drivers = %#v", policy.Drivers)
	}
	api := manifest.Services["api"]
	if api.Kind != "rpc" || api.Discovery.Analyzer != "go-subdirectory" || !reflect.DeepEqual(api.Discovery.Bindings, []string{"dbRpc"}) {
		t.Fatalf("api description = %#v", api)
	}
	if api.Dependencies["db"].Binding != "dbRpc" || api.Dependencies["db"].Port != "rpc" {
		t.Fatalf("api dependency = %#v", api.Dependencies["db"])
	}
}

func TestLoadValidatesServiceListenerScope(t *testing.T) {
	configured := strings.Replace(policyManifestYAML, "    kind: rpc\n", "    kind: rpc\n    network:\n      listen: all-interfaces\n", 1)
	manifest, err := Load(writeManifest(t, configured))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Services["api"].Network.Listen != "all-interfaces" {
		t.Fatalf("listener scope = %q", manifest.Services["api"].Network.Listen)
	}

	invalid := strings.Replace(configured, "listen: all-interfaces", "listen: public", 1)
	if _, err := Load(writeManifest(t, invalid)); err == nil || !strings.Contains(err.Error(), "must be loopback or all-interfaces") {
		t.Fatalf("invalid listener scope error = %v", err)
	}

	untyped := strings.Replace(validManifestYAML, "    path: services/api\n", "    path: services/api\n    network:\n      listen: all-interfaces\n", 1)
	if _, err := Load(writeManifest(t, untyped)); err == nil || !strings.Contains(err.Error(), "requires a typed service kind") {
		t.Fatalf("untyped listener scope error = %v", err)
	}
}

func TestLoadRejectsUnknownNestedPolicyField(t *testing.T) {
	yaml := strings.Replace(policyManifestYAML, "      materializer: yaml-overlay", "      materializer: yaml-overlay\n      companyMagic: true", 1)

	_, err := Load(writeManifest(t, yaml))
	if err == nil || !strings.Contains(err.Error(), "field companyMagic not found") {
		t.Fatalf("error = %v, want strict nested policy field error", err)
	}
}

func TestLoadValidatesServerIsolation(t *testing.T) {
	tests := []struct {
		name      string
		yaml      string
		wantError string
	}{
		{
			name:      "missing registration mode",
			yaml:      strings.Replace(policyManifestYAML, "              mode: config\n", "", 1),
			wantError: "registration.mode must be config or not-applicable",
		},
		{
			name:      "missing registration path",
			yaml:      strings.Replace(policyManifestYAML, "              path: discovType\n", "", 1),
			wantError: "registration.path is required",
		},
		{
			name:      "missing disabled value",
			yaml:      strings.Replace(policyManifestYAML, "              disabledValue: \"\"\n", "", 1),
			wantError: "registration.disabledValue is required",
		},
		{
			name:      "missing listener",
			yaml:      strings.Replace(policyManifestYAML, "            listener:\n              path: listenOn\n              value: \"127.0.0.1:${port.rpc}\"\n", "", 1),
			wantError: "listener.path is required",
		},
		{
			name:      "not applicable registration fields",
			yaml:      strings.Replace(policyManifestYAML, "mode: config", "mode: not-applicable", 1),
			wantError: "must not declare file, path, or disabledValue",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(writeManifest(t, test.yaml))
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want error containing %q", err, test.wantError)
			}
		})
	}
}

func TestLoadRejectsWorkspaceStateDir(t *testing.T) {
	yaml := strings.Replace(validManifestYAML, "  name: sample", "  name: sample\n  stateDir: ${workspace}/.conven-state", 1)

	_, err := Load(writeManifest(t, yaml))
	if err == nil || !strings.Contains(err.Error(), "field stateDir not found") {
		t.Fatalf("error = %v, want workspace.stateDir to be rejected as an unknown field", err)
	}
}

func TestLoadRejectsMultipleYAMLDocuments(t *testing.T) {
	_, err := Load(writeManifest(t, validManifestYAML+"---\nversion: 2\n"))
	if err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("error = %v, want multiple document error", err)
	}
}

func TestLoadValidatesManifest(t *testing.T) {
	tests := []struct {
		name      string
		yaml      string
		wantError string
	}{
		{
			name:      "future version",
			yaml:      strings.Replace(validManifestYAML, "version: 2", "version: 4", 1),
			wantError: "version must be 1, 2, or 3",
		},
		{
			name:      "workspace name",
			yaml:      strings.Replace(validManifestYAML, "name: sample", "name: \"\"", 1),
			wantError: "workspace.name is required",
		},
		{
			name:      "service path",
			yaml:      strings.Replace(validManifestYAML, "path: services/api", "path: \"\"", 1),
			wantError: "services.api.path is required",
		},
		{
			name:      "run command",
			yaml:      strings.Replace(validManifestYAML, "run: [go, run, .]", "run: []", 1),
			wantError: "services.api.runner.run is required",
		},
		{
			name:      "run executable",
			yaml:      strings.Replace(validManifestYAML, "run: [go, run, .]", "run: [\"\", arg]", 1),
			wantError: "services.api.runner.run executable is required",
		},
		{
			name:      "empty run argument",
			yaml:      strings.Replace(validManifestYAML, "run: [go, run, .]", "run: [go, \"\"]", 1),
			wantError: "argument 1 must not be empty",
		},
		{
			name:      "port range",
			yaml:      strings.Replace(validManifestYAML, "rpc: 18080", "rpc: 0", 1),
			wantError: "must be between 1 and 65535",
		},
		{
			name:      "unknown dependency",
			yaml:      strings.Replace(validManifestYAML, "      db:\n        localEnv:", "      missing:\n        localService: missing\n        localEnv:", 1),
			wantError: "localService references unknown service",
		},
		{
			name:      "self dependency",
			yaml:      strings.Replace(validManifestYAML, "      db:\n        localEnv:", "      api:\n        localEnv:", 1),
			wantError: "must not reference itself",
		},
		{
			name:      "unknown workspace policy",
			yaml:      strings.Replace(policyManifestYAML, "policy: retail", "policy: missing", 1),
			wantError: "workspace.policy references unknown policy",
		},
		{
			name:      "invalid disabled binding",
			yaml:      strings.Replace(validManifestYAML, "  name: sample", "  name: sample\n  disabledBindings: [\"bad binding\"]", 1),
			wantError: "workspace.disabledBindings[0]",
		},
		{
			name:      "duplicate disabled binding",
			yaml:      strings.Replace(validManifestYAML, "  name: sample", "  name: sample\n  disabledBindings: [legacyRpc, legacyRpc]", 1),
			wantError: "duplicates \"legacyRpc\"",
		},
		{
			name:      "dependency binding without port",
			yaml:      strings.Replace(policyManifestYAML, "        binding: dbRpc\n        port: rpc\n", "        binding: dbRpc\n", 1),
			wantError: "binding and port must be declared together",
		},
		{
			name:      "dependency unknown named port",
			yaml:      strings.Replace(policyManifestYAML, "        binding: dbRpc\n        port: rpc\n", "        binding: dbRpc\n        port: http\n", 1),
			wantError: "references unknown port",
		},
		{
			name:      "unsupported materializer",
			yaml:      strings.Replace(policyManifestYAML, "materializer: yaml-overlay", "materializer: shell", 1),
			wantError: "must be yaml-overlay",
		},
		{
			name:      "unsafe config source",
			yaml:      strings.Replace(policyManifestYAML, "sourceDir: resources", "sourceDir: ../resources", 1),
			wantError: "must stay within its config directory",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(writeManifest(t, test.yaml))
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want error containing %q", err, test.wantError)
			}
		})
	}
}

func TestLoadRejectsUnsafeServiceNames(t *testing.T) {
	for _, name := range []string{".", "..", "../api", `api\worker`, "api:worker", "-api", "服务"} {
		t.Run(name, func(t *testing.T) {
			yaml := strings.Replace(validManifestYAML, "  api:\n", fmt.Sprintf("  %q:\n", name), 1)
			_, err := Load(writeManifest(t, yaml))
			if err == nil || !strings.Contains(err.Error(), "service name") {
				t.Fatalf("error = %v, want unsafe service name error", err)
			}
		})
	}
}

func TestServiceNamesAndValidateSelection(t *testing.T) {
	manifest := &model.Manifest{Services: map[string]model.Service{
		"payment": {},
		"order":   {},
		"user":    {},
	}}

	if got := ServiceNames(manifest); !reflect.DeepEqual(got, []string{"order", "payment", "user"}) {
		t.Fatalf("ServiceNames = %#v, want sorted names", got)
	}
	if err := ValidateSelection(manifest, []string{"user", "order"}); err != nil {
		t.Fatalf("ValidateSelection returned error for valid selection: %v", err)
	}
	if err := ValidateSelection(manifest, nil); err != nil {
		t.Fatalf("ValidateSelection returned error for empty selection: %v", err)
	}

	err := ValidateSelection(manifest, []string{"missing", "user", "user"})
	if err == nil {
		t.Fatal("ValidateSelection accepted duplicate and unknown services")
	}
	if !strings.Contains(err.Error(), "duplicate services: user") {
		t.Fatalf("error = %v, want duplicate service detail", err)
	}
	if !strings.Contains(err.Error(), "unknown services: missing") {
		t.Fatalf("error = %v, want unknown service detail", err)
	}
}

func writeManifest(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conven.yaml")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return path
}

func TestManifestKafkaConsumerRequiresDisabledIsolation(t *testing.T) {
	manifest := &model.Manifest{
		Version: 3,
		Workspace: model.Workspace{Name: "sample"},
		Policies: map[string]model.Policy{
			"go-passive": {
				Drivers: model.PolicyDrivers{Runtime: "go-generic", Framework: "go", ConfigSource: "environment", Discovery: "passive", Materializer: "environment"},
				Routing: model.PolicyRouting{Servers: map[string]model.ServerRoute{"http": {
					Port: "http", Env: map[string]string{"HOST": "127.0.0.1", "PORT": "${port.http}"},
					Isolation: model.ServerIsolation{Registration: model.RegistrationGuard{Mode: "not-applicable"}, Listener: model.ListenerGuard{Path: "HOST", Value: "127.0.0.1"}},
				}}},
			},
		},
		Services: map[string]model.Service{
			"api": {
				Path: "api", Policy: "go-passive", Kinds: []string{"http"},
				Discovery: model.ServiceDiscovery{Analyzer: "go-root-module", Certifier: "go-generic", Consumers: []string{"kafka"}},
				Runner: model.Runner{Run: []string{"api"}}, Ports: map[string]int{"http": 18080},
				HealthChecks: []model.ServiceHealthCheck{{Server: "http", Type: "tcp", Address: "127.0.0.1:18080"}},
			},
		},
	}
	err := validateManifest(manifest)
	if err == nil || !strings.Contains(err.Error(), "isolation.consumers.kafka") {
		t.Fatalf("missing Kafka isolation error = %v", err)
	}
	service := manifest.Services["api"]
	service.Isolation.Consumers = map[string]model.ConsumerIsolation{"kafka": {Mode: "guarded", Env: "SERVICE_KAFKA_CONSUMERS_ENABLED"}}
	manifest.Services["api"] = service
	if err := validateManifest(manifest); err != nil {
		t.Fatalf("valid Kafka isolation rejected: %v", err)
	}
}
