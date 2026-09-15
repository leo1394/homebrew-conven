package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestRPCTargetInitialization(t *testing.T) {
	for _, test := range []struct { name, helper, body string; blocked, repairable bool }{
		{name: "unconditional", body: `func use(c config.Config) { client(c.ChatclbtRpc) }`},
		{name: "discovery only", body: `func use(c config.Config) { if c.ChatclbtRpc.DiscovType != "" { client(c.ChatclbtRpc) } }`, blocked: true, repairable: true},
		{name: "target supported", body: `func use(c config.Config) { if c.ChatclbtRpc.DiscovType != "" || c.ChatclbtRpc.Target != "" { client(c.ChatclbtRpc) } }`},
		{name: "alias", body: `func use(c config.Config) { rpc := c.ChatclbtRpc; if rpc.DiscovType != "" { client(rpc) } }`, blocked: true, repairable: true},
		{name: "wrong binding", body: `func use(c config.Config) { if c.OtherRpc.DiscovType != "" { client(c.OtherRpc) }; client(c.ChatclbtRpc) }`},
		{name: "else", body: `func use(c config.Config) { if c.ChatclbtRpc.Target == "" {} else { client(c.ChatclbtRpc) } }`},
		{name: "complex", body: `func use(c config.Config) { if c.ChatclbtRpc.DiscovType != "" && enabled() { client(c.ChatclbtRpc) } }`, blocked: true},
		{name: "helper", helper: `func hasRPC(c zrpc.RpcClientConf) bool { return c.DiscovType != "" || c.Target != "" }`, body: `func use(c config.Config) { if hasRPC(c.ChatclbtRpc) { client(c.ChatclbtRpc) } }`},
		{name: "unsupported helper", helper: `func hasRPC(c zrpc.RpcClientConf) bool { return c.DiscovType != "" }`, body: `func use(c config.Config) { if hasRPC(c.ChatclbtRpc) { client(c.ChatclbtRpc) } }`, blocked: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := writeGoRPCFixture(t, test.helper, test.body)
			err := InspectGoRPCTargetInitialization(root, "go", "chatclbtRpc")
			if (err != nil) != test.blocked { t.Fatalf("error = %v", err) }
			if !test.blocked { return }
			var guard *rpcTargetGuardError
			if !errors.As(err, &guard) || guard.repairable != test.repairable || !strings.Contains(err.Error(), "go/svc/service_context.go:") { t.Fatalf("diagnostic = %v", err) }
			if test.repairable {
				if err := repairRPCTargetGuard(guard); err != nil { t.Fatal(err) }
				if err := InspectGoRPCTargetInitialization(root, "go", "chatclbtRpc"); err != nil { t.Fatal(err) }
			}
		})
	}
}

func TestWorkspaceTargetRepairPreservesFormattingAndIsIdempotent(t *testing.T) {
	body := "func use(c config.Config) {\n    // keep this comment\n    if c.ChatclbtRpc.DiscovType != \"\" {\n        client(c.ChatclbtRpc)\n    }\n}"
	root := writeGoRPCFixture(t, "", body)
	path := filepath.Join(root, "go", "svc", "service_context.go")
	before, _ := os.ReadFile(path)
	manifest := &model.Manifest{
		Workspace: model.Workspace{Policy: "local"},
		Policies: map[string]model.Policy{"local": {Drivers: model.PolicyDrivers{Runtime: "go-zero"}, Routing: model.PolicyRouting{LocalDependency: model.RouteRule{Mode: "replace", Value: map[string]interface{}{"target": "127.0.0.1:${dependency.port}"}}}}},
		Services: map[string]model.Service{"portal": {Path: ".", Runner: model.Runner{Workdir: "go"}, Dependencies: map[string]model.Dependency{"store": {Binding: "chatclbtRpc", LocalService: "store", Port: "rpc"}}}},
	}
	diagnostics := []string{}
	notes, err := repairWorkspaceRPCTargetGuards(manifest, root, &diagnostics)
	if err != nil || len(diagnostics) != 0 { t.Fatalf("diagnostics=%v err=%v", diagnostics, err) }
	if len(notes) != 1 || !strings.Contains(notes[0], "automatically added Target") { t.Fatalf("notes = %v", notes) }
	after, _ := os.ReadFile(path)
	want := strings.Replace(string(before), `c.ChatclbtRpc.DiscovType != ""`, `c.ChatclbtRpc.DiscovType != "" || c.ChatclbtRpc.Target != ""`, 1)
	if string(after) != want { t.Fatal("unexpected source edits") }
	if notes, err := repairWorkspaceRPCTargetGuards(manifest, root, &diagnostics); err != nil || len(notes) != 0 { t.Fatalf("not idempotent: %v %v", notes, err) }
}

func TestTargetRepairRejectsConcurrentSourceChange(t *testing.T) {
	root := writeGoRPCFixture(t, "", `func use(c config.Config) { if c.ChatclbtRpc.DiscovType != "" { client(c.ChatclbtRpc) } }`)
	err := InspectGoRPCTargetInitialization(root, "go", "chatclbtRpc")
	var guard *rpcTargetGuardError
	if !errors.As(err, &guard) || !guard.repairable { t.Fatalf("error = %v", err) }
	changed := append(append([]byte{}, guard.source...), []byte("\n// concurrent user edit\n")...)
	if err := os.WriteFile(guard.path, changed, 0600); err != nil { t.Fatal(err) }
	if err := repairRPCTargetGuard(guard); err == nil { t.Fatal("overwrote concurrent edit") }
	got, _ := os.ReadFile(guard.path)
	if string(got) != string(changed) { t.Fatal("source changed") }
}
