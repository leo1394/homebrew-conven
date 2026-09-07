package runtime

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/model"
)

func disabledBindingTestService(t *testing.T, application string, guarded bool) PlannedService {
	t.Helper()
	service := externalDependencyTestService(t, application, nil)
	root := t.TempDir()
	service.Directory = root
	service.Workdir = root
	source := "package main\nimport \"github.com/tal-tech/go-zero/zrpc\"\ntype Config struct { Chat zrpc.RpcClientConf `yaml:\"chatclbtRpc\"` }\nfunc enabled(c zrpc.RpcClientConf) bool { return c.DiscovType != \"\" || c.Target != \"\" || len(c.Endpoints) > 0 }\nfunc initClients(c Config) { "
	if guarded {
		source += "if enabled(c.Chat) { zrpc.MustNewClient(c.Chat) }"
	} else {
		source += "zrpc.MustNewClient(c.Chat)"
	}
	source += " }\n"
	for name, contents := range map[string]string{"go.mod": "module example.test/api\ngo 1.22\n", "main.go": source} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return service
}

func TestDisabledBindingCapabilityChecksEverySelectedService(t *testing.T) {
	good := disabledBindingTestService(t, "chatclbtRpc:\n  discovType: ''\n  consul:\n    host: remote\n    key: chatclbt.rpc\n", true)
	bad := disabledBindingTestService(t, "chatclbtRpc:\n  discovType: ''\n", false)
	good.Name = "guarded-api"
	bad.Name = "unguarded-api"
	plan := &Plan{
		Workspace: &WorkspaceData{Manifest: &model.Manifest{Workspace: model.Workspace{DisabledBindings: []string{"chatclbtRpc"}}}},
		Order: []string{good.Name, bad.Name},
		Services: map[string]PlannedService{good.Name: good, bad.Name: bad},
	}
	var output bytes.Buffer
	err := preflightDisabledBindings(plan, &output, true)
	if err == nil || !strings.Contains(err.Error(), "unguarded-api") || !strings.Contains(err.Error(), "chatclbtRpc") {
		t.Fatalf("expected service-specific capability error, got %v", err)
	}
	if !strings.Contains(output.String(), "guarded-api.chatclbtRpc: disabled (guarded)") {
		t.Fatalf("guarded service evidence missing: %s", output.String())
	}
}

func TestDisabledBindingRejectsResidualRoutes(t *testing.T) {
	for _, field := range []string{
		"target: '127.0.0.1:8080'",
		"endpoints: ['127.0.0.1:8080']",
		"endpints: ['127.0.0.1:8080']",
		"etcd: {hosts: ['127.0.0.1:2379']}",
		"etcd: {key: 'chatclbt.rpc'}",
		"discovType: consul",
	} {
		t.Run(field, func(t *testing.T) {
			service := disabledBindingTestService(t, "chatclbtRpc:\n  "+field+"\n", true)
			_, err := (goZeroConsulRuntimeContract{}).ValidateDisabledBindings(service, []string{"chatclbtRpc"}, nil)
			if err == nil || !strings.Contains(err.Error(), "residual") || !strings.Contains(err.Error(), "chatclbtRpc") {
				t.Fatalf("expected residual route diagnostic, got %v", err)
			}
		})
	}
}

func TestDisabledBindingAbsentConfigStillChecksRequiredClient(t *testing.T) {
	service := disabledBindingTestService(t, "name: api\n", false)
	_, err := (goZeroConsulRuntimeContract{}).ValidateDisabledBindings(service, []string{"chatclbtRpc"}, nil)
	if err == nil || !strings.Contains(err.Error(), "chatclbtRpc") {
		t.Fatalf("missing config must not hide unguarded initialization: %v", err)
	}
}

func TestDisabledBindingUnknownSourceFailsClosed(t *testing.T) {
	service := disabledBindingTestService(t, "unknownRpc: {discovType: ''}\n", true)
	_, err := (goZeroConsulRuntimeContract{}).ValidateDisabledBindings(service, []string{"unknownRpc"}, nil)
	if err == nil || !strings.Contains(err.Error(), "no verifiable") {
		t.Fatalf("unknown source capability must fail closed: %v", err)
	}
}

