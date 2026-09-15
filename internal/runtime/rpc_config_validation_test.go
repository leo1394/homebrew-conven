package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/materialize"
)

func TestPreflightLocalTargetRejectsDiscoveryOnlyInitialization(t *testing.T) {
	plan := rpcConfigValidationTestPlan(t, "chatRpc: {target: '127.0.0.1:18090'}\n", false)
	service := plan.Services["api"]
	service.Config.Routes = []PlannedRoute{{Binding: "chatRpc", Local: true, Mode: "replace"}}
	path := filepath.Join(service.Directory, "service.go")
	if err := os.WriteFile(path, []byte("package service\nfunc use(c Config) { if c.Chat.DiscovType != \"\" { mustNewClient(c.Chat) } }\n"), 0600); err != nil { t.Fatal(err) }
	err := preflightRPCClientConfigs(plan)
	if err == nil || !strings.Contains(err.Error(), "service.go:2") || !strings.Contains(err.Error(), "local target initialization") { t.Fatalf("error = %v", err) }
	service.Config.Routes[0].Local = false
	if err := preflightRPCClientConfigs(plan); err != nil { t.Fatalf("remote route was affected: %v", err) }
	service.Config.Routes[0].Local = true
	if err := os.WriteFile(path, []byte("package service\nfunc use(c Config) { if c.Chat.DiscovType != \"\" || c.Chat.Target != \"\" { mustNewClient(c.Chat) } }\n"), 0600); err != nil { t.Fatal(err) }
	if err := preflightRPCClientConfigs(plan); err != nil { t.Fatal(err) }
}

