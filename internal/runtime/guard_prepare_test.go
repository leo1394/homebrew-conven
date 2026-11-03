package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/model"
)

func guardPreparationFixture(t *testing.T) (*WorkspaceData, string, string) {
	t.Helper()
	root, responses, output := t.TempDir(), t.TempDir(), t.TempDir()
	manifest := &model.Manifest{
		Workspace: model.Workspace{Policy: "apollo"},
		Environments: map[string]model.Environment{"test": {}},
		Policies: map[string]model.Policy{"apollo": {
			Drivers: model.PolicyDrivers{ConfigSource: "apollo", Materializer: "yaml-overlay"},
			Config: model.PolicyConfig{SourceDir: ".", Application: "application.yaml", Bootstrap: "bootstrap.yaml"},
		}},
		Services: map[string]model.Service{},
	}
	for _, name := range []string{"alpha", "beta"} {
		manifest.Services[name] = model.Service{Path: name}
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil { t.Fatal(err) }
		if err := os.WriteFile(filepath.Join(root, name, "bootstrap.yaml"), []byte("appId: " + name + "\ncluster: default\nnamespaceName: application.yml\nip: 127.0.0.1\n"), 0600); err != nil { t.Fatal(err) }
		if err := os.WriteFile(filepath.Join(root, name, "application.yaml"), []byte("original: true\n"), 0600); err != nil { t.Fatal(err) }
		data, err := json.Marshal(map[string]any{"appId": name, "cluster": "default", "namespaceName": "application.yml", "configurations": map[string]string{"content": "remote: true\n"}})
		if err != nil { t.Fatal(err) }
		if err := os.WriteFile(filepath.Join(responses, name + ".json"), data, 0600); err != nil { t.Fatal(err) }
	}
	workspace := testWorkspace(t, root, manifest)
	if err := os.WriteFile(workspace.ConfigPath, []byte("fixture manifest\n"), 0600); err != nil { t.Fatal(err) }
	return workspace, responses, output
}

func TestPrepareGuardConfigurationsPublishesPrivateSnapshotWithoutSourceWrites(t *testing.T) {
	workspace, responses, output := guardPreparationFixture(t)
	alpha := workspace.Manifest.Services["alpha"]; alpha.Ports = map[string]int{"http": 18083}; alpha.Network.Listen = model.NetworkListenAllInterfaces; workspace.Manifest.Services["alpha"] = alpha
	beta := workspace.Manifest.Services["beta"]; beta.Ports = map[string]int{"rpc": 18081}; workspace.Manifest.Services["beta"] = beta
	dir, err := PrepareGuardConfigurations(context.Background(), workspace, "test", []string{"alpha", "beta"}, responses, output)
	if err != nil { t.Fatal(err) }
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0700 { t.Fatalf("private output: %v %v", info, err) }
	for _, name := range []string{"alpha", "beta"} {
		data, err := os.ReadFile(filepath.Join(dir, "configs", name, "application.yaml"))
		if err != nil || !strings.Contains(string(data), "remote: true") { t.Fatalf("materialized %s: %v", name, err) }
		data, err = os.ReadFile(filepath.Join(workspace.Root, name, "application.yaml"))
		if err != nil || string(data) != "original: true\n" { t.Fatalf("source modified: %s", name) }
	}
	data, err := os.ReadFile(filepath.Join(dir, "sources.json"))
	if err != nil { t.Fatal(err) }
	var prepared guardPreparedSources
	if err := json.Unmarshal(data, &prepared); err != nil { t.Fatal(err) }
	if len(prepared.Ingress) != 2 || prepared.Ingress[0].Service != "beta" || prepared.Ingress[0].Address != "127.0.0.1" || prepared.Ingress[1].Service != "alpha" || prepared.Ingress[1].Address != "0.0.0.0" || prepared.Ingress[1].Port != 18083 { t.Fatalf("service exposure not preserved: %+v", prepared.Ingress) }
	for _, name := range []string{"alpha", "beta"} {
		effective, err := os.ReadFile(filepath.Join(dir, "configs", name, "application.yaml"))
		if err != nil || string(prepared.Services[name].Application) != string(effective) { t.Fatalf("Guard adapter did not receive effective %s config: %v", name, err) }
	}
}

func TestPrepareGuardConfigurationsApolloDefaultFallback(t *testing.T) {
	for _, cluster := range []string{"default", "prod", ""} {
		t.Run(cluster, func(t *testing.T) {
			workspace, responses, output := guardPreparationFixture(t)
			bootstrap := filepath.Join(workspace.Root, "alpha", "bootstrap.yaml")
			if err := os.WriteFile(bootstrap, []byte("appId: alpha\ncluster: test\nnamespaceName: application.yml\nip: apollo.test:8080\n"), 0600); err != nil { t.Fatal(err) }
			data, _ := json.Marshal(map[string]any{"appId":"alpha", "cluster":cluster, "namespaceName":"application.yml", "configurations":map[string]string{"content":"remote: true\n"}})
			if err := os.WriteFile(filepath.Join(responses, "alpha.json"), data, 0600); err != nil { t.Fatal(err) }
			_, err := PrepareGuardConfigurations(context.Background(), workspace, "test", []string{"alpha"}, responses, output)
			if (err == nil) != (cluster == "default") { t.Fatalf("cluster %q: %v", cluster, err) }
			original, _ := os.ReadFile(bootstrap); if !strings.Contains(string(original), "cluster: test") { t.Fatal("source bootstrap changed") }
		})
	}
}

func TestPrepareGuardConfigurationsRollsBackOnIdentityMismatch(t *testing.T) {
	workspace, responses, output := guardPreparationFixture(t)
	if err := os.WriteFile(filepath.Join(responses, "beta.json"), []byte(`{"appId":"wrong","configurations":{"content":"password: never-log-this"}}`), 0600); err != nil { t.Fatal(err) }
	dir, err := PrepareGuardConfigurations(context.Background(), workspace, "test", []string{"alpha", "beta"}, responses, output)
	if err == nil || dir != "" || strings.Contains(err.Error(), "never-log-this") { t.Fatalf("unexpected result: %q %v", dir, err) }
	entries, err := os.ReadDir(output)
	if err != nil || len(entries) != 0 { t.Fatalf("partial transaction published: %v %v", entries, err) }
}

func TestPrepareGuardConfigurationsRejectsCancellationAndUnknownEnvironment(t *testing.T) {
	workspace, responses, output := guardPreparationFixture(t)
	ctx, cancel := context.WithCancel(context.Background()); cancel()
	if _, err := PrepareGuardConfigurations(ctx, workspace, "test", []string{"alpha"}, responses, output); err == nil { t.Fatal("ignored cancellation") }
	if _, err := PrepareGuardConfigurations(context.Background(), workspace, "missing", []string{"alpha"}, responses, output); err == nil { t.Fatal("accepted undeclared environment") }
	entries, err := os.ReadDir(output)
	if err != nil || len(entries) != 0 { t.Fatal("published invalid transaction") }
}

func TestReadGuardResponseRejectsOversizeAndDirectory(t *testing.T) {
	dir := t.TempDir()
	if _, err := readGuardResponse(dir); err == nil { t.Fatal("accepted directory") }
	path := filepath.Join(dir, "large.json")
	file, err := os.Create(path); if err != nil { t.Fatal(err) }
	err = file.Truncate((16 << 20) + 1); file.Close(); if err != nil { t.Fatal(err) }
	if _, err := readGuardResponse(path); err == nil { t.Fatal("accepted oversized response") }
}
