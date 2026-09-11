package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

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
