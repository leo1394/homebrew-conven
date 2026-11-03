package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuardDependencyInventoryCollectsIssuesAndLocalModes(t *testing.T) {
	inventory := guardDependencyInventory{Version: 1, Environment: "test"}
	bindings := map[string]string{"missingRpc": "remote", "badRpc": "remote", "localRpc": "local", "offRpc": "disabled", "consulRpc": "remote"}
	inventory.inspect("api", []byte(`badRpc: {target: 123}
localRpc: {target: '127.0.0.1:9000'}
offRpc: {}
consulRpc:
  discovType: consul
  consul: {host: registry.test, port: 8500, key: partner.rpc}
`), bindings, "endpoints", nil)
	inventory.sort()
	if len(inventory.Items) != 5 { t.Fatalf("items: %+v", inventory.Items) }
	wanted := map[string]string{"missingRpc": "invalid", "badRpc": "invalid", "localRpc": "local", "offRpc": "disabled", "consulRpc": "remote"}
	for _, item := range inventory.Items {
		if item.Mode != wanted[item.Source] { t.Fatalf("unexpected item: %+v", item) }
		if item.Source == "consulRpc" && (item.Protocol != "grpc" || item.RegistryHost != "registry.test" || item.RegistryPort != 8500 || item.Discovery != "partner.rpc") { t.Fatalf("consul: %+v", item) }
		if item.Source == "localRpc" && item.Host != "" { t.Fatal("local dependency exported as remote endpoint") }
	}
	inventory.inspect("other", []byte("secondRpc: {}\n"), map[string]string{"secondRpc": "remote", "thirdRpc": "remote"}, "endpoints", nil)
	if len(inventory.Items) != 7 { t.Fatal("issues in another service not collected") }
}

func TestGuardDependencyInventoryRedactsAndSorts(t *testing.T) {
	first := []byte(`listenOn: '0.0.0.0:8080'
server: {address: '0.0.0.0:9000'}
database: {dsn: 'dbuser:dbsecret@tcp(db.test:3306)/private?password=hidden'}
httpClient: {url: 'https://user:urlsecret@api.test:8443/private?token=querysecret'}
unknown: {endpoint: 'custom://user:secret@custom.test:7777/private?token=hidden'}
rpc: {endpoints: ['rpc.test:9090']}
`)
	second := []byte(`rpc: {endpoints: ['rpc.test:9090']}
unknown: {endpoint: 'custom://user:secret@custom.test:7777/private?token=hidden'}
httpClient: {url: 'https://user:urlsecret@api.test:8443/private?token=querysecret'}
database: {dsn: 'dbuser:dbsecret@tcp(db.test:3306)/private?password=hidden'}
server: {address: '0.0.0.0:9000'}
listenOn: '0.0.0.0:8080'
`)
	a, b := guardDependencyInventory{Version: 1}, guardDependencyInventory{Version: 1}
	a.inspect("api", first, map[string]string{"rpc": "remote"}, "endpoints", nil)
	b.inspect("api", second, map[string]string{"rpc": "remote"}, "endpoints", nil)
	a.sort(); b.sort()
	one, _ := json.Marshal(a); two, _ := json.Marshal(b)
	if string(one) != string(two) { t.Fatalf("unstable ordering:\n%s\n%s", one, two) }
	for _, secret := range []string{"dbuser", "dbsecret", "urlsecret", "querysecret", "private", "hidden", "0.0.0.0"} {
		if strings.Contains(string(one), secret) { t.Fatalf("leaked %s: %s", secret, one) }
	}
	if len(a.Items) != 4 { t.Fatalf("items: %s", one) }
	for _, item := range a.Items {
		if item.Source == "unknown.endpoint" && item.Mode != "unsupported" { t.Fatalf("unknown scheme silently supported: %+v", item) }
		if item.Source == "rpc.endpoints.0" && (item.Protocol != "grpc" || item.Host != "rpc.test" || item.Port != 9090) { t.Fatalf("RPC endpoint: %+v", item) }
	}
}

