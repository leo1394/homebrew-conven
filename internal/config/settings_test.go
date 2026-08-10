package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSettingsLocalOverridesGlobalAndUnsetRevealsGlobal(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	mustMkdirAll(t, filepath.Join(workspace, ".loom"))

	if err := SetSetting("", home, true, "ktctl.path", "/global/ktctl"); err != nil {
		t.Fatal(err)
	}
	if err := SetSetting("", home, true, "ktctl.kubeconfig", "/global/kubeconfig"); err != nil {
		t.Fatal(err)
	}
	if err := SetSetting(workspace, home, false, "ktctl.path", "/local/ktctl"); err != nil {
		t.Fatal(err)
	}
	if err := SetSetting(workspace, home, false, "ktctl.kubeconfig", "/local/kubeconfig"); err != nil {
		t.Fatal(err)
	}
	if err := SetSetting(workspace, home, false, "editor.command", "code"); err != nil {
		t.Fatal(err)
	}
	values, err := EffectiveSettings(workspace, home)
	if err != nil {
		t.Fatal(err)
	}
	if values["ktctl.path"] != "/local/ktctl" {
		t.Fatalf("effective ktctl.path = %q", values["ktctl.path"])
	}
	if values["ktctl.kubeconfig"] != "/local/kubeconfig" {
		t.Fatalf("effective ktctl.kubeconfig = %q", values["ktctl.kubeconfig"])
	}
	if actual := SortedSettingKeys(values); !reflect.DeepEqual(actual, []string{"editor.command", "ktctl.kubeconfig", "ktctl.path"}) {
		t.Fatalf("sorted keys = %#v", actual)
	}
	if err := UnsetSetting(workspace, home, false, "ktctl.path"); err != nil {
		t.Fatal(err)
	}
	values, err = EffectiveSettings(workspace, home)
	if err != nil {
		t.Fatal(err)
	}
	if values["ktctl.path"] != "/global/ktctl" {
		t.Fatalf("global ktctl.path was not revealed: %#v", values)
	}
	if err := UnsetSetting(workspace, home, false, "ktctl.kubeconfig"); err != nil {
		t.Fatal(err)
	}
	values, err = EffectiveSettings(workspace, home)
	if err != nil {
		t.Fatal(err)
	}
	if values["ktctl.kubeconfig"] != "/global/kubeconfig" {
		t.Fatalf("global ktctl.kubeconfig was not revealed: %#v", values)
	}
}

func TestSettingsGlobalScopeDoesNotIncludeLocal(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	mustMkdirAll(t, filepath.Join(workspace, ".loom"))
	if err := SetSetting("", home, true, "ktctl.path", "global-ktctl"); err != nil {
		t.Fatal(err)
	}
	if err := SetSetting(workspace, home, false, "ktctl.path", "local-ktctl"); err != nil {
		t.Fatal(err)
	}
	values, err := ScopeSettings(workspace, home, true)
	if err != nil {
		t.Fatal(err)
	}
	if values["ktctl.path"] != "global-ktctl" {
		t.Fatalf("global settings = %#v", values)
	}
}

func TestSettingsRejectsRelativeKtctlPathWithSeparator(t *testing.T) {
	workspace := t.TempDir()
	mustMkdirAll(t, filepath.Join(workspace, ".loom"))
	err := SetSetting(workspace, t.TempDir(), false, "ktctl.path", "../bin/ktctl")
	if err == nil || !strings.Contains(err.Error(), "absolute path") {
		t.Fatalf("error = %v, want path validation error", err)
	}
}

func TestSettingsRejectsMultipleKtctlKubeconfigFiles(t *testing.T) {
	workspace := t.TempDir()
	mustMkdirAll(t, filepath.Join(workspace, ".loom"))
	value := strings.Join([]string{"/config/one", "/config/two"}, string(os.PathListSeparator))
	err := SetSetting(workspace, t.TempDir(), false, "ktctl.kubeconfig", value)
	if err == nil || !strings.Contains(err.Error(), "multiple kubeconfig files") {
		t.Fatalf("error = %v, want kubeconfig path validation error", err)
	}
}

func TestSettingsFilesArePrivate(t *testing.T) {
	home := t.TempDir()
	if err := SetSetting("", home, true, "ktctl.path", "ktctl-custom"); err != nil {
		t.Fatal(err)
	}
	path, err := GlobalSettingsPath(home)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("config permissions = %o, want 600", info.Mode().Perm())
	}
}