func TestDisabledBindingUnsupportedAdapterFailsClosed(t *testing.T) {
	plan := &Plan{
		Workspace: &WorkspaceData{Manifest: &model.Manifest{
			Workspace: model.Workspace{DisabledBindings: []string{"chatclbtRpc"}},
			Services: map[string]model.Service{"api": {Discovery: model.ServiceDiscovery{ConsumerBindings: []string{"chatclbtRpc"}}}},
		}},
		Order: []string{"api"},
		Services: map[string]PlannedService{"api": {Name: "api"}},
	}
	var output bytes.Buffer
	err := preflightDisabledBindings(plan, &output, true)
	if err == nil || !strings.Contains(err.Error(), "cannot verify") {
		t.Fatalf("unsupported adapter must fail closed: %v", err)
	}
	plan.Workspace.Manifest.Services["api"] = model.Service{Dependencies: map[string]model.Dependency{"chat": {Binding: "chatclbtRpc"}}}
	err = preflightDisabledBindings(plan, &output, true)
	if err == nil || !strings.Contains(err.Error(), "cannot verify") || !strings.Contains(err.Error(), "chatclbtRpc") {
		t.Fatalf("dependency-only binding on unsupported selected service must fail closed: %v", err)
	}
}

func TestDisabledBindingUnknownRequestAndUnselectedOwner(t *testing.T) {
	service := disabledBindingTestService(t, "name: api\n", true)
	plan := &Plan{
		Workspace: &WorkspaceData{Manifest: &model.Manifest{
			Workspace: model.Workspace{DisabledBindings: []string{"typoRpc"}},
			Services: map[string]model.Service{"other": {Discovery: model.ServiceDiscovery{ConsumerBindings: []string{"otherRpc"}}}},
		}},
		Order: []string{"api"},
		Services: map[string]PlannedService{"api": service},
	}
	var output bytes.Buffer
	err := preflightDisabledBindings(plan, &output, true)
	if err == nil || !strings.Contains(err.Error(), "unknown disabled binding typoRpc") {
		t.Fatalf("unknown requested binding must not pass: %v", err)
	}
	plan.Workspace.Manifest.Workspace.DisabledBindings = []string{"otherRpc"}
	if err := preflightDisabledBindings(plan, &output, true); err != nil {
		t.Fatalf("known binding owned by unselected service is not a typo: %v", err)
	}
}

func TestStartDisabledBindingPreflightFailsBeforeBuild(t *testing.T) {
	service := disabledBindingTestService(t, "host: 0.0.0.0\nport: 8080\nchatclbtRpc: {discovType: consul, consul: {host: remote, port: 8500, key: chatclbt.rpc}}\n", false)
	resources := filepath.Join(service.Directory, "resources")
	if err := os.Mkdir(resources, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(service.Config.Plan.TargetDir, "application.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string][]byte{"application.yaml": data, "config-dev.yaml": []byte("appId: test\n")} {
		if err := os.WriteFile(filepath.Join(resources, name), contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(service.Directory, "build.marker")
	workspace := testWorkspace(t, service.Directory, &model.Manifest{
		Version: 1,
		Workspace: model.Workspace{Name: "disabled-preflight", Policy: "retail", DisabledBindings: []string{"chatclbtRpc"}},
		Policies: map[string]model.Policy{"retail": {
			Drivers: model.PolicyDrivers{Framework: "go-zero", ConfigSource: "repository", Discovery: "consul", Materializer: "yaml-overlay"},
			Config: model.PolicyConfig{SourceDir: "resources", Application: "application.yaml", Bootstrap: "config-dev.yaml", RuntimeBootstrap: "config-local.yaml"},
			Process: model.PolicyProcess{Env: map[string]string{"PROFILE_ACTIVE": "local"}, Args: []string{"-f", "${configDir}"}},
			Routing: model.PolicyRouting{Servers: map[string]model.ServerRoute{"http": {
				Port: "http",
				Isolation: model.ServerIsolation{Registration: model.RegistrationGuard{Mode: "not-applicable"}, Listener: model.ListenerGuard{Path: "host", Value: "127.0.0.1"}},
			}}},
		}},
		Services: map[string]model.Service{"api": {
			Path: ".", Kind: "http", Ports: map[string]int{"http": 18080},
			Runner: model.Runner{Build: []string{"touch", marker}, Run: []string{"/usr/bin/false"}},
		}},
	})
	var output bytes.Buffer
	_, err = Start(context.Background(), workspace, StartOptions{Services: []string{"api"}, Output: &output})
	if err == nil || !strings.Contains(err.Error(), "chatclbtRpc") || !strings.Contains(err.Error(), "capability") {
		t.Fatalf("expected disabled capability failure before build: %v\n%s", err, output.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("build ran despite failed capability preflight: %v", err)
	}
	if strings.Contains(output.String(), "Building api") || strings.Contains(output.String(), "Starting api") {
		t.Fatalf("build/start stage reached after preflight failure: %s", output.String())
	}
}
