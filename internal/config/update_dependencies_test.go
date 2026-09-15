package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestUpdateDependencyMissingProviderDiagnosticAndRecovery(t *testing.T) {
	root := t.TempDir()
	writeDiscoveryFile(t, filepath.Join(root, "portal", "resources", "application.yaml"), "storeLayoutRpc: {discovType: consul, consul: {key: store-layout.rpc}}\n")
	manifest := &model.Manifest{
		Policies: map[string]model.Policy{"files": {Drivers: model.PolicyDrivers{Materializer: "yaml-overlay"}, Config: model.PolicyConfig{SourceDir: "resources", Application: "application.yaml"}}},
		Services: map[string]model.Service{"portal": {Path: "portal", Policy: "files"}},
		Environments: map[string]model.Environment{"dev": {}, "test": {}},
	}
	scanned := []DiscoveredService{{Path: "portal", Bindings: []string{"storeLayoutRpc"}}}
	_, notes, err := synchronizeApplicationDependencies(manifest, root, scanned)
	if err != nil || len(notes) != 1 { t.Fatalf("notes=%v err=%v", notes, err) }
	for _, part := range []string{"portal", "storeLayoutRpc", "application.yaml", "store-layout.rpc", "no workspace provider", "remote binding preserved"} {
		if !strings.Contains(notes[0], part) { t.Fatalf("missing %q: %v", part, notes) }
	}
	manifest.Services["store-layout-service"] = model.Service{Path: "store-layout-service", Ports: map[string]int{"rpc": 18090}, Discovery: model.ServiceDiscovery{Identity: "store-layout.rpc"}}
	_, notes, err = synchronizeApplicationDependencies(manifest, root, scanned)
	if err != nil || len(notes) != 0 { t.Fatalf("notes=%v err=%v", notes, err) }
	if manifest.Services["portal"].Dependencies["store-layout-service"].Binding != "storeLayoutRpc" { t.Fatal("dependency was not automatically created") }
	for name, environment := range manifest.Environments { if environment.Resolutions["portal"]["store-layout-service"].Mode != "remote" { t.Fatalf("missing resolution in %s", name) } }
}

func TestUpdateWorkspaceRepairsPortalScenario(t *testing.T) {
	workspace := t.TempDir()
	writeGoServiceRepository(t, workspace, "portal", false, "example.com/portal", "main")
	writeDiscoveryFile(t, filepath.Join(workspace, "portal", "go", "go.mod"), "module example.com/portal\nrequire github.com/tal-tech/go-zero v1.0.0\n")
	writeDiscoveryFile(t, filepath.Join(workspace, "portal", "go", "main.go"), "package main\nimport \"flag\"\nfunc main() { dir := flag.String(\"f\", \"../resources\", \"config\"); flag.Parse(); start(*dir) }\nfunc start(string) {}\n")
	writeDiscoveryFile(t, filepath.Join(workspace, "portal", "go", "config", "config.go"), "package config\nimport \"github.com/tal-tech/go-zero/zrpc\"\ntype Config struct { rest.RestConf; StoreLayoutRpc zrpc.RpcClientConf `yaml:\"storeLayoutRpc\"` }\n")
	sourcePath := filepath.Join(workspace, "portal", "go", "svc", "service_context.go")
	writeDiscoveryFile(t, sourcePath, "package svc\nimport \"example.com/portal/config\"\nfunc NewServiceContext(c config.Config) {\n\tif c.StoreLayoutRpc.DiscovType != \"\" { mustNewClient(c.StoreLayoutRpc) }\n}\n")
	writeDiscoveryFile(t, filepath.Join(workspace, "portal", "resources", "application.yaml"), "storeLayoutRpc: {discovType: consul, consul: {key: store-layout.rpc}}\n")
	path := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, path, `version: 3
workspace: {name: portal-regression}
environments:
  dev: {}
  test: {}
policies:
  files:
    drivers: {runtime: go-zero, framework: go-zero, discovery: consul, configSource: repository, materializer: yaml-overlay}
    config: {sourceDir: resources, application: application.yaml}
    routing:
      localDependency:
        mode: replace
        value: {target: '127.0.0.1:${dependency.port}'}
      servers:
        http:
          port: http
          isolation:
            registration: {mode: not-applicable}
            listener: {path: host, value: 127.0.0.1}
services:
  portal:
    path: portal
    policy: files
    runner: {run: [custom-runner], workdir: go}
    discovery: {analyzer: go-subdirectory-module, certifier: go-zero}
  store-layout-service:
    path: store-layout-service
    runner: {run: [custom-runner]}
    ports: {rpc: 18090}
    discovery: {identity: store-layout.rpc}
`)
	result, err := UpdateWorkspace(path, workspace, false)
	if err != nil { t.Fatal(err) }
	if len(result.SourceRepairs) != 1 { t.Fatalf("repairs=%v notes=%v", result.SourceRepairs, result.DependencyNotes) }
	manifest, err := Load(path)
	if err != nil { t.Fatal(err) }
	if manifest.Services["portal"].Dependencies["store-layout-service"].Binding != "storeLayoutRpc" { t.Fatal("missing automatically synchronized dependency") }
	if err := InspectGoRPCTargetInitialization(filepath.Join(workspace, "portal"), "go", "storeLayoutRpc"); err != nil { t.Fatal(err) }
	before, _ := os.ReadFile(sourcePath)
	result, err = UpdateWorkspace(path, workspace, false)
	if err != nil || len(result.SourceRepairs) != 0 { t.Fatalf("second update: %v %v", result, err) }
	assertFileContents(t, sourcePath, string(before))
}

