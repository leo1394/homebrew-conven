package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInitLocalWorkspaceCreatesMinimalNoClusterFiles(t *testing.T) {
	workspace := t.TempDir()
	application := []byte("version: 2\nworkspace:\n  name: demo\nenvironments: {}\nservices: {}\n")
	result, err := InitLocalWorkspaceDetailsWithPolicySpecification(workspace, application, []byte("spec"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created {
		t.Fatal("workspace was not created")
	}
	manifest, err := Load(filepath.Join(workspace, ".conven", "conven.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != 2 || manifest.Environments["local"].Connection.Driver != "none" || manifest.Environments["local"].EnvFile != "" {
		t.Fatalf("local environment = %#v", manifest.Environments["local"])
	}
	for _, name := range []string{"compose.yaml", "local.env", "local.env.example"} {
		if _, err := os.Stat(filepath.Join(workspace, ".conven", name)); !os.IsNotExist(err) {
			t.Fatalf("%s should be generated only on demand: %v", name, err)
		}
	}
}
