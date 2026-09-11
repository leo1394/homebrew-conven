package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSetBindingsDisabledAtomicAndIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conven.yaml")
	writeDiscoveryFile(t, path, "version: 3\nworkspace:\n  name: test\n  disabledBindings:\n    - existingRpc # retain comment\nservices: {}\n")
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	changed, err := SetBindingsDisabled(path, []string{"pigeonRpc", "pigeonRpc"}, true)
	if err != nil || !changed {
		t.Fatalf("disable = %v, %v", changed, err)
	}
	manifest, err := Load(path)
	if err != nil || !reflect.DeepEqual(manifest.Workspace.DisabledBindings, []string{"existingRpc", "pigeonRpc"}) {
		t.Fatalf("manifest = %#v, %v", manifest, err)
	}
	data, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	if !strings.Contains(string(data), "# retain comment") || info.Mode().Perm() != 0640 {
		t.Fatal("comment or permissions lost")
	}
	for _, request := range [][]string{{"pigeonRpc"}, {"validRpc", "invalid.name"}} {
		changed, err = SetBindingsDisabled(path, request, true)
		if changed || (len(request) == 2 && err == nil) || (len(request) == 1 && err != nil) {
			t.Fatalf("request %v = %v, %v", request, changed, err)
		}
		assertFileContents(t, path, string(data))
	}
	changed, err = SetBindingsDisabled(path, []string{"pigeonRpc", "missingRpc"}, false)
	if err != nil || !changed {
		t.Fatalf("enable = %v, %v", changed, err)
	}
	manifest, err = Load(path)
	if err != nil || !reflect.DeepEqual(manifest.Workspace.DisabledBindings, []string{"existingRpc"}) {
		t.Fatalf("remaining bindings = %#v, %v", manifest, err)
	}
	if _, err := SetBindingsDisabled(path, []string{"existingRpc"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := SetBindingsDisabled(path, []string{"newRpc"}, true); err != nil {
		t.Fatal(err)
	}
}
