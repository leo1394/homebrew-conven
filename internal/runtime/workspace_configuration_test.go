package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/config"
)

const workspaceConfigurationFixture = `# retain configuration comment
version: 3
workspace:
  name: config-test
environments:
  test:
    connection:
      driver: ktctl
      command: ktctl
      kubeconfig: configs/test
      context: test-context
      namespace: test
      readiness:
        - name: api
          address: 127.0.0.1:80
  prod:
    connection:
      driver: none
services:
  api:
    path: api
    discovery:
      analyzer: go-subdirectory
      consumerBindings: [dbRpc]
    dependencies:
      db:
        binding: dbRpc
        port: rpc
    runner:
      run: [api]
  db:
    path: db
    ports:
      rpc: 18081
    runner:
      run: [db]
`

func configurationFixture(t *testing.T) (string, string) {
	t.Helper()
	for _, name := range []string{"CONVEN_KUBECONFIG", "KTCTL_KUBECONFIG", "KUBECONFIG"} { t.Setenv(name, "") }
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".conven"), 0700); err != nil { t.Fatal(err) }
	path := config.ManifestPath(root)
	if err := os.WriteFile(path, []byte(workspaceConfigurationFixture), 0640); err != nil { t.Fatal(err) }
	return root, path
}

func configurationRequest(snapshot *WorkspaceConfiguration) WorkspaceConfigurationSave {
	request := WorkspaceConfigurationSave{Revision:snapshot.Revision, Environments:[]config.EnvironmentConfiguration{}, Services:[]config.ServiceConfiguration{}, DisabledBindings:append([]string{}, snapshot.DisabledBindings...)}
	connectionByEnvironment := snapshot.ConnectionByEnvironment
	request.ConnectionByEnvironment = &connectionByEnvironment
	for _, env := range snapshot.Environments { request.Environments = append(request.Environments, config.EnvironmentConfiguration{Name:env.Name, Kubeconfig:env.Configured.Kubeconfig, Context:env.Configured.Context, Namespace:env.Configured.Namespace}) }
	for _, service := range snapshot.Services { request.Services = append(request.Services, config.ServiceConfiguration{Name:service.Name, Listen:service.Listen}) }
	return request
}

func TestWorkspaceConfigurationRoundTripAndBindingDeclaration(t *testing.T) {
	root, path := configurationFixture(t)
	snapshot, err := ReadWorkspaceConfiguration(root)
	if err != nil { t.Fatal(err) }
	if snapshot.Session != nil || len(snapshot.Environments) != 2 || len(snapshot.Services) != 2 || len(snapshot.Bindings) != 1 { t.Fatalf("unexpected snapshot: %#v", snapshot) }
	binding := snapshot.Bindings[0]
	if binding.Name != "dbRpc" || binding.Capability != "not-verified" || strings.Join(binding.Consumers, ",") != "api" || strings.Join(binding.Providers, ",") != "db" { t.Fatalf("incorrect binding: %#v", binding) }
	request := configurationRequest(snapshot)
	for i := range request.Environments { if request.Environments[i].Name == "test" { request.Environments[i].Kubeconfig = ""; request.Environments[i].Context = "new-context"; request.Environments[i].Namespace = "new-namespace" } }
	request.DisabledBindings = []string{"dbRpc"}
	saved, err := SaveWorkspaceConfiguration(root, request)
	if err != nil { t.Fatal(err) }
	if saved.Revision == snapshot.Revision || !saved.Bindings[0].Disabled { t.Fatalf("save not reflected: %#v", saved) }
	manifest, err := config.Load(path); if err != nil { t.Fatal(err) }
	c := manifest.Environments["test"].Connection
	if c.Kubeconfig != "" || c.Context != "new-context" || c.Namespace != "new-namespace" || strings.Join(manifest.Workspace.DisabledBindings, ",") != "dbRpc" { t.Fatalf("incorrect persisted fields: %#v", manifest) }
	data, err := os.ReadFile(path); if err != nil { t.Fatal(err) }
	if !strings.Contains(string(data), "# retain configuration comment") || strings.Contains(string(data), "kubeconfig:") { t.Fatalf("comment/removal lost: %s", data) }
	info, err := os.Stat(path); if err != nil || info.Mode().Perm() != 0640 { t.Fatalf("mode lost: %v %v", info, err) }
	if _, err := SaveWorkspaceConfiguration(root, request); err == nil { t.Fatal("accepted stale revision") }
}