func TestUpdateWorkspaceSynchronizesApplicationDependencies(t *testing.T) {
	workspace := t.TempDir()
	writeGoServiceRepository(t, workspace, "sa", false, "example.com/sa", "main")
	writeDiscoveryFile(t, filepath.Join(workspace, "sa", "go", "go.mod"), "module example.com/sa\nrequire github.com/tal-tech/go-zero v1.0.0\n")
	writeDiscoveryFile(t, filepath.Join(workspace, "sa", "go", "main.go"), "package main\nimport \"flag\"\nfunc main() { dir := flag.String(\"f\", \"../resources\", \"config\"); flag.Parse(); start(*dir) }\nfunc start(string) {}\n")
	writeDiscoveryFile(t, filepath.Join(workspace, "sa", "go", "config", "config.go"), "package config\ntype Config struct { rest.RestConf }\n")
	path := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, path, `version: 3
workspace:
  name: test
environments:
  dev:
    resolutions: &routes
      sa:
        mdm: {mode: remote}
  test:
    resolutions: *routes
policies:
  files:
    drivers: {runtime: go-zero, framework: go-zero, discovery: consul, configSource: repository, materializer: yaml-overlay}
    config: {sourceDir: resources, application: application.yaml}
    routing:
      servers:
        http:
          port: http
          isolation:
            registration: {mode: not-applicable}
            listener: {path: host, value: 127.0.0.1}
services:
  sa:
    path: sa
    policy: files
    runner: {run: [custom-runner]}
    discovery:
      analyzer: go-subdirectory-module
      certifier: go-zero
      consumerBindings: [mdmRpc, pigeonRpc, storeLayoutRpc]
    dependencies:
      mdm: {localService: mdm, binding: mdmRpc, port: rpc}
      store: {localService: store, binding: storeLayoutRpc, port: rpc}
  mdm:
    path: mdm
    runner: {run: [custom-runner]}
    ports: {rpc: 18089}
    discovery: {identity: mdm.rpc}
  store:
    path: store
    runner: {run: [custom-runner]}
    ports: {rpc: 18090}
`)
	application := filepath.Join(workspace, "sa", "resources", "application.yaml")
	writeDiscoveryFile(t, application, "# mdmRpc:\n# pigeonRpc:\nstoreLayoutRpc:\n  endpoints: [127.0.0.1:9000]\n")
	if _, err := UpdateWorkspace(path, workspace, false); err != nil { t.Fatal(err) }
	manifest, err := Load(path)
	if err != nil { t.Fatal(err) }
	if !reflect.DeepEqual(manifest.Services["sa"].Discovery.ConsumerBindings, []string{"storeLayoutRpc"}) { t.Fatal("stale consumer bindings") }
	if len(manifest.Services["sa"].Dependencies) != 1 { t.Fatal("stale dependency") }
	for _, env := range manifest.Environments {
		if len(env.Resolutions["sa"]) != 1 || env.Resolutions["sa"]["store"].Mode != "remote" { t.Fatal("stale or missing resolution") }
	}
	before, _ := os.ReadFile(path)
	if _, err := UpdateWorkspace(path, workspace, false); err != nil { t.Fatal(err) }
	assertFileContents(t, path, string(before))
	writeDiscoveryFile(t, application, "mdmRpc: {consul: {key: mdm.rpc}}\nstoreLayoutRpc: {}\n")
	if _, err := UpdateWorkspace(path, workspace, false); err != nil { t.Fatal(err) }
	manifest, err = Load(path)
	if err != nil { t.Fatal(err) }
	if manifest.Services["sa"].Dependencies["mdm"].LocalService != "mdm" { t.Fatal("new provider not mapped") }
	before, _ = os.ReadFile(path)
	writeDiscoveryFile(t, application, "mdmRpc: [invalid\n")
	if _, err := UpdateWorkspace(path, workspace, false); err == nil { t.Fatal("accepted invalid YAML") }
	assertFileContents(t, path, string(before))
	if err := os.Remove(application); err != nil { t.Fatal(err) }
	result, err := UpdateWorkspace(path, workspace, false)
	if err != nil || len(result.DependencyNotes) == 0 { t.Fatalf("missing source: %v, %v", result, err) }
	assertFileContents(t, path, string(before))
}