func TestGuardDependencyInventoryMalformedConsulAndUnsupported(t *testing.T) {
	inventory := guardDependencyInventory{}
	inventory.inspect("api", []byte("one: {discovType: consul, consul: {host: registry.test}}\ntwo: {discovType: consul}\nclient: {endpoint: '${RUNTIME_ADDRESS}'}\n"), nil, "endpoints", nil)
	if len(inventory.Items) != 3 { t.Fatalf("items: %+v", inventory.Items) }
	for _, item := range inventory.Items {
		if item.Source == "client.endpoint" { if item.Mode != "unsupported" { t.Fatalf("computed address: %+v", item) }; continue }
		if item.Mode != "invalid" || item.Code != "invalid-consul-client" { t.Fatalf("consul: %+v", item) }
	}
}

func TestGuardDependencyInventoryDatabaseMapsAndInvalidPorts(t *testing.T) {
	inventory := guardDependencyInventory{}
	inventory.inspect("api", []byte("mysql: {host: db.test, port: 3306, password: never-export}\nredis: {host: cache.test, port: invalid}\nclient: {url: 'https://api.test:0/path'}\n"), nil, "endpoints", nil)
	if len(inventory.Items) != 3 { t.Fatalf("items: %+v", inventory.Items) }
	for _, item := range inventory.Items {
		if item.Source == "mysql.host" {
			if item.Mode != "remote" || item.Format != "host-port-map" || item.Host != "db.test" || item.Port != 3306 { t.Fatalf("database map: %+v", item) }
		} else if item.Mode != "invalid" { t.Fatalf("invalid endpoint: %+v", item) }
	}
	data, _ := json.Marshal(inventory)
	if strings.Contains(string(data), "never-export") { t.Fatal("database credential leaked") }
}

func TestGuardDependencyInventoryExcludesNonOutboundURLs(t *testing.T) {
	inventory := guardDependencyInventory{}
	inventory.inspect("api", []byte(`banner: 'https://banner.test/welcome'
description: 'https://docs.test/help'
metadata: {url: 'https://metadata.test'}
callbackUrl: 'https://callback.test'
publicResponseUrl: 'https://public.test'
public: {url: 'https://public.test'}
response: {url: 'https://response.test'}
server: {host: listen.test, port: 8080}
httpClient: {url: 'https://outbound.test'}
unknownClient: {host: unknown.test, port: 9090}
`), nil, "endpoints", nil)
	if len(inventory.Items) != 2 { t.Fatalf("unexpected dependencies: %+v", inventory.Items) }
	for _, item := range inventory.Items {
		if item.Source == "httpClient.url" {
			if item.Protocol != "https" || item.Port != 443 || item.Host != "outbound.test" || item.Mode != "remote" { t.Fatalf("TLS endpoint: %+v", item) }
		} else if item.Source != "unknownClient.host" || item.Mode != "unsupported" || item.Host != "unknown.test" || item.Port != 9090 || item.Format != "host-port-map" { t.Fatalf("unknown split endpoint: %+v", item) }
	}
}

func TestGuardDependencyInventoryNormalizesDatabaseProtocols(t *testing.T) {
	inventory := guardDependencyInventory{}
	inventory.inspect("api", []byte(`postgresql: {host: db.test, port: 5432}
mysql: {host: mysql.test, port: 3306}
redis: {host: cache.test, port: 6379}
mongodb: {host: mongo.test, port: 27017}
database: {url: 'postgresql://user:secret@db.test/private?token=hidden'}
`), nil, "endpoints", nil)
	if len(inventory.Items) != 5 { t.Fatalf("database dependencies: %+v", inventory.Items) }
	for _, item := range inventory.Items {
		if item.Mode != "remote" { t.Fatalf("database endpoint: %+v", item) }
		if item.Source == "postgresql.host" || item.Source == "database.url" {
			if item.Protocol != "postgres" { t.Fatalf("postgres protocol not normalized: %+v", item) }
		}
	}
}

