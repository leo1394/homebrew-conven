package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestSetServiceListenerScopeUpdatesManifestAtomically(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	source := "# keep listener comment\n" + policyManifestYAML
	writeDiscoveryFile(t, manifestPath, source)
	if err := os.Chmod(manifestPath, 0640); err != nil {
		t.Fatal(err)
	}

	result, err := SetServiceListenerScope(manifestPath, []string{"api"}, model.NetworkListenAllInterfaces)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Changed, []string{"api"}) || len(result.Unchanged) != 0 {
		t.Fatalf("listener update result = %#v", result)
	}
	manifest, err := Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Services["api"].Network.Listen != model.NetworkListenAllInterfaces {
		t.Fatalf("listener scope = %q", manifest.Services["api"].Network.Listen)
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "# keep listener comment") || !strings.Contains(string(data), "listen: all-interfaces") {
		t.Fatalf("updated manifest =\n%s", data)
	}
	info, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0640 {
		t.Fatalf("manifest mode = %o", info.Mode().Perm())
	}

	beforeNoOp := append([]byte(nil), data...)
	result, err = SetServiceListenerScope(manifestPath, []string{"api"}, model.NetworkListenAllInterfaces)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Changed) != 0 || !reflect.DeepEqual(result.Unchanged, []string{"api"}) {
		t.Fatalf("no-op listener result = %#v", result)
	}
	afterNoOp, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterNoOp, beforeNoOp) {
		t.Fatal("no-op listener update rewrote the manifest")
	}

	result, err = SetServiceListenerScope(manifestPath, []string{"api"}, model.NetworkListenLoopback)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Changed, []string{"api"}) {
		t.Fatalf("loopback result = %#v", result)
	}
	manifest, err = Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Services["api"].Network.Listen != "" || manifest.Services["api"].Network.EffectiveListen() != model.NetworkListenLoopback {
		t.Fatalf("loopback network = %#v", manifest.Services["api"].Network)
	}
	data, err = os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "network:") {
		t.Fatalf("loopback default retained redundant network config:\n%s", data)
	}
}

func TestSetServiceListenerScopeRejectsInvalidRequestsWithoutChangingManifest(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, policyManifestYAML)

	for _, test := range []struct {
		name     string
		services []string
		mode     string
		message  string
	}{
		{name: "missing services", mode: model.NetworkListenAllInterfaces, message: "at least one service"},
		{name: "unknown service", services: []string{"missing"}, mode: model.NetworkListenAllInterfaces, message: "unknown services"},
		{name: "duplicate service", services: []string{"api", "api"}, mode: model.NetworkListenAllInterfaces, message: "duplicate services"},
		{name: "unsupported mode", services: []string{"api"}, mode: "public", message: "must be loopback or all-interfaces"},
	} {
		t.Run(test.name, func(t *testing.T) {
			before, err := os.ReadFile(manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			_, err = SetServiceListenerScope(manifestPath, test.services, test.mode)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("listener update error = %v", err)
			}
			after, readErr := os.ReadFile(manifestPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatal("failed listener update changed the manifest")
			}
		})
	}
}