func TestUpdateDependencyRemapsChangedIdentity(t *testing.T) {
	root := t.TempDir()
	writeDiscoveryFile(t, filepath.Join(root, "api", "resources", "application.yaml"), "clientRpc: {discovType: consul, consul: {key: new.rpc}}\n")
	manifest := &model.Manifest{
		Policies: map[string]model.Policy{"files": {Drivers: model.PolicyDrivers{Materializer: "yaml-overlay"}, Config: model.PolicyConfig{SourceDir: "resources", Application: "application.yaml"}}},
		Services: map[string]model.Service{
			"api": {Path: "api", Policy: "files", Dependencies: map[string]model.Dependency{"old": {Binding: "clientRpc", LocalService: "old", Port: "rpc"}}},
			"old": {Path: "old", Ports: map[string]int{"rpc": 18080}, Discovery: model.ServiceDiscovery{Identity: "old.rpc", ProviderAliases: []string{"clientRpc"}}},
			"new": {Path: "new", Ports: map[string]int{"rpc": 18081}, Discovery: model.ServiceDiscovery{Identity: "new.rpc"}},
		},
		Environments: map[string]model.Environment{"test": {Resolutions: map[string]map[string]model.DependencyResolution{"api": {"old": {Mode: "remote"}}}}},
	}
	if _, _, err := synchronizeApplicationDependencies(manifest, root, []DiscoveredService{{Path: "api", Bindings: []string{"clientRpc"}}}); err != nil { t.Fatal(err) }
	if len(manifest.Services["api"].Dependencies) != 1 || manifest.Services["api"].Dependencies["new"].LocalService != "new" { t.Fatal("stale provider retained") }
	if len(manifest.Environments["test"].Resolutions["api"]) != 1 || manifest.Environments["test"].Resolutions["api"]["new"].Mode != "remote" { t.Fatal("stale environment resolution") }
}
