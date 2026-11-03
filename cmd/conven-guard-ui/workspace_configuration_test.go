package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/config"
	convenruntime "github.com/leo1394/homebrew-conven/internal/runtime"
)

func runConfigurationAdapter(t *testing.T, input string, args ...string) (int, []byte) {
	t.Helper()
	in, err := os.CreateTemp(t.TempDir(), "input"); if err != nil { t.Fatal(err) }; defer in.Close()
	if _, err := in.WriteString(input); err != nil { t.Fatal(err) }; if _, err := in.Seek(0, 0); err != nil { t.Fatal(err) }
	out, err := os.CreateTemp(t.TempDir(), "output"); if err != nil { t.Fatal(err) }; defer out.Close()
	oldArgs, oldIn, oldOut := os.Args, os.Stdin, os.Stdout
	defer func() { os.Args, os.Stdin, os.Stdout = oldArgs, oldIn, oldOut }()
	os.Args, os.Stdin, os.Stdout = append([]string{"conven-guard-ui"}, args...), in, out
	status := run()
	data, err := os.ReadFile(out.Name()); if err != nil { t.Fatal(err) }
	return status, data
}

func TestWorkspaceConfigurationAdapterReadSaveAndPlannedConnection(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".conven"), 0700); err != nil { t.Fatal(err) }
	if err := os.WriteFile(config.ManifestPath(root), []byte("version: 3\nworkspace: {name: adapter-test}\nenvironments: {test: {connection: {driver: none}}}\nservices: {}\n"), 0600); err != nil { t.Fatal(err) }
	status, data := runConfigurationAdapter(t, "", "--workspace-config", root)
	if status != 0 { t.Fatalf("read failed: %s", data) }
	var snapshot convenruntime.WorkspaceConfiguration
	if err := json.Unmarshal(data, &snapshot); err != nil { t.Fatal(err) }
	request := convenruntime.WorkspaceConfigurationSave{Revision:snapshot.Revision, Environments:[]config.EnvironmentConfiguration{{Name:"test", Namespace:"updated"}}, Services:[]config.ServiceConfiguration{}, DisabledBindings:[]string{}}
	enabled := true
	request.ConnectionByEnvironment = &enabled
	encoded, err := json.Marshal(request); if err != nil { t.Fatal(err) }
	status, data = runConfigurationAdapter(t, string(encoded), "--save-workspace-config", root)
	if status != 0 { t.Fatalf("save failed: %s", data) }
	if err := json.Unmarshal(data, &snapshot); err != nil || !snapshot.ConnectionByEnvironment || snapshot.Environments[0].Configured.Namespace != "updated" { t.Fatalf("save did not return refreshed snapshot: %s %v", data, err) }
	status, data = runConfigurationAdapter(t, "", "--planned-connection", root, "test")
	var planned map[string]string
	if err := json.Unmarshal(data, &planned); err != nil || status != 0 || planned["phase"] != "planned" || planned["environment"] != "test" { t.Fatalf("planned command failed: %s %v", data, err) }
	status, data = runConfigurationAdapter(t, string(encoded), "--save-workspace-config", root)
	var failure map[string]string
	if err := json.Unmarshal(data, &failure); err != nil || status == 0 || failure["code"] != "conflict" || failure["error"] == "" { t.Fatalf("missing structured conflict: %s %v", data, err) }
}

func TestWorkspaceConfigurationAdapterBoundsAndRejectsJSON(t *testing.T) {
	for _, input := range []string{strings.Repeat("x", 1024*1024+1), `{"unexpected":true}`, `{} {}`, `null`, `{"revision":12}`} {
		status, data := runConfigurationAdapter(t, input, "--save-workspace-config", t.TempDir())
		var failure map[string]string
		if err := json.Unmarshal(data, &failure); err != nil || status == 0 || failure["error"] == "" || failure["code"] == "" { t.Fatalf("unstructured failure: %s %v", data, err) }
	}
	for _, args := range [][]string{{"--workspace-config"}, {"--save-workspace-config", "/workspace", "extra"}, {"--planned-connection", "/workspace"}} {
		status, data := runConfigurationAdapter(t, "", args...)
		var failure map[string]string
		if err := json.Unmarshal(data, &failure); err != nil || status == 0 || failure["code"] != "validation" { t.Fatalf("unstructured argument failure: %s %v", data, err) }
	}
}

func TestWorkspaceConfigurationAdapterRequiresExplicitOptInValue(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".conven"), 0700); err != nil { t.Fatal(err) }
	path := config.ManifestPath(root)
	source := "version: 3\nworkspace: {name: adapter-test, connectionByEnvironment: true}\nenvironments: {}\nservices: {}\n"
	if err := os.WriteFile(path, []byte(source), 0600); err != nil { t.Fatal(err) }
	status, data := runConfigurationAdapter(t, "", "--workspace-config", root)
	if status != 0 { t.Fatalf("read failed: %s", data) }
	var snapshot convenruntime.WorkspaceConfiguration
	if err := json.Unmarshal(data, &snapshot); err != nil || !snapshot.ConnectionByEnvironment { t.Fatalf("missing initial opt-in: %s %v", data, err) }
	request := map[string]any{"revision":snapshot.Revision, "environments":[]any{}, "services":[]any{}, "disabledBindings":[]string{}}
	for _, value := range []string{"missing", "null"} {
		if value == "null" { request["connectionByEnvironment"] = nil }
		encoded, err := json.Marshal(request); if err != nil { t.Fatal(err) }
		status, data = runConfigurationAdapter(t, string(encoded), "--save-workspace-config", root)
		var failure map[string]string
		if err := json.Unmarshal(data, &failure); err != nil || status == 0 || failure["code"] != "validation" { t.Fatalf("%s did not fail validation: %s %v", value, data, err) }
		current, err := os.ReadFile(path); if err != nil || string(current) != source { t.Fatalf("%s changed manifest: %s %v", value, current, err) }
	}
	request["connectionByEnvironment"] = false
	encoded, err := json.Marshal(request); if err != nil { t.Fatal(err) }
	status, data = runConfigurationAdapter(t, string(encoded), "--save-workspace-config", root)
	if err := json.Unmarshal(data, &snapshot); err != nil || status != 0 || snapshot.ConnectionByEnvironment { t.Fatalf("explicit false did not disable opt-in: %s %v", data, err) }
}
