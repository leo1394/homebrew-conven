package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateWorkspaceManifestV1AndV2ToV3(t *testing.T) {
	for _, version := range []int{1, 2} {
			t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			workspace := t.TempDir()
			boundary := filepath.Join(workspace, ".conven")
			if err := os.Mkdir(boundary, 0700); err != nil { t.Fatal(err) }
			path := filepath.Join(boundary, "conven.yaml")
			v2Runtime := ""
			if version == 2 {
				v2Runtime = `    kind: http
    ports:
      http: 18080
    health:
      type: tcp
      address: 127.0.0.1:18080
`
			}
			source := fmt.Sprintf(`version: %d
workspace:
  name: migrate
  disabledBindings: []
environments:
  local:
    connection:
      driver: none
services:
  demo:
    path: demo
    runner:
      run: [sh, -c, "exit 0"]
%s`, version, v2Runtime)
			if err := os.WriteFile(path, []byte(source), 0600); err != nil { t.Fatal(err) }
			result, err := MigrateWorkspaceManifest(workspace)
			if err != nil { t.Fatal(err) }
			if !result.Changed || result.From != version || result.To != 3 || result.BackupPath == "" {
				t.Fatalf("migration result = %#v", result)
			}
			manifest, err := Load(path)
			if err != nil { t.Fatal(err) }
			if manifest.Version != 3 { t.Fatalf("version = %d", manifest.Version) }
			migrated := manifest.Services["demo"]
			if len(migrated.Kinds) != 0 {
				t.Fatalf("generic runner migration = %#v", migrated)
			}
			if version == 2 && (migrated.Policy != "generic-runner" || migrated.Discovery.Certifier != "manual") {
				t.Fatalf("v2 generic runner migration = %#v", migrated)
			}
			if version == 2 && (len(migrated.HealthChecks) != 1 || migrated.HealthChecks[0].Type != "tcp" || migrated.HealthChecks[0].Server != "") {
				t.Fatalf("v2 runner health semantics were not preserved: %#v", migrated.HealthChecks)
			}
			before, err := os.ReadFile(path)
			if err != nil { t.Fatal(err) }
			again, err := MigrateWorkspaceManifest(workspace)
			if err != nil { t.Fatal(err) }
			after, err := os.ReadFile(path)
			if err != nil { t.Fatal(err) }
			if again.Changed || string(before) != string(after) {
				t.Fatalf("repeated migration changed the manifest: %#v", again)
			}
		})
	}
}

func TestMigrateWorkspaceManifestRejectsActiveSessionWithoutChangingFile(t *testing.T) {
	workspace := t.TempDir()
	boundary := filepath.Join(workspace, ".conven")
	runtimeDirectory := filepath.Join(boundary, "runtime")
	if err := os.MkdirAll(runtimeDirectory, 0700); err != nil { t.Fatal(err) }
	path := filepath.Join(boundary, "conven.yaml")
	source := `version: 2
workspace:
  name: migrate
  disabledBindings: []
environments: {}
services:
  demo:
    path: demo
    runner:
      run: [sh, -c, "exit 0"]
`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil { t.Fatal(err) }
	session := fmt.Sprintf(`{"version":3,"services":[{"name":"demo","pid":%d}]}`, os.Getpid())
	if err := os.WriteFile(filepath.Join(runtimeDirectory, "session.json"), []byte(session), 0600); err != nil { t.Fatal(err) }
	_, err := MigrateWorkspaceManifest(workspace)
	if err == nil || !strings.Contains(err.Error(), "services --stop-all") {
		t.Fatalf("active migration error = %v", err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil { t.Fatal(readErr) }
	if string(after) != source { t.Fatal("failed migration changed the manifest") }
}