func TestPreflightRPCClientConfigsValidRoutes(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{name: "Consul", yaml: "chatRpc: {discovType: consul, consul: {host: consul.local, port: 8500, key: chat.rpc}}\n"},
		{name: "target", yaml: "chatRpc: {target: 'dns:///chat.service:8080'}\n"},
		{name: "local target", yaml: "chatRpc: {target: '127.0.0.1:18081'}\n"},
		{name: "fork endpints", yaml: "chatRpc: {endpints: ['127.0.0.1:8080']}\n"},
		{name: "Etcd", yaml: "chatRpc: {etcd: {hosts: ['127.0.0.1:2379'], key: chat.rpc}}\n"},
		{name: "explicit Etcd", yaml: "chatRpc: {discovType: etcd, etcd: {hosts: ['127.0.0.1:2379'], key: chat.rpc}}\n"},
		{name: "explicit Etcd with target", yaml: "chatRpc: {discovType: etcd, target: '127.0.0.1:18081'}\n"},
		{name: "explicit Etcd with endpoints", yaml: "chatRpc: {discovType: etcd, endpints: ['127.0.0.1:18081']}\n"},
		{name: "negative timeout", yaml: "chatRpc: {target: '127.0.0.1:18081', timeout: -1}\n"},
		{name: "minimum timeout", yaml: "chatRpc: {target: '127.0.0.1:18081', timeout: -9223372036854775808}\n"},
		{name: "maximum timeout", yaml: "chatRpc: {target: '127.0.0.1:18081', timeout: 9223372036854775807}\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := rpcConfigValidationTestPlan(t, test.yaml, false)
			if err := preflightRPCClientConfigs(plan); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPreflightRPCClientConfigsRejectsInvalidRoutes(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		part string
	}{
		{name: "missing active config", yaml: "name: api\n", part: "chatRpc"},
		{name: "required blank", yaml: "chatRpc: {}\n", part: "no active RPC route"},
		{name: "unknown discovery", yaml: "chatRpc: {discovType: zookeeper}\n", part: "unsupported discovery"},
		{name: "Consul missing host", yaml: "chatRpc: {discovType: consul, consul: {port: 8500, key: chat.rpc}}\n", part: "consul.host"},
		{name: "Consul invalid port", yaml: "chatRpc: {discovType: consul, consul: {host: consul.local, port: bad, key: chat.rpc}}\n", part: "consul.port"},
		{name: "endpoints wrong type", yaml: "chatRpc: {endpoints: '127.0.0.1:8080'}\n", part: "endpoints must be a sequence"},
		{name: "unsupported endpoints spelling", yaml: "chatRpc: {endpoints: ['127.0.0.1:8080']}\n", part: "not consumed by RpcClientConf"},
		{name: "empty endpoint", yaml: "chatRpc: {endpoints: ['  ']}\n", part: "whitespace-only"},
		{name: "Etcd missing key", yaml: "chatRpc: {etcd: {hosts: ['127.0.0.1:2379']}}\n", part: "etcd.key"},
		{name: "explicit Etcd missing route", yaml: "chatRpc: {discovType: etcd}\n", part: "requires direct endpoints"},
		{name: "invalid target", yaml: "chatRpc: {target: 'dns:///%zz'}\n", part: "valid gRPC target"},
		{name: "invalid timeout type", yaml: "chatRpc: {target: '127.0.0.1:18081', timeout: {bad: type}}\n", part: "timeout must be"},
		{name: "overflow timeout", yaml: "chatRpc: {target: '127.0.0.1:18081', timeout: 999999999999999999999999}\n", part: "int64 range"},
		{name: "string timeout", yaml: "chatRpc: {target: '127.0.0.1:18081', timeout: invalid}\n", part: "timeout must be an integer"},
		{name: "invalid app type", yaml: "chatRpc: {target: '127.0.0.1:18081', app: [bad]}\n", part: "app must be a string"},
		{name: "invalid token type", yaml: "chatRpc: {target: '127.0.0.1:18081', token: {bad: type}}\n", part: "token must be a string"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := rpcConfigValidationTestPlan(t, test.yaml, false)
			err := preflightRPCClientConfigs(plan)
			if err == nil || !strings.Contains(err.Error(), "service api") || !strings.Contains(err.Error(), "binding chatRpc") || !strings.Contains(err.Error(), "application.yaml") || !strings.Contains(err.Error(), test.part) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestPreflightRPCClientConfigsDoesNotExposeCredentialValues(t *testing.T) {
	for _, secretConfig := range []string{
		"chatRpc: {target: '127.0.0.1:18081', app: [sensitive-app-value]}\n",
		"chatRpc: {target: '127.0.0.1:18081', token: {sensitive-token-value: bad}}\n",
	} {
		err := preflightRPCClientConfigs(rpcConfigValidationTestPlan(t, secretConfig, false))
		if err == nil {
			t.Fatal("malformed credential value was accepted")
		}
		if strings.Contains(err.Error(), "sensitive-app-value") || strings.Contains(err.Error(), "sensitive-token-value") {
			t.Fatalf("error exposes credential value: %v", err)
		}
	}
}

func TestPreflightRPCClientConfigsAllowsProvenOptionalBlank(t *testing.T) {
	plan := rpcConfigValidationTestPlan(t, "chatRpc: {discovType: ''}\n", true)
	if err := preflightRPCClientConfigs(plan); err != nil {
		t.Fatal(err)
	}
}

func TestPreflightRPCClientConfigsRejectsGuardedExplicitEtcdWithoutRoute(t *testing.T) {
	plan := rpcConfigValidationTestPlan(t, "chatRpc: {discovType: etcd}\n", true)
	err := preflightRPCClientConfigs(plan)
	if err == nil || !strings.Contains(err.Error(), "requires direct endpoints") {
		t.Fatalf("error = %v", err)
	}
}

func TestPreflightRPCClientConfigsRejectsMalformedUnusedDeclaration(t *testing.T) {
	for _, test := range []struct {
		yaml string
		part string
	}{
		{yaml: "chatRpc: invalid\n", part: "expected an RPC configuration mapping"},
		{yaml: "chatRpc: {consul: {port: invalid}}\n", part: "consul.port must be an integer"},
	} {
		plan := rpcConfigValidationTestPlan(t, test.yaml, true)
		service := plan.Services["api"]
		if err := os.WriteFile(filepath.Join(service.Directory, "service.go"), []byte("package service\n\nfunc use(c Config) {}\n"), 0600); err != nil {
			t.Fatal(err)
		}
		err := preflightRPCClientConfigs(plan)
		if err == nil || !strings.Contains(err.Error(), test.part) {
			t.Fatalf("error = %v", err)
		}
	}
}

func TestPreflightRPCClientConfigsAllowsIncompleteUnusedRoute(t *testing.T) {
	plan := rpcConfigValidationTestPlan(t, "chatRpc: {etcd: {hosts: ['127.0.0.1:2379']}}\n", true)
	service := plan.Services["api"]
	if err := os.WriteFile(filepath.Join(service.Directory, "service.go"), []byte("package service\n\nfunc use(c Config) {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := preflightRPCClientConfigs(plan); err != nil {
		t.Fatal(err)
	}
}

func TestPreflightRPCClientConfigsRejectsUnrelatedEndpointReplacement(t *testing.T) {
	plan := rpcConfigValidationTestPlan(t, "chatRpc: {endpints: ['127.0.0.1:18081']}\n", false)
	service := plan.Services["api"]
	configFile := filepath.Join(service.Directory, "config.go")
	source, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	source = []byte(strings.ReplaceAll(string(source), "github.com/tal-tech/go-zero/zrpc", "github.com/zeromicro/go-zero/zrpc"))
	if err := os.WriteFile(configFile, source, 0600); err != nil {
		t.Fatal(err)
	}
	err = preflightRPCClientConfigs(plan)
	if err == nil || !strings.Contains(err.Error(), "local go-zero module replacement") {
		t.Fatalf("error = %v", err)
	}
}

func TestPreflightRPCClientConfigsRejectsAmbiguousVersionedEndpointReplacement(t *testing.T) {
	plan := rpcConfigValidationTestPlan(t, "chatRpc: {endpoints: ['127.0.0.1:18081']}\n", false)
	service := plan.Services["api"]
	files := map[string]string{
		"go.mod": "module example.test/service\n\nrequire github.com/tal-tech/go-zero v1.5.5\nreplace (\n github.com/tal-tech/go-zero v1.0.0 => ./internal/decoy\n github.com/tal-tech/go-zero v1.5.5 => ./internal/go-zero\n)\n",
		"internal/decoy/go.mod": "module github.com/tal-tech/go-zero\n",
		"internal/decoy/zrpc/config.go": "package zrpc\n\ntype RpcClientConf struct { Endpoints []string `yaml:\"endpoints\"` }\n",
	}
	for name, contents := range files {
		filename := filepath.Join(service.Directory, name)
		if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	err := preflightRPCClientConfigs(plan)
	if err == nil || !strings.Contains(err.Error(), "version-specific or multiple replacements") {
		t.Fatalf("error = %v", err)
	}
	for _, fallback := range []string{"target: '127.0.0.1:18081'", "etcd: {hosts: ['127.0.0.1:2379'], key: chat.rpc}"} {
		application := "chatRpc: {endpoints: ['127.0.0.1:18081'], " + fallback + "}\n"
		if err := os.WriteFile(filepath.Join(service.Config.Plan.TargetDir, "application.yaml"), []byte(application), 0600); err != nil {
			t.Fatal(err)
		}
		err := preflightRPCClientConfigs(plan)
		if err == nil || !strings.Contains(err.Error(), "cannot verify direct endpoint spelling") {
			t.Fatalf("fallback must not mask an unknown higher-priority endpoint route: %v", err)
		}
	}
}

func rpcConfigValidationTestPlan(t *testing.T, application string, guarded bool) *Plan {
	t.Helper()
	repository := t.TempDir()
	source := `package service

import "github.com/tal-tech/go-zero/zrpc"

type Config struct {
	Chat zrpc.RpcClientConf ` + "`yaml:\"chatRpc\"`" + `
}
`
	if err := os.WriteFile(filepath.Join(repository, "config.go"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "go.mod"), []byte("module example.test/service\n\nreplace github.com/tal-tech/go-zero => ./internal/go-zero\n"), 0600); err != nil {
		t.Fatal(err)
	}
	goZero := filepath.Join(repository, "internal", "go-zero", "zrpc")
	if err := os.MkdirAll(goZero, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "internal", "go-zero", "go.mod"), []byte("module github.com/tal-tech/go-zero\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goZero, "config.go"), []byte("package zrpc\n\ntype RpcClientConf struct { Endpoints []string `yaml:\"endpints\"` }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	usage := "package service\n\nfunc use(c Config) { mustNewClient(c.Chat) }\n"
	if guarded {
		usage = `package service

import "github.com/tal-tech/go-zero/zrpc"

func hasRPCClient(c zrpc.RpcClientConf) bool {
	return c.DiscovType != "" || c.Target != "" || len(c.Endpoints) > 0 || len(c.Etcd.Hosts) > 0 || c.Etcd.Key != ""
}

func use(c Config) {
	if hasRPCClient(c.Chat) {
		mustNewClient(c.Chat)
	}
}
`
	}
	if err := os.WriteFile(filepath.Join(repository, "service.go"), []byte(usage), 0600); err != nil {
		t.Fatal(err)
	}
	configDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDirectory, "application.yaml"), []byte(application), 0600); err != nil {
		t.Fatal(err)
	}
	service := PlannedService{
		Name:      "api",
		Directory: repository,
		Workdir:   ".",
		Config: &PlannedConfig{
			Runtime: "go-zero",
			Plan: materialize.Plan{
				TargetDir:   configDirectory,
				Application: "application.yaml",
			},
		},
	}
	return &Plan{Order: []string{"api"}, Services: map[string]PlannedService{"api": service}}
}