func TestWorkspaceConfigurationOverridesAndInputConflicts(t *testing.T) {
	root, _ := configurationFixture(t)
	settings := config.LocalSettingsPath(root)
	if err := os.WriteFile(settings, []byte("ktctl.kubeconfig: /configured\n"), 0600); err != nil { t.Fatal(err) }
	snapshot, err := ReadWorkspaceConfiguration(root); if err != nil { t.Fatal(err) }
	var test EnvironmentConfigurationView
	for _, env := range snapshot.Environments { if env.Name == "test" { test = env } }
	if test.Configured.Kubeconfig != "configs/test" || test.Effective["kubeconfig"] != "/configured" || test.Effective["source"] != "ktctl.kubeconfig" { t.Fatalf("override lost: %#v", test) }
	request := configurationRequest(snapshot)
	t.Setenv("CONVEN_KUBECONFIG", "/environment")
	if _, err := SaveWorkspaceConfiguration(root, request); err == nil { t.Fatal("ignored changed inherited override") }
	snapshot, err = ReadWorkspaceConfiguration(root); if err != nil { t.Fatal(err) }
	for _, env := range snapshot.Environments { if env.Name == "test" && (env.Effective["source"] != "CONVEN_KUBECONFIG" || env.Effective["kubeconfig"] != "/environment") { t.Fatalf("environment priority lost: %#v", env) } }
	request = configurationRequest(snapshot)
	if err := os.WriteFile(settings, []byte("ktctl.kubeconfig: /changed-but-masked\n"), 0600); err != nil { t.Fatal(err) }
	if _, err := SaveWorkspaceConfiguration(root, request); err == nil { t.Fatal("ignored masked settings change") }
}

func TestWorkspaceConfigurationSaveEnvironmentOptIn(t *testing.T) {
	root, path := configurationFixture(t)
	settings := config.LocalSettingsPath(root)
	settingsData := "ktctl.kubeconfig: /shared/config\n"
	if err := os.WriteFile(settings, []byte(settingsData), 0600); err != nil { t.Fatal(err) }
	snapshot, err := ReadWorkspaceConfiguration(root); if err != nil { t.Fatal(err) }
	if snapshot.ConnectionByEnvironment { t.Fatal("opt-in must default false") }
	request := configurationRequest(snapshot)
	enabled := true
	request.ConnectionByEnvironment = &enabled
	saved, err := SaveWorkspaceConfiguration(root, request); if err != nil { t.Fatal(err) }
	if !saved.ConnectionByEnvironment { t.Fatal("save omitted opt-in") }
	for _, env := range saved.Environments {
		if env.Name == "test" && (env.Effective["source"] != "manifest connection.kubeconfig" || env.Effective["kubeconfig"] != filepath.Join(root, "configs", "test")) { t.Fatalf("opt-in not applied to resolver: %#v", env) }
	}
	manifest, err := config.Load(path); if err != nil { t.Fatal(err) }
	if !manifest.Workspace.ConnectionByEnvironment { t.Fatal("opt-in not saved in manifest") }
	if data, err := os.ReadFile(settings); err != nil || string(data) != settingsData { t.Fatalf("save modified settings: %s %v", data, err) }
	request = configurationRequest(saved); disabled := false; request.ConnectionByEnvironment = &disabled
	saved, err = SaveWorkspaceConfiguration(root, request); if err != nil { t.Fatal(err) }
	if saved.ConnectionByEnvironment { t.Fatal("opt-in not cleared") }
	for _, env := range saved.Environments { if env.Name == "test" && env.Effective["source"] != "ktctl.kubeconfig" { t.Fatalf("legacy precedence not restored: %#v", env) } }
	manifest, err = config.Load(path); if err != nil || manifest.Workspace.ConnectionByEnvironment { t.Fatalf("manifest opt-in not cleared: %v", err) }
}

func TestWorkspaceConfigurationInvalidRequestsDoNotWrite(t *testing.T) {
	root, path := configurationFixture(t)
	snapshot, err := ReadWorkspaceConfiguration(root); if err != nil { t.Fatal(err) }
	for _, mutate := range []func(*WorkspaceConfigurationSave){
		func(r *WorkspaceConfigurationSave) { r.ConnectionByEnvironment = nil },
		func(r *WorkspaceConfigurationSave) { r.Services = r.Services[:1] },
		func(r *WorkspaceConfigurationSave) { r.Environments[1].Name = r.Environments[0].Name },
		func(r *WorkspaceConfigurationSave) { r.DisabledBindings = []string{"missing"} },
		func(r *WorkspaceConfigurationSave) { r.Services[0].Listen = "all-interfaces" },
		func(r *WorkspaceConfigurationSave) { r.Environments[0].Kubeconfig = "one:two" },
	} {
		request := configurationRequest(snapshot); mutate(&request)
		if _, err := SaveWorkspaceConfiguration(root, request); err == nil { t.Fatal("accepted invalid request") }
		data, err := os.ReadFile(path); if err != nil || string(data) != workspaceConfigurationFixture { t.Fatalf("invalid request changed manifest: %v", err) }
	}
	if err := os.Remove(path); err != nil { t.Fatal(err) }
	target := filepath.Join(root, "target.yaml")
	if err := os.WriteFile(target, []byte(workspaceConfigurationFixture), 0600); err != nil { t.Fatal(err) }
	if err := os.Symlink(target, path); err != nil { t.Fatal(err) }
	if _, err := ReadWorkspaceConfiguration(root); err == nil { t.Fatal("accepted manifest symlink") }
	if err := os.Remove(path); err != nil { t.Fatal(err) }
	if err := os.WriteFile(path, []byte("version: [broken\n"), 0600); err != nil { t.Fatal(err) }
	if _, err := ReadWorkspaceConfiguration(root); err == nil { t.Fatal("returned fallback for malformed manifest") }
}