func TestGuardDependencyInventoryClassifiesDatabaseAddressFromContext(t *testing.T) {
	inventory := guardDependencyInventory{}
	inventory.inspect("any-service", []byte("mysql: {url: 'db.test:3306'}\nredis: {address: 'cache.test:6379'}\nunknownClient: {url: 'unknown.test:9090'}\n"), nil, "endpoints", nil)
	if len(inventory.Items) != 3 { t.Fatalf("dependencies: %+v", inventory.Items) }
	for _, item := range inventory.Items {
		if item.Source == "unknownClient.url" {
			if item.Mode != "unsupported" || item.Code != "unknown-endpoint-protocol" { t.Fatalf("unresolved endpoint skipped: %+v", item) }
		} else if item.Mode != "remote" || item.Protocol == "" || item.Format != "host-port" { t.Fatalf("database context lost: %+v", item) }
	}
}

func TestGuardDependencyInventoryExcludesSourceVerifiedResponseURLs(t *testing.T) {
	inventory := guardDependencyInventory{}
	inventory.inspect("any-service", []byte(`appH5: {qaUrl: 'https://qa.test', backendUrl: 'https://backend.test'}
reportH5: {planReportUrl: 'https://report.test', taskReportUrl: 'https://task-report.test', backendUrl: 'https://report-backend.test'}
cnpgStoreKpiDetailPageUrl: 'https://viewer.test'
redReset: {login: {loginUrl: 'https://login.test'}}
fileMan: {host: 'https://files.test'}
pmsClient: {host: 'https://pms.test'}
httpClient: {qaUrl: 'https://qa-backend.test'}
`), nil, "endpoints", nil)
	wanted := map[string]bool{"appH5.backendUrl":true,"reportH5.backendUrl":true,"fileMan.host":true,"pmsClient.host":true,"httpClient.qaUrl":true}
	if len(inventory.Items) != len(wanted) { t.Fatalf("response URLs treated as connects: %+v", inventory.Items) }
	for _, item := range inventory.Items { if !wanted[item.Source] || item.Mode != "remote" || item.Protocol != "https" { t.Fatalf("real outbound client skipped: %+v", item) } }
}

func TestGuardDependencyInventoryIgnoresInactiveServerConsul(t *testing.T) {
	for _, discovery := range []string{"disabled", "direct", ""} {
		inventory := guardDependencyInventory{}
		inventory.inspect("any-service", []byte("discovType: '"+discovery+"'\nconsul: {host: registry.test, port: 8500}\nclient: {url: 'https://backend.test'}\n"), nil, "endpoints", nil)
		if len(inventory.Items) != 1 || inventory.Items[0].Source != "client.url" { t.Fatalf("inactive server registration inventoried (%s): %+v", discovery, inventory.Items) }
	}
	inventory := guardDependencyInventory{}
	inventory.inspect("any-service", []byte("consul: {host: registry.test, port: 8500}\n"), nil, "endpoints", nil)
	if len(inventory.Items) != 1 || inventory.Items[0].Mode != "unsupported" { t.Fatal("unknown Consul usage silently skipped") }
}

func TestGuardDependencyInventoryRetainsKafkaWritersAsRemote(t *testing.T) {
	inventory := guardDependencyInventory{}
	inventory.inspect("any-service", []byte("mq: [{host: broker.test, port: 9092}]\ntopics: [{type: writer, name: events}]\n"), nil, "endpoints", nil)
	if len(inventory.Items) != 1 || inventory.Items[0].Source != "mq.0.host" || inventory.Items[0].Protocol != "kafka" || inventory.Items[0].Mode != "remote" { t.Fatalf("Kafka broker lost or misclassified: %+v", inventory.Items) }
}

func TestGuardDependencyInventoryConsulKeysAreBoundedAndIdentifyDependency(t *testing.T) {
	makeInventory := func(key string) guardDependencyInventory {
		inventory := guardDependencyInventory{}
		inventory.inspect("api", []byte("clientRpc:\n  discovType: consul\n  consul: {host: registry.test, port: 8500, key: '" + key + "'}\n"), map[string]string{"clientRpc": "remote"}, "endpoints", nil)
		return inventory
	}
	one, two := makeInventory("partner.rpc"), makeInventory("other.rpc")
	if len(one.Items) != 1 || len(two.Items) != 1 || one.Items[0].ID == two.Items[0].ID { t.Fatal("Consul service key absent from identity") }
	for _, key := range []string{"secret?token=hidden", "bad key", strings.Repeat("x", 257)} {
		inventory := makeInventory(key)
		if len(inventory.Items) != 1 || inventory.Items[0].Mode != "invalid" || inventory.Items[0].Discovery != "" { t.Fatalf("unsafe Consul key: %+v", inventory.Items) }
		data, _ := json.Marshal(inventory)
		if strings.Contains(string(data), key) { t.Fatal("unsafe discovery key leaked") }
	}
}

