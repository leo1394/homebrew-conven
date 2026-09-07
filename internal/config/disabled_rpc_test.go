package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectGoRPCDisableCapabilitiesRecognizesRealGuards(t *testing.T) {
	tests := []struct {
		name   string
		helper string
	}{
		{
			name: "portal",
			helper: `func hasRpcClient(c zrpc.RpcClientConf) bool {
	return c.DiscovType != "" || c.Target != "" || len(c.Endpoints) > 0
}`,
		},
		{
			name: "rea API",
			helper: `func hasRpcClientConfig(c zrpc.RpcClientConf) bool {
	return c.DiscovType != "" || c.Target != "" || len(c.Endpoints) > 0 ||
		len(c.Etcd.Hosts) > 0 || c.Etcd.Key != ""
}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := writeGoRPCFixture(t, test.helper, `func NewServiceContext(c config.Config) {
	rpc := c.ChatclbtRpc
	if hasRpcClientConfig(rpc) {
		mustNewClient(rpc)
	}
}`)
			if test.name == "portal" {
				writeAnalyzerFile(t, filepath.Join(repository, "go", "svc", "service_context.go"), "package svc\n\nimport (\n\t\"example.com/service/go/config\"\n\t\"example.com/zrpc\"\n)\n\n"+test.helper+`

func NewServiceContext(c config.Config) {
	rpc := c.ChatclbtRpc
	if hasRpcClient(rpc) {
		mustNewClient(rpc)
	}
}
`)
			}

			result, err := InspectGoRPCDisableCapabilities(repository, filepath.Join(repository, "go"), []string{"chatclbtRpc", "missingRpc"})
			if err != nil {
				t.Fatal(err)
			}
			if result["chatclbtRpc"] != "guarded" || len(result) != 1 {
				t.Fatalf("capabilities = %#v", result)
			}
		})
	}
}

func TestInspectGoRPCDisableCapabilitiesReportsUnusedCommentedBinding(t *testing.T) {
	repository := writeGoRPCFixture(t, "", `func NewServiceContext(c config.Config) {
	// mustNewClient(c.ChatclbtRpc)
}`)

	result, err := InspectGoRPCDisableCapabilities(repository, "go", []string{"chatclbtRpc"})
	if err != nil {
		t.Fatal(err)
	}
	if result["chatclbtRpc"] != "unused" {
		t.Fatalf("capabilities = %#v", result)
	}
}

func TestInspectGoRPCDisableCapabilitiesRejectsUnsafeUses(t *testing.T) {
	tests := []struct {
		name       string
		helper     string
		body       string
		errorParts []string
	}{
		{
			name: "helper returning true",
			helper: `func hasRpcClientConfig(c zrpc.RpcClientConf) bool { return true }`,
			body: `func NewServiceContext(c config.Config) {
	if hasRpcClientConfig(c.ChatclbtRpc) { mustNewClient(c.ChatclbtRpc) }
}`,
			errorParts: []string{"chatclbtRpc", "unknown guard", "service_context.go:"},
		},
		{
			name: "consul guard",
			helper: `func hasRpcClientConfig(c zrpc.RpcClientConf) bool { return c.DiscovType != "" || len(c.Consul.Hosts) > 0 }`,
			body: `func NewServiceContext(c config.Config) {
	if hasRpcClientConfig(c.ChatclbtRpc) { mustNewClient(c.ChatclbtRpc) }
}`,
			errorParts: []string{"chatclbtRpc", "unknown guard"},
		},
		{
			name: "wrong binding guard",
			helper: validGoRPCFixtureHelper,
			body: `func NewServiceContext(c config.Config) {
	if hasRpcClientConfig(c.OtherRpc) { mustNewClient(c.ChatclbtRpc) }
}`,
			errorParts: []string{"chatclbtRpc", "unguarded"},
		},
		{
			name: "guard mutation",
			helper: validGoRPCFixtureHelper,
			body: `func NewServiceContext(c config.Config) {
	if hasRpcClientConfig(c.ChatclbtRpc) {
		c.ChatclbtRpc.Target = "remote"
		mustNewClient(c.ChatclbtRpc)
	}
}`,
			errorParts: []string{"chatclbtRpc", "mutated"},
		},
		{
			name: "unguarded second use",
			helper: validGoRPCFixtureHelper,
			body: `func NewServiceContext(c config.Config) {
	if hasRpcClientConfig(c.ChatclbtRpc) { mustNewClient(c.ChatclbtRpc) }
	mustNewClient(c.ChatclbtRpc)
}`,
			errorParts: []string{"chatclbtRpc", "unguarded"},
		},
		{
			name: "unknown wrapper",
			helper: `func hasRpcClientConfig(c zrpc.RpcClientConf) bool { return enabled(c) }`,
			body: `func NewServiceContext(c config.Config) {
	if hasRpcClientConfig(c.ChatclbtRpc) { mustNewClient(c.ChatclbtRpc) }
			}`,
			errorParts: []string{"chatclbtRpc", "unknown guard"},
		},
		{
			name:   "shadowed len",
			helper: `var len = func([]string) int { return 1 }

func hasRpcClientConfig(c zrpc.RpcClientConf) bool { return len(c.Endpoints) > 0 }`,
			body: `func NewServiceContext(c config.Config) {
	if hasRpcClientConfig(c.ChatclbtRpc) { mustNewClient(c.ChatclbtRpc) }
}`,
			errorParts: []string{"chatclbtRpc", "unknown guard"},
		},
		{
			name:   "unresolved nested holder",
			helper: validGoRPCFixtureHelper,
			body: `func NewServiceContext(holder Holder) {
	mustNewClient(holder.Config.ChatclbtRpc)
}`,
			errorParts: []string{"chatclbtRpc", "unresolved receiver"},
		},
		{
			name: "unresolved local config",
			body: `func NewServiceContext() {
	var c config.Config
	mustNewClient(c.ChatclbtRpc)
}`,
			errorParts: []string{"chatclbtRpc", "unresolved receiver"},
		},
		{
			name: "unresolved global config",
			body: `var c config.Config

func NewServiceContext() { mustNewClient(c.ChatclbtRpc) }`,
			errorParts: []string{"chatclbtRpc", "unresolved receiver"},
		},
		{
			name:   "unresolved package global",
			helper: validGoRPCFixtureHelper,
			body:   `var escaped = holder.Config.ChatclbtRpc`,
			errorParts: []string{"chatclbtRpc", "unresolved package-level receiver"},
		},
		{
			name: "package initializer",
			body: `var c config.Config

var client = mustNewClient(c.ChatclbtRpc)`,
			errorParts: []string{"chatclbtRpc", "unresolved package-level receiver"},
		},
		{
			name:   "local helper shadow",
			helper: validGoRPCFixtureHelper,
			body: `func NewServiceContext(c config.Config) {
	hasRpcClientConfig := func(zrpc.RpcClientConf) bool { return true }
	if hasRpcClientConfig(c.ChatclbtRpc) { mustNewClient(c.ChatclbtRpc) }
}`,
			errorParts: []string{"chatclbtRpc", "unknown guard"},
		},
		{
			name:   "impure false guard",
			helper: `func enabled(c zrpc.RpcClientConf) bool { return sideEffect(c) && c.Target != "" }`,
			body: `func NewServiceContext(c config.Config) {
	if enabled(c.ChatclbtRpc) { mustNewClient(c.ChatclbtRpc) }
}`,
			errorParts: []string{"chatclbtRpc", "unknown guard"},
		},
		{
			name: "side effect in condition",
			body: `func NewServiceContext(c config.Config) {
	if mustNewClient(c.ChatclbtRpc) != nil && c.ChatclbtRpc.Target != "" {}
}`,
			errorParts: []string{"chatclbtRpc", "unknown guard"},
		},
		{
			name: "mutating helper",
			helper: `func mutate(c *zrpc.RpcClientConf) bool { c.Target = "remote"; return true }

func enabled(c zrpc.RpcClientConf) bool { return mutate(&c) && c.Target != "" }`,
			body: `func NewServiceContext(c config.Config) {
	if enabled(c.ChatclbtRpc) { mustNewClient(c.ChatclbtRpc) }
}`,
			errorParts: []string{"chatclbtRpc", "unknown guard"},
		},
		{
			name:   "whole config assignment",
			helper: validGoRPCFixtureHelper,
			body: `func NewServiceContext(c config.Config) {
	c = config.Config{ChatclbtRpc: zrpc.RpcClientConf{Target: "remote"}}
	if hasRpcClientConfig(c.ChatclbtRpc) { mustNewClient(c.ChatclbtRpc) }
}`,
			errorParts: []string{"chatclbtRpc", "whole-config reassignment"},
		},
		{
			name:   "whole config pointer escape",
			helper: validGoRPCFixtureHelper,
			body: `func NewServiceContext(c config.Config) {
	configure(&c)
	if hasRpcClientConfig(c.ChatclbtRpc) { mustNewClient(c.ChatclbtRpc) }
}`,
			errorParts: []string{"chatclbtRpc", "pointer to its containing config"},
		},
		{
			name:   "whole config reflection escape",
			helper: validGoRPCFixtureHelper,
			body: `func NewServiceContext(c config.Config) {
	reflect.ValueOf(c).FieldByName("ChatclbtRpc")
	if hasRpcClientConfig(c.ChatclbtRpc) { mustNewClient(c.ChatclbtRpc) }
}`,
			errorParts: []string{"chatclbtRpc", "unknown whole-config call"},
		},
		{
			name:   "pointer config assignment",
			helper: validGoRPCFixtureHelper,
			body: `func NewServiceContext(c *config.Config) {
	*c = config.Config{ChatclbtRpc: zrpc.RpcClientConf{Target: "remote"}}
	if hasRpcClientConfig(c.ChatclbtRpc) { mustNewClient(c.ChatclbtRpc) }
}`,
			errorParts: []string{"chatclbtRpc", "whole-config reassignment"},
		},
		{
			name:   "parenthesized whole config escape",
			helper: validGoRPCFixtureHelper,
			body: `func NewServiceContext(c config.Config) {
	externalInitialize((c))
	if hasRpcClientConfig(c.ChatclbtRpc) { mustNewClient(c.ChatclbtRpc) }
}`,
			errorParts: []string{"chatclbtRpc", "unknown whole-config call"},
		},
		{
			name:   "parenthesized address escape",
			helper: validGoRPCFixtureHelper,
			body: `func NewServiceContext(c config.Config) {
	configure(&(c))
	if hasRpcClientConfig(c.ChatclbtRpc) { mustNewClient(c.ChatclbtRpc) }
}`,
			errorParts: []string{"chatclbtRpc", "pointer to its containing config"},
		},
		{
			name:   "dereferenced address assignment",
			helper: validGoRPCFixtureHelper,
			body: `func NewServiceContext(c config.Config) {
	*(&c) = config.Config{ChatclbtRpc: zrpc.RpcClientConf{Target: "remote"}}
	if hasRpcClientConfig(c.ChatclbtRpc) { mustNewClient(c.ChatclbtRpc) }
}`,
			errorParts: []string{"chatclbtRpc", "whole-config reassignment"},
		},
		{
			name:   "anonymous holder escape",
			helper: validGoRPCFixtureHelper,
			body: `func NewServiceContext(c config.Config) {
	h := struct{ C config.Config }{c}
	reflect.ValueOf(h.C).FieldByName("ChatclbtRpc")
	if hasRpcClientConfig(c.ChatclbtRpc) { mustNewClient(c.ChatclbtRpc) }
}`,
			errorParts: []string{"chatclbtRpc", "unrecognized whole-config composite"},
		},
		{
			name:   "named holder reflection",
			helper: validGoRPCFixtureHelper,
			body: `type Holder struct { C config.Config }

func NewServiceContext(c config.Config) {
	h := Holder{C: c}
	reflect.ValueOf(h.C).FieldByName("ChatclbtRpc")
	if hasRpcClientConfig(c.ChatclbtRpc) { mustNewClient(c.ChatclbtRpc) }
}`,
			errorParts: []string{"chatclbtRpc", "unknown whole-config call"},
		},
		{
			name:   "named holder dynamic reflection",
			helper: validGoRPCFixtureHelper,
			body: `type Holder struct { C config.Config }

func NewServiceContext(c config.Config) {
	h := Holder{C: c}
	name := "ChatclbtRpc"
	reflect.ValueOf(h.C).FieldByName(name)
	if hasRpcClientConfig(c.ChatclbtRpc) { mustNewClient(c.ChatclbtRpc) }
}`,
			errorParts: []string{"chatclbtRpc", "unknown whole-config call"},
		},
		{
			name:   "named holder indexed reflection",
			helper: validGoRPCFixtureHelper,
			body: `type Holder struct { C config.Config }

func NewServiceContext(c config.Config) {
	h := Holder{C: c}
	reflect.ValueOf(h.C).Field(0)
	if hasRpcClientConfig(c.ChatclbtRpc) { mustNewClient(c.ChatclbtRpc) }
}`,
			errorParts: []string{"chatclbtRpc", "unknown whole-config call"},
		},
		{
			name:   "address of named carrier",
			helper: validGoRPCFixtureHelper,
			body: `type Holder struct { C config.Config }

func NewServiceContext(c config.Config) {
	h := Holder{C: c}
	reflect.ValueOf(&h).Field(0)
	if hasRpcClientConfig(c.ChatclbtRpc) { mustNewClient(c.ChatclbtRpc) }
}`,
			errorParts: []string{"chatclbtRpc", "pointer to a config carrier"},
		},
		{
			name:   "parenthesized address of named carrier",
			helper: validGoRPCFixtureHelper,
			body: `type Holder struct { C config.Config }

func NewServiceContext(c config.Config) {
	h := Holder{C: c}
	reflect.ValueOf((&h)).Field(0)
	if hasRpcClientConfig(c.ChatclbtRpc) { mustNewClient(c.ChatclbtRpc) }
}`,
			errorParts: []string{"chatclbtRpc", "pointer to a config carrier"},
		},
		{
			name:   "pointer alias of named carrier",
			helper: validGoRPCFixtureHelper,
			body: `type Holder struct { C config.Config }

func NewServiceContext(c config.Config) {
	h := Holder{C: c}
	p := &h
	reflect.ValueOf(p).Field(0)
	if hasRpcClientConfig(c.ChatclbtRpc) { mustNewClient(c.ChatclbtRpc) }
}`,
			errorParts: []string{"chatclbtRpc", "pointer to a config carrier"},
		},
		{
			name:   "whole carrier reassignment",
			helper: validGoRPCFixtureHelper,
			body: `type Holder struct { C config.Config }

func NewServiceContext(c config.Config) {
	h := Holder{C: c}
	h = Holder{C: config.Config{ChatclbtRpc: zrpc.RpcClientConf{Target: "remote"}}}
	if hasRpcClientConfig(c.ChatclbtRpc) { mustNewClient(c.ChatclbtRpc) }
}`,
			errorParts: []string{"chatclbtRpc", "whole-carrier reassignment"},
		},
		{
			name:   "unknown carrier method",
			helper: validGoRPCFixtureHelper,
			body: `type Holder struct { C config.Config }

func NewServiceContext(c config.Config) {
	h := Holder{C: c}
	h.Mutate()
	if hasRpcClientConfig(c.ChatclbtRpc) { mustNewClient(c.ChatclbtRpc) }
}`,
			errorParts: []string{"chatclbtRpc", "config-carrier method call"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := writeGoRPCFixture(t, test.helper, test.body)
			_, err := InspectGoRPCDisableCapabilities(repository, "go", []string{"chatclbtRpc"})
			if err == nil {
				t.Fatal("unsafe RPC use was accepted")
			}
			for _, part := range test.errorParts {
				if !strings.Contains(err.Error(), part) {
					t.Fatalf("error = %q, missing %q", err, part)
				}
			}
		})
	}
}

func TestInspectGoRPCDisableCapabilitiesAllowsAnalyzedConfigForwarding(t *testing.T) {
	repository := writeGoRPCFixture(t, validGoRPCFixtureHelper, `func inspectConfig(c config.Config) {}

func NewServiceContext(c config.Config) {
	inspectConfig(c)
	if hasRpcClientConfig(c.ChatclbtRpc) { mustNewClient(c.ChatclbtRpc) }
}`)

	result, err := InspectGoRPCDisableCapabilities(repository, "go", []string{"chatclbtRpc"})
	if err != nil {
		t.Fatal(err)
	}
	if result["chatclbtRpc"] != "guarded" {
		t.Fatalf("capabilities = %#v", result)
	}
}

func TestInspectGoRPCDisableCapabilitiesAllowsParenthesizedFieldAccess(t *testing.T) {
	repository := writeGoRPCFixture(t, validGoRPCFixtureHelper, `func NewServiceContext(c *config.Config) {
	if hasRpcClientConfig(((c).ChatclbtRpc)) {
		mustNewClient(((c).ChatclbtRpc))
	}
}`)

	result, err := InspectGoRPCDisableCapabilities(repository, "go", []string{"chatclbtRpc"})
	if err != nil {
		t.Fatal(err)
	}
	if result["chatclbtRpc"] != "guarded" {
		t.Fatalf("capabilities = %#v", result)
	}
}

func TestInspectGoRPCDisableCapabilitiesRejectsCrossFileLenShadow(t *testing.T) {
	repository := writeGoRPCFixture(t, `func enabled(c zrpc.RpcClientConf) bool {
	return len(c.Endpoints) > 0
}`, `func NewServiceContext(c config.Config) {
	if enabled(c.ChatclbtRpc) { mustNewClient(c.ChatclbtRpc) }
}`)
	writeAnalyzerFile(t, filepath.Join(repository, "go", "svc", "len.go"), `package svc

func len([]string) int { return 1 }
`)

	_, err := InspectGoRPCDisableCapabilities(repository, "go", []string{"chatclbtRpc"})
	if err == nil || !strings.Contains(err.Error(), "unknown guard") {
		t.Fatalf("cross-file shadowed len error = %v", err)
	}
}

const validGoRPCFixtureHelper = `func hasRpcClientConfig(c zrpc.RpcClientConf) bool {
	return c.DiscovType != "" || c.Target != "" || len(c.Endpoints) > 0 ||
		len(c.Etcd.Hosts) > 0 || c.Etcd.Key != ""
}`

func writeGoRPCFixture(t *testing.T, helper, body string) string {
	t.Helper()
	repository := newAnalyzerRepository(t, "rpc-disable-service")
	writeAnalyzerFile(t, filepath.Join(repository, "go", "go.mod"), "module example.com/service/go\n")
	writeAnalyzerFile(t, filepath.Join(repository, "go", "config", "config.go"), `package config

import "example.com/zrpc"

type Config struct {
	ChatclbtRpc zrpc.RpcClientConf `+"`yaml:\"chatclbtRpc\"`"+`
	OtherRpc zrpc.RpcClientConf `+"`yaml:\"otherRpc\"`"+`
}
`)
	writeAnalyzerFile(t, filepath.Join(repository, "go", "svc", "service_context.go"), `package svc

import (
	"example.com/service/go/config"
	"example.com/zrpc"
)

`+helper+"\n\n"+body+"\n")
	return repository
}
