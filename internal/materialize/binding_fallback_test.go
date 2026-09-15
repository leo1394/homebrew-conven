package materialize

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRepositoryBindingFallbackPrecedence(t *testing.T) {
	repository := "db: {password: do-not-copy}\nrpc: {target: 'localhost:9000'}\notherRpc: {target: 'localhost:9001'}\n"
	for _, test := range []struct { name, apollo, source, want string; fail bool }{
		{"missing", "name: api\n", repository, "localhost:9000", false},
		{"existing", "rpc: {target: 'localhost:8000'}\n", repository, "localhost:8000", false},
		{"null", "rpc: null\n", repository, "null", false},
		{"empty", "rpc: {}\n", repository, "{}", false},
		{"invalid Apollo", "rpc: {discovType: consul, consul: {key: test}}\n", repository, "", true},
		{"invalid source", "name: api\n", "rpc: {discovType: consul, consul: {key: test}}\n", "", true},
		{"unused invalid source", "rpc: {target: 'localhost:8000'}\n", "rpc: {consul: {key: test}}\n", "localhost:8000", false},
		{"source null", "name: api\n", "rpc: null\n", "name: api", false},
		{"missing both", "name: api\n", "other: true\n", "name: api", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, _, err := mergeBindingFallbacks([]byte(test.apollo), []byte(test.source), []BindingFallback{{Binding: "rpc"}})
			if (err != nil) != test.fail { t.Fatalf("error = %v", err) }
			if test.fail { return }
			if !strings.Contains(string(data), test.want) { t.Fatalf("result = %s", data) }
			if strings.Contains(string(data), "do-not-copy") || strings.Contains(string(data), "otherRpc") { t.Fatal("copied unrelated configuration") }
		})
	}
}

func TestMaterializeBindingFallbackReadsFreshSourceAndPublishesAtomically(t *testing.T) {
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() { w.WriteHeader(http.StatusNotFound); return }
		json.NewEncoder(w).Encode(map[string]any{"configurations": map[string]string{"content": "name: remote\n"}})
	}))
	defer server.Close()
	root := t.TempDir()
	source, configs := filepath.Join(root, "source"), filepath.Join(root, "configs")
	writeTestSource(t, source, server.URL)
	if err := os.Mkdir(configs, 0700); err != nil { t.Fatal(err) }
	plan := testPlan(source, configs, filepath.Join(configs, "api"))
	plan.SourceDriver = SourceApollo
	plan.Patches = nil
	plan.BindingFallbacks = []BindingFallback{{Binding: "rpc"}}
	for _, port := range []string{"9000", "9001"} {
		if err := os.WriteFile(filepath.Join(source, "application.yaml"), []byte("rpc: {target: 'localhost:"+port+"'}\nsecret: do-not-copy\n"), 0600); err != nil { t.Fatal(err) }
		origins, err := MaterializeWithReport(context.Background(), plan)
		if err != nil { t.Fatal(err) }
		data, _ := os.ReadFile(filepath.Join(plan.TargetDir, "application.yaml"))
		if len(origins) != 1 || !strings.Contains(string(data), port) || strings.Contains(string(data), "do-not-copy") { t.Fatalf("wrong fallback result: %s; %v", data, origins) }
	}
	plan.Patches = []Patch{{File: "application.yaml", Path: "rpc.target", Value: "localhost:9002"}}
	origins, err := MaterializeWithReport(context.Background(), plan)
	if err != nil || len(origins) != 1 || origins[0].Source != "explicit patch" { t.Fatalf("patch report: %v, %v", origins, err) }
	before, _ := os.ReadFile(filepath.Join(plan.TargetDir, "application.yaml"))
	if !strings.Contains(string(before), "9002") { t.Fatal("patch did not override fallback") }
	fail.Store(true)
	if err := Materialize(context.Background(), plan); err == nil { t.Fatal("Apollo fetch failure fell back to repository") }
	after, _ := os.ReadFile(filepath.Join(plan.TargetDir, "application.yaml"))
	if string(before) != string(after) { t.Fatal("failed materialization replaced current config") }
}

func TestRepositoryBindingFallbackRejectsUnsafeDocuments(t *testing.T) {
	for _, source := range []string{"rpc: {}\nrpc: {}", "rpc: &a {child: *a}", "rpc: {<<: {target: x}}", "? [a,b]\n: x", "rpc: {}\n---\nrpc: {}"} {
		if _, err := bindingDocument([]byte(source)); err == nil { t.Fatalf("accepted unsafe document %q", source) }
	}
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(target, []byte("rpc: {}"), 0600); err != nil { t.Fatal(err) }
	if err := os.Symlink(target, filepath.Join(root, "application.yaml")); err != nil { t.Fatal(err) }
	if err := ValidateBindingFallbackSource(root, "application.yaml"); err == nil { t.Fatal("accepted symlink") }
}

func TestRepositoryBindingFallbackDereferencesAliases(t *testing.T) {
	data, _, err := mergeBindingFallbacks([]byte("name: api\n"), []byte("template: &rpc {target: 'localhost:9000'}\nrpc: *rpc\n"), []BindingFallback{{Binding: "rpc"}})
	if err != nil { t.Fatal(err) }
	var result map[string]interface{}
	if err := yaml.Unmarshal(data, &result); err != nil { t.Fatal(err) }
	if len(result) != 2 || strings.Contains(string(data), "*rpc") { t.Fatalf("invalid clone: %s", data) }
}
