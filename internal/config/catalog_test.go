package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadCatalogAcceptsRepositoryAndRPCBindingIdentities(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.yaml")
	data := `version: 1
services:
  - repository: catalog-api
    kind: http
    port: 18080
  - rpcBinding: catalogRpc
    kind: rpc
    port: 18081
  - repository: inventory-rpc
    rpcBinding: inventoryRpc
    kind: rpc
    port: 18082
disabledRpcBindings:
  - legacyRpc
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := LoadCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Services) != 3 {
		t.Fatalf("services = %#v", catalog.Services)
	}
	if catalog.Services[0].Repository != "catalog-api" || catalog.Services[1].RPCBinding != "catalogRpc" || catalog.Services[2].Repository != "inventory-rpc" {
		t.Fatalf("catalog identities = %#v", catalog.Services)
	}
	if strings.Join(catalog.DisabledRPCBindings, ",") != "legacyRpc" {
		t.Fatalf("disabled RPC bindings = %#v", catalog.DisabledRPCBindings)
	}
}

func TestLoadCatalogRejectsInvalidCatalogs(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		message string
	}{
		{name: "unknown version", data: "version: 2\nservices: []\n", message: "version must be 1"},
		{name: "missing identity", data: "version: 1\nservices:\n  - kind: http\n    port: 18080\n", message: "must declare repository, rpcBinding, or both"},
		{name: "binding requires rpc", data: "version: 1\nservices:\n  - rpcBinding: catalogRpc\n    kind: http\n    port: 18080\n", message: "kind must be rpc when rpcBinding is declared"},
		{name: "duplicate repository", data: "version: 1\nservices:\n  - repository: api\n    kind: http\n    port: 18080\n  - repository: api\n    kind: http\n    port: 18081\n", message: "duplicates services[0].repository"},
		{name: "duplicate binding", data: "version: 1\nservices:\n  - rpcBinding: apiRpc\n    kind: rpc\n    port: 18080\n  - rpcBinding: apiRpc\n    kind: rpc\n    port: 18081\n", message: "duplicates services[0].rpcBinding"},
		{name: "duplicate port", data: "version: 1\nservices:\n  - repository: api\n    kind: http\n    port: 18080\n  - repository: worker\n    kind: http\n    port: 18080\n", message: "duplicates services[0].port"},
		{name: "duplicate disabled binding", data: "version: 1\nservices: []\ndisabledRpcBindings: [apiRpc, apiRpc]\n", message: "duplicates disabledRpcBindings[0]"},
		{name: "unknown field", data: "version: 1\nservices: []\nunknown: true\n", message: "field unknown not found"},
		{name: "multiple documents", data: "version: 1\nservices: []\n---\nversion: 1\nservices: []\n", message: "multiple YAML documents"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "catalog.yaml")
			if err := os.WriteFile(path, []byte(test.data), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadCatalog(path)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("LoadCatalog error = %v, want %q", err, test.message)
			}
		})
	}
}

func TestLoadCatalogRejectsSymbolicLink(t *testing.T) {
	directory := t.TempDir()
	realPath := filepath.Join(directory, "real.yaml")
	path := filepath.Join(directory, "catalog.yaml")
	if err := os.WriteFile(realPath, []byte("version: 1\nservices: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realPath, path); err != nil {
		t.Fatal(err)
	}
	_, err := LoadCatalog(path)
	if err == nil || !strings.Contains(err.Error(), "not a symbolic link") {
		t.Fatalf("LoadCatalog error = %v", err)
	}
}

func TestEditWorkspaceCatalogPublishesOnlyValidChanges(t *testing.T) {
	workspace := t.TempDir()
	boundary := filepath.Join(workspace, ".conven")
	if err := os.Mkdir(boundary, 0700); err != nil {
		t.Fatal(err)
	}
	path := CatalogPath(workspace)
	source := "version: 1\nservices: []\ndisabledRpcBindings: []\n"
	if err := os.WriteFile(path, []byte(source), 0640); err != nil {
		t.Fatal(err)
	}
	result, err := EditWorkspaceCatalog(workspace, func(draft string) error {
		return os.WriteFile(draft, []byte("version: 1\nservices:\n  - rpcBinding: catalogRpc\n    kind: rpc\n    port: 18081\ndisabledRpcBindings: []\n"), 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Path != path || result.DraftPath != "" {
		t.Fatalf("edit result = %#v", result)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "rpcBinding: catalogRpc") {
		t.Fatalf("published catalog = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0640 {
		t.Fatalf("published catalog mode = %04o, want 0640", info.Mode().Perm())
	}

	published := string(data)
	result, err = EditWorkspaceCatalog(workspace, func(draft string) error {
		return os.WriteFile(draft, []byte("version: 1\nservices:\n  - kind: rpc\n    port: 18082\n"), 0600)
	})
	if err == nil || !strings.Contains(err.Error(), "edited catalog is invalid") {
		t.Fatalf("invalid edit error = %v", err)
	}
	if result.DraftPath == "" {
		t.Fatalf("invalid edit did not preserve its draft: %#v", result)
	}
	if _, statErr := os.Stat(result.DraftPath); statErr != nil {
		t.Fatalf("preserved draft: %v", statErr)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != published {
		t.Fatalf("invalid edit changed source: %q", data)
	}
}

func TestEditWorkspaceCatalogPreservesChangedDraftOnEditorFailure(t *testing.T) {
	workspace := t.TempDir()
	boundary := filepath.Join(workspace, ".conven")
	if err := os.Mkdir(boundary, 0700); err != nil {
		t.Fatal(err)
	}
	path := CatalogPath(workspace)
	source := "version: 1\nservices: []\n"
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := EditWorkspaceCatalog(workspace, func(draft string) error {
		if writeErr := os.WriteFile(draft, []byte("version: 1\nservices: []\ndisabledRpcBindings: [apiRpc]\n"), 0600); writeErr != nil {
			return writeErr
		}
		return errors.New("editor exited")
	})
	if err == nil || !strings.Contains(err.Error(), "editor exited") || result.DraftPath == "" {
		t.Fatalf("editor failure = (%#v, %v)", result, err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != source {
		t.Fatalf("editor failure changed source: %q", data)
	}
}