func TestPrepareGuardConfigurationsWritesPrivateInventory(t *testing.T) {
	workspace, responses, output := guardPreparationFixture(t)
	response := map[string]any{"appId": "alpha", "cluster": "default", "namespaceName": "application.yml", "configurations": map[string]string{"content": "client: {url: 'https://user:secret@remote.test/path?token=hidden'}\n"}}
	data, _ := json.Marshal(response)
	if err := os.WriteFile(filepath.Join(responses, "alpha.json"), data, 0600); err != nil { t.Fatal(err) }
	dir, err := PrepareGuardConfigurations(context.Background(), workspace, "test", []string{"alpha", "beta"}, responses, output)
	if err != nil { t.Fatal(err) }
	path := filepath.Join(dir, "dependencies.json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 { t.Fatalf("private inventory: %v %v", info, err) }
	data, err = os.ReadFile(path)
	if err != nil { t.Fatal(err) }
	var inventory guardDependencyInventory
	if err := json.Unmarshal(data, &inventory); err != nil { t.Fatal(err) }
	if inventory.Version != 1 || inventory.Environment != "test" || len(inventory.Items) != 1 || inventory.Items[0].Host != "remote.test" { t.Fatalf("inventory: %s", data) }
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "hidden") { t.Fatal("inventory leaked secrets") }
}

func TestPrepareGuardConfigurationsInspectsUndeclaredSourceBindings(t *testing.T) {
	workspace, responses, output := guardPreparationFixture(t)
	policy := workspace.Manifest.Policies["apollo"]
	policy.Drivers.Runtime = "go-zero"
	policy.Drivers.Framework = "go-zero"
	policy.Drivers.Discovery = "consul"
	workspace.Manifest.Policies["apollo"] = policy
	for _, name := range []string{"alpha", "beta"} {
		source := "package main\nimport \"github.com/tal-tech/go-zero/zrpc\"\ntype Config struct { Missing zrpc.RpcClientConf `yaml:\"missingRpc\"`; Broken zrpc.RpcClientConf `yaml:\"brokenRpc\"` }\n"
		if err := os.WriteFile(filepath.Join(workspace.Root, name, "main.go"), []byte(source), 0600); err != nil { t.Fatal(err) }
	}
	dir, err := PrepareGuardConfigurations(context.Background(), workspace, "test", []string{"beta", "alpha"}, responses, output)
	if err != nil { t.Fatal(err) }
	data, err := os.ReadFile(filepath.Join(dir, "dependencies.json"))
	if err != nil { t.Fatal(err) }
	var inventory guardDependencyInventory
	if err := json.Unmarshal(data, &inventory); err != nil { t.Fatal(err) }
	if len(inventory.Items) != 4 { t.Fatalf("source-inspected issues: %s", data) }
	for _, item := range inventory.Items {
		if item.Mode != "invalid" || item.Code != "missing-rpc-client" { t.Fatalf("source binding not inspected: %+v", item) }
	}
}

func TestPrepareGuardDependencyReportCollectsIndependentFailures(t *testing.T) {
	workspace, responses, output := guardPreparationFixture(t)
	if err := os.WriteFile(filepath.Join(responses, "alpha.json"), []byte(`{"appId":"wrong","configurations":{"content":"never-export-this-secret"}}`), 0600); err != nil { t.Fatal(err) }
	if err := os.WriteFile(filepath.Join(responses, "beta.json"), []byte("invalid-never-export-this-secret"), 0600); err != nil { t.Fatal(err) }
	dir, err := PrepareGuardDependencyReport(context.Background(), workspace, "test", []string{"alpha", "beta"}, responses, output)
	if err != nil { t.Fatal(err) }
	data, err := os.ReadFile(filepath.Join(dir, "dependencies.json"))
	if err != nil { t.Fatal(err) }
	var inventory guardDependencyInventory
	if err := json.Unmarshal(data, &inventory); err != nil { t.Fatal(err) }
	if len(inventory.Items) != 2 { t.Fatalf("independent failures: %s", data) }
	for _, item := range inventory.Items { if item.Mode != "invalid" || item.Code != "CONFIG_PREPARATION_FAILED" { t.Fatalf("failure: %+v", item) } }
	if strings.Contains(string(data), "never-export") { t.Fatal("preparation error leaked content") }
	data, err = os.ReadFile(filepath.Join(dir, "sources.json"))
	if err != nil { t.Fatal(err) }
	var sources guardPreparedSources
	if err := json.Unmarshal(data, &sources); err != nil { t.Fatal(err) }
	if len(sources.Services) != 0 { t.Fatal("failed sources retained") }
}

