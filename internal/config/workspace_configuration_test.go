package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceConfigurationAtomicListenerAndConnectionUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conven.yaml")
	source := policyManifestYAML + "environments:\n  test:\n    connection:\n      driver: none\n"
	writeDiscoveryFile(t, path, source)
	_, revision, err := ReadConfigurationManifest(path); if err != nil { t.Fatal(err) }
	environments := []EnvironmentConfiguration{{Name:"test", Kubeconfig:"configs/test", Context:"test", Namespace:"test"}}
	services := []ServiceConfiguration{{Name:"api", Listen:"all-interfaces"}, {Name:"db", Listen:"loopback"}}
	if err := SaveWorkspaceConfiguration(path, revision, true, environments, services, []string{"dbRpc"}, nil); err != nil { t.Fatal(err) }
	manifest, _, err := ReadConfigurationManifest(path); if err != nil { t.Fatal(err) }
	if !manifest.Workspace.ConnectionByEnvironment || manifest.Services["api"].Network.Listen != "all-interfaces" || manifest.Environments["test"].Connection.Kubeconfig != "configs/test" || len(manifest.Workspace.DisabledBindings) != 1 { t.Fatalf("lost edit: %#v", manifest) }
	if err := SaveWorkspaceConfiguration(path, revision, false, environments, services, []string{}, nil); !errors.Is(err, ErrWorkspaceConfigurationConflict) { t.Fatalf("stale manifest not a conflict: %v", err) }
}

func TestWorkspaceConfigurationConcurrentEditIsNotOverwritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conven.yaml")
	writeDiscoveryFile(t, path, policyManifestYAML)
	_, revision, err := ReadConfigurationManifest(path); if err != nil { t.Fatal(err) }
	want := "# concurrent edit\n" + policyManifestYAML
	err = SaveWorkspaceConfiguration(path, revision, false, []EnvironmentConfiguration{}, []ServiceConfiguration{{Name:"api", Listen:"all-interfaces"}, {Name:"db", Listen:"loopback"}}, []string{}, func() error { return os.WriteFile(path, []byte(want), 0600) })
	if !errors.Is(err, ErrWorkspaceConfigurationConflict) { t.Fatalf("concurrent edit not a conflict: %v", err) }
	assertFileContents(t, path, want)
}

func TestWorkspaceConfigurationPreservesUntouchedUnsupportedAdapter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conven.yaml")
	source := "version: 2\nworkspace: {name: test}\nservices:\n  api:\n    path: api\n    kind: rpc\n    network: {listen: all-interfaces}\n    runner: {run: [api]}\n"
	writeDiscoveryFile(t, path, source)
	manifest, revision, err := ReadConfigurationManifest(path); if err != nil { t.Fatal(err) }
	if ListenerConfigurationReason(manifest, manifest.Services["api"]) == "" { t.Fatal("unsupported adapter shown editable") }
	if err := SaveWorkspaceConfiguration(path, revision, false, []EnvironmentConfiguration{}, []ServiceConfiguration{{Name:"api", Listen:"all-interfaces"}}, []string{}, nil); err != nil { t.Fatal(err) }
}
