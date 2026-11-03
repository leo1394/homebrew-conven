package runtime

import (
	"path/filepath"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestConnectionPreviewFollowsEnvironmentAndOverride(t *testing.T) {
	for _, key := range []string{"CONVEN_KUBECONFIG", "KTCTL_KUBECONFIG", "KUBECONFIG"} { t.Setenv(key, "") }
	manifest := &model.Manifest{Environments:map[string]model.Environment{}}
	for _, env := range []string{"dev", "test", "prod"} {
		manifest.Environments[env] = model.Environment{Connection:model.Connection{Driver:"ktctl", Command:"ktctl", Kubeconfig:"configs/" + env, Context:env + "-context", Namespace:env, Readiness:[]model.Endpoint{{Name:"api", Address:"127.0.0.1:80"}}}}
	}
	workspace := testWorkspace(t, t.TempDir(), manifest)
	for _, env := range []string{"dev", "test", "prod"} {
		value, err := ConnectionPreview(workspace, env)
		if err != nil { t.Fatal(err) }
		if value["kubeconfig"] != filepath.Join(workspace.Root, "configs", env) || value["context"] != env + "-context" || value["namespace"] != env || value["source"] != "manifest connection.kubeconfig" { t.Fatalf("wrong environment: %v", value) }
	}
	workspace.Settings = map[string]string{"ktctl.kubeconfig":"/host/override"}
	value, err := ConnectionPreview(workspace, "test")
	if err != nil || value["kubeconfig"] != "/host/override" || value["source"] != "ktctl.kubeconfig" { t.Fatalf("lost override: %v %v", value, err) }
	t.Setenv("CONVEN_KUBECONFIG", "/host/env-override")
	value, err = ConnectionPreview(workspace, "test")
	if err != nil || value["source"] != "CONVEN_KUBECONFIG" || value["kubeconfig"] != "/host/env-override" { t.Fatalf("lost environment override: %v %v", value, err) }
	if _, err := ConnectionPreview(workspace, "undeclared"); err == nil { t.Fatal("accepted undeclared environment") }
}

func TestConnectionPreviewWorkspaceEnvironmentOptIn(t *testing.T) {
	for _, key := range []string{"CONVEN_KUBECONFIG", "KTCTL_KUBECONFIG", "KUBECONFIG", "PREVIEW_CUSTOM_KUBECONFIG"} { t.Setenv(key, "") }
	manifest := &model.Manifest{Environments:map[string]model.Environment{}}
	for _, name := range []string{"dev", "test", "prod", "empty"} {
		connection := model.Connection{Driver:"ktctl", Command:"ktctl", Kubeconfig:"configs/" + name, KubeconfigEnv:"PREVIEW_CUSTOM_KUBECONFIG", Context:name + "-context", Namespace:name, Readiness:[]model.Endpoint{{Name:"api", Address:"127.0.0.1:80"}}}
		if name == "empty" { connection.Kubeconfig = "" }
		manifest.Environments[name] = model.Environment{Connection:connection}
	}
	workspace := testWorkspace(t, t.TempDir(), manifest)
	workspace.Settings = map[string]string{"ktctl.kubeconfig":"/shared/config"}
	for _, enabled := range []bool{false, true} {
		manifest.Workspace.ConnectionByEnvironment = enabled
		for _, name := range []string{"dev", "test", "prod", "empty"} {
			value, err := ConnectionPreview(workspace, name); if err != nil { t.Fatal(err) }
			want, source := "/shared/config", "ktctl.kubeconfig"
			if enabled && name != "empty" { want, source = filepath.Join(workspace.Root, "configs", name), "manifest connection.kubeconfig" }
			if value["kubeconfig"] != want || value["source"] != source || value["context"] != name + "-context" || value["namespace"] != name { t.Fatalf("enabled=%v env=%s: %v", enabled, name, value) }
		}
	}
	for _, key := range []string{"CONVEN_KUBECONFIG", "KTCTL_KUBECONFIG", "PREVIEW_CUSTOM_KUBECONFIG"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "/external/config")
			value, err := ConnectionPreview(workspace, "test")
			if err != nil || value["kubeconfig"] != "/external/config" || value["source"] != key { t.Fatalf("external override lost: %v %v", value, err) }
		})
	}
	plan := &Plan{Workspace:workspace, EnvironmentName:"test", Environment:manifest.Environments["test"], RunDir:workspace.Store.CurrentDir}
	connection, err := planConnection(plan, CommonOptions{Kubeconfig:"/explicit/config"})
	if err != nil || connection.Kubeconfig != "/explicit/config" || connection.KubeconfigSource != "command line" { t.Fatalf("explicit override lost: %#v %v", connection, err) }
	t.Setenv("KUBECONFIG", "/fallback/config")
	value, err := ConnectionPreview(workspace, "test")
	if err != nil || value["source"] != "manifest connection.kubeconfig" { t.Fatalf("KUBECONFIG fallback was promoted: %v %v", value, err) }
	workspace.Settings = map[string]string{}
	value, err = ConnectionPreview(workspace, "empty")
	if err != nil || value["source"] != "KUBECONFIG" || value["kubeconfig"] != "/fallback/config" { t.Fatalf("empty env fallback lost: %v %v", value, err) }
}