func TestPrepareGuardDependencyReportRetainsSuccessfulServices(t *testing.T) {
	workspace, responses, output := guardPreparationFixture(t)
	if err := os.WriteFile(filepath.Join(responses, "alpha.json"), []byte("invalid"), 0600); err != nil { t.Fatal(err) }
	dir, err := PrepareGuardDependencyReport(context.Background(), workspace, "test", []string{"alpha", "beta"}, responses, output)
	if err != nil { t.Fatal(err) }
	data, err := os.ReadFile(filepath.Join(dir, "sources.json"))
	if err != nil { t.Fatal(err) }
	var sources guardPreparedSources
	if err := json.Unmarshal(data, &sources); err != nil { t.Fatal(err) }
	if len(sources.Services) != 1 || len(sources.Services["beta"].Application) == 0 { t.Fatal("independent service was not prepared") }
	if _, err := PrepareGuardDependencyReport(context.Background(), workspace, "unknown", []string{"alpha"}, responses, output); err == nil { t.Fatal("global invalid environment retained as report") }
}

func TestGuardDependencyInventoryIncludesRdmAddr(t *testing.T) {
	inventory:=guardDependencyInventory{}
	inventory.inspect("partner-service",[]byte("rdm: {addr: '10.2.4.8:6379', password: private}\n"),nil,"endpoints",nil)
	if len(inventory.Items)!=1 { t.Fatalf("Redis dependency missed: %+v",inventory.Items) }
	item:=inventory.Items[0]
	if item.Source!="rdm.addr" || item.Protocol!="redis" || item.Mode!="remote" || item.Format!="host-port" || item.Host!="10.2.4.8" || item.Port!=6379 { t.Fatalf("incorrect Redis dependency: %+v",item) }
}

func TestGuardDependencyInventoryIncludesRedisConfigAddr(t *testing.T) {
	inventory:=guardDependencyInventory{}
	inventory.inspect("visit-plan-mgr-service",[]byte("redisConfig: {addr: '10.2.4.8:6379'}\n"),nil,"endpoints",nil)
	if len(inventory.Items)!=1 || inventory.Items[0].Protocol!="redis" || inventory.Items[0].Mode!="remote" { t.Fatalf("Redis config not classified: %+v",inventory.Items) }
}

func TestGuardRedisInventoryForms(t *testing.T) {
	for _,test:=range []struct{name,document,protocol,mode string}{
		{"rdm","rdm: {addr: 'cache.test:6379'}","redis","remote"},
		{"redisConfig","redisConfig: {addr: 'cache.test:6379'}","redis","remote"},
		{"normalized","redis_config: {address: 'cache.test:6379'}","redis","remote"},
		{"nested","clients: {redis: {host: cache.test, port: 6379}}","redis","remote"},
		{"url","client: {url: 'redis://cache.test:6379/0'}","redis","remote"},
		{"unknown","cacheClient: {addr: 'cache.test:6379'}","","unsupported"},
		{"empty","redis: {addr: ''}","redis","invalid"},
		{"missing","redisConfig: {db: 0}","redis","invalid"},
	} { t.Run(test.name,func(t *testing.T){
		inventory:=guardDependencyInventory{};inventory.inspect("selected",[]byte(test.document),nil,"endpoints",nil)
		if len(inventory.Items)!=1 || inventory.Items[0].Protocol!=test.protocol || inventory.Items[0].Mode!=test.mode { t.Fatalf("unexpected inventory: %+v",inventory.Items) }
	}) }
}
