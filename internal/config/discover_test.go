package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestScanServicesFindsSupportedDirectChildRepositories(t *testing.T) {
	workspace := t.TempDir()
	writeGoServiceRepository(t, workspace, "alpha-service", false, "example.com/alpha-service", "main")
	writeGoServiceRepository(t, workspace, "beta-service", true, "example.com/beta-service", "main")
	writeGoServiceRepository(t, workspace, "wrong-module", false, "example.com/different", "main")
	writeGoServiceRepository(t, workspace, "not-main", false, "example.com/not-main", "service")
	writeGoServiceRepository(t, workspace, "malformed-main", false, "example.com/malformed-main", "main")
	writeDiscoveryFile(t, filepath.Join(workspace, "malformed-main", "go", "main.go"), "package\n")
	writeGoServiceRepository(t, workspace, "missing-module", false, "example.com/missing-module", "main")
	writeDiscoveryFile(t, filepath.Join(workspace, "missing-module", "go", "go.mod"), "go 1.24\n")
	mustMkdirAll(t, filepath.Join(workspace, "unsupported", ".git"))
	writeGoServiceRepository(t, filepath.Join(workspace, "container"), "nested-service", false, "example.com/nested-service", "main")
	writeDiscoveryFile(t, filepath.Join(workspace, "plain-directory", "go", "go.mod"), "module example.com/plain-directory\n")
	writeDiscoveryFile(t, filepath.Join(workspace, "plain-directory", "go", "main.go"), "package main\n")

	services, skipped, err := ScanServices(workspace)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(services))
	for _, service := range services {
		names = append(names, service.Name)
		if service.Path != service.Name || service.Runner.Workdir != "go" {
			t.Fatalf("service = %#v", service)
		}
		if !reflect.DeepEqual(service.Runner.Build, []string{"go", "build", "-o", "${artifact}", "."}) {
			t.Fatalf("build = %#v", service.Runner.Build)
		}
		if !reflect.DeepEqual(service.Runner.Run, []string{"${artifact}"}) {
			t.Fatalf("run = %#v", service.Runner.Run)
		}
	}
	if !reflect.DeepEqual(names, []string{"alpha-service", "beta-service"}) {
		t.Fatalf("discovered = %v", names)
	}
	if !reflect.DeepEqual(skipped, []string{"malformed-main", "missing-module", "not-main", "unsupported", "wrong-module"}) {
		t.Fatalf("skipped = %v", skipped)
	}
}

func TestInitStoresDiscoveredDescriptionOnlyInWorkspaceConvenDirectory(t *testing.T) {
	workspace := t.TempDir()
	writeGoServiceRepository(t, workspace, "api-service", false, "example.com/api-service", "main")
	repository := filepath.Join(workspace, "api-service")
	writeDiscoveryFile(t, filepath.Join(repository, "go", "config", "config.go"), `package config

type Config struct {
	Server rest.RestConf `+"`yaml:\",inline\"`"+`
	Partner zrpc.RpcClientConf `+"`yaml:\"partnerRpc\"`"+`
}
`)
	before := analyzerRepositorySnapshot(t, repository)

	result, err := InitWorkspaceDetails(workspace, []byte("fallback\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created {
		t.Fatal("workspace manifest was not created")
	}
	manifest, err := Load(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	service := manifest.Services["api-service"]
	if !reflect.DeepEqual(service.Kinds, []string{"http"}) {
		t.Fatalf("service kinds = %v", service.Kinds)
	}
	if !reflect.DeepEqual(service.Ports, map[string]int{"http": 18080}) {
		t.Fatalf("service ports = %v", service.Ports)
	}
	if service.Discovery.Analyzer != "go-subdirectory-module" || !reflect.DeepEqual(service.Discovery.ConsumerBindings, []string{"partnerRpc"}) {
		t.Fatalf("service discovery = %#v", service.Discovery)
	}
	after := analyzerRepositorySnapshot(t, repository)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("source repository changed during init:\nbefore=%#v\nafter=%#v", before, after)
	}
	if _, err := os.Stat(filepath.Join(repository, ".conven")); !os.IsNotExist(err) {
		t.Fatalf("init created a source repository .conven directory: %v", err)
	}
}

func TestInitWorkspaceUsesDiscoveredServicesAndPreservesManifest(t *testing.T) {
	workspace := t.TempDir()
	writeGoServiceRepository(t, workspace, "account-service", false, "example.com/account-service", "main")
	writeGoServiceRepository(t, workspace, "gateway-service", false, "example.com/gateway-service", "main")

	path, created, err := InitWorkspace(workspace, []byte("fallback\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("manifest was not created")
	}
	manifest, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ServiceNames(manifest), []string{"account-service", "gateway-service"}) {
		t.Fatalf("services = %v", ServiceNames(manifest))
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, created, err = InitWorkspace(workspace, []byte("replacement\n"))
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("existing manifest was overwritten")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("second init changed the manifest")
	}
}

func TestDiscoverWorkspaceAddsByPathAndPreservesManualConfiguration(t *testing.T) {
	workspace := t.TempDir()
	writeGoServiceRepository(t, workspace, "account-service", false, "example.com/account-service", "main")
	writeGoServiceRepository(t, workspace, "new-service", false, "example.com/new-service", "main")
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, `# keep top comment
version: 2
workspace:
  name: test
services:
  account-custom:
    # keep service comment
    path: account-service
    runner:
      run: [custom-runner]
    ports:
      rpc: 18081
  old-service:
    path: old-service
    runner:
      workdir: go
      build: [go, build, -o, "${artifact}", .]
      run: ["${artifact}"]
`)

	result, err := DiscoverWorkspace(manifestPath, workspace, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Added, []string{"new-service"}) {
		t.Fatalf("added = %v", result.Added)
	}
	if !reflect.DeepEqual(result.Existing, []string{"account-custom"}) {
		t.Fatalf("existing = %v", result.Existing)
	}
	if !reflect.DeepEqual(result.Missing, []string{"old-service"}) {
		t.Fatalf("missing = %v", result.Missing)
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, preserved := range []string{"# keep top comment", "# keep service comment", "custom-runner", "rpc: 18081"} {
		if !strings.Contains(text, preserved) {
			t.Fatalf("manifest lost %q:\n%s", preserved, text)
		}
	}
	manifest, err := Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ServiceNames(manifest), []string{"account-custom", "new-service", "old-service"}) {
		t.Fatalf("services = %v", ServiceNames(manifest))
	}

	result, err = DiscoverWorkspace(manifestPath, workspace, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Pruned, []string{"old-service"}) {
		t.Fatalf("pruned = %v", result.Pruned)
	}
	manifest, err = Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ServiceNames(manifest), []string{"account-custom", "new-service"}) {
		t.Fatalf("services after prune = %v", ServiceNames(manifest))
	}
}

func TestDiscoverWorkspaceAssignsStableLocalPortsToNewServices(t *testing.T) {
	workspace := t.TempDir()
	writeGoServiceRepository(t, workspace, "customer-custom-service", false, "example.com/customer-custom-service", "main")
	writeDiscoveryFile(t, filepath.Join(workspace, "customer-custom-service", "go", "config", "config.go"), `package config

type Config struct {
	zrpc.RpcServerConf
}
`)
	writeGoServiceRepository(t, workspace, "portal-new-service", false, "example.com/portal-new-service", "main")
	writeDiscoveryFile(t, filepath.Join(workspace, "portal-new-service", "go", "config", "config.go"), `package config

type Config struct {
	rest.RestConf
}
`)
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, `version: 2
workspace:
  name: test
services:
  existing-service:
    path: existing-service
    runner:
      run: [existing-service]
    ports:
      http: 18080
      metrics: 18082
`)

	result, err := DiscoverWorkspace(manifestPath, workspace, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Added, []string{"customer-custom-service", "portal-new-service"}) {
		t.Fatalf("added = %v", result.Added)
	}
	manifest, err := Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(manifest.Services["customer-custom-service"].Ports, map[string]int{"rpc": 18081}) {
		t.Fatalf("customer ports = %v", manifest.Services["customer-custom-service"].Ports)
	}
	if !reflect.DeepEqual(manifest.Services["portal-new-service"].Ports, map[string]int{"http": 18083}) {
		t.Fatalf("portal ports = %v", manifest.Services["portal-new-service"].Ports)
	}
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err = DiscoverWorkspace(manifestPath, workspace, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Added) != 0 || len(result.Updated) != 0 {
		t.Fatalf("repeat discovery changed services: added=%v updated=%v", result.Added, result.Updated)
	}
	after, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("repeat discovery changed assigned ports")
	}
}

func TestDiscoverWorkspaceBackfillsPortForPreviouslyDiscoveredService(t *testing.T) {
	workspace := t.TempDir()
	writeGoServiceRepository(t, workspace, "customer-custom-service", false, "example.com/customer-custom-service", "main")
	writeDiscoveryFile(t, filepath.Join(workspace, "customer-custom-service", "go", "config", "config.go"), `package config

type Config struct {
	zrpc.RpcServerConf
}
`)
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, `version: 2
workspace:
  name: test
services:
  customer-custom-service:
    path: customer-custom-service
    kind: rpc
    discovery:
      analyzer: go-subdirectory-module
    runner:
      workdir: go
      build: [go, build, -o, "${artifact}", .]
      run: ["${artifact}"]
    ports: {}
`)

	result, err := DiscoverWorkspace(manifestPath, workspace, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Updated, []string{"customer-custom-service"}) {
		t.Fatalf("updated = %v", result.Updated)
	}
	manifest, err := Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(manifest.Services["customer-custom-service"].Ports, map[string]int{"rpc": 18080}) {
		t.Fatalf("customer ports = %v", manifest.Services["customer-custom-service"].Ports)
	}
}

func TestDiscoverWorkspaceAddsKindPortWithoutChangingOtherPorts(t *testing.T) {
	workspace := t.TempDir()
	writeGoServiceRepository(t, workspace, "customer-custom-service", false, "example.com/customer-custom-service", "main")
	writeDiscoveryFile(t, filepath.Join(workspace, "customer-custom-service", "go", "config", "config.go"), `package config

type Config struct {
	zrpc.RpcServerConf
}
`)
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, `version: 2
workspace:
  name: test
services:
  customer-custom-service:
    path: customer-custom-service
    kind: rpc
    runner:
      run: [customer-custom-service]
    ports:
      metrics: 18080
`)

	result, err := DiscoverWorkspace(manifestPath, workspace, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Updated, []string{"customer-custom-service"}) {
		t.Fatalf("updated = %v", result.Updated)
	}
	manifest, err := Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(manifest.Services["customer-custom-service"].Ports, map[string]int{"metrics": 18080, "rpc": 18081}) {
		t.Fatalf("customer ports = %v", manifest.Services["customer-custom-service"].Ports)
	}
}

func TestDiscoverWorkspaceBackfillsOnlyEmptyDiscoveredFacts(t *testing.T) {
	workspace := t.TempDir()
	writeGoServiceRepository(t, workspace, "api-service", false, "example.com/api-service", "main")
	repository := filepath.Join(workspace, "api-service")
	writeDiscoveryFile(t, filepath.Join(repository, "go", "config", "config.go"), `package config

type Config struct {
	Server rest.RestConf `+"`yaml:\",inline\"`"+`
	Partner zrpc.RpcClientConf `+"`yaml:\"partnerRpc\"`"+`
}
`)
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, `version: 1
workspace:
  name: test
services:
  custom-api:
    # manual fields stay authoritative
    path: api-service
    runner:
      run: [custom]
`)
	repositoryBefore := analyzerRepositorySnapshot(t, repository)

	result, err := DiscoverWorkspace(manifestPath, workspace, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Updated, []string{"custom-api"}) {
		t.Fatalf("updated = %v", result.Updated)
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"# manual fields stay authoritative", "run: [custom]", "kind: http", "analyzer: go-subdirectory-module", "partnerRpc"} {
		if !strings.Contains(string(data), expected) {
			t.Fatalf("updated manifest is missing %q:\n%s", expected, data)
		}
	}

	manual := strings.Replace(string(data), "kind: http", "kind: rpc\n    network:\n      listen: all-interfaces", 1)
	manual = strings.Replace(manual, "analyzer: go-subdirectory-module", "analyzer: manual", 1)
	if err := os.WriteFile(manifestPath, []byte(manual), 0600); err != nil {
		t.Fatal(err)
	}
	result, err = DiscoverWorkspace(manifestPath, workspace, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Updated) != 0 {
		t.Fatalf("manual discovered facts were overwritten: %v", result.Updated)
	}
	manifest, err := Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != 1 || manifest.Services["custom-api"].Kind != "rpc" || manifest.Services["custom-api"].Network.Listen != "all-interfaces" || manifest.Services["custom-api"].Discovery.Analyzer != "manual" {
		t.Fatalf("manual facts changed: %#v", manifest.Services["custom-api"])
	}
	if repositoryAfter := analyzerRepositorySnapshot(t, repository); !reflect.DeepEqual(repositoryAfter, repositoryBefore) {
		t.Fatalf("source repository changed during registry:\nbefore=%#v\nafter=%#v", repositoryBefore, repositoryAfter)
	}
	if _, err := os.Stat(filepath.Join(repository, ".conven")); !os.IsNotExist(err) {
		t.Fatalf("registry created a source repository .conven directory: %v", err)
	}
}

func TestDiscoverWorkspaceRejectsPruneWithRemainingDependency(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, `version: 2
workspace:
  name: test
services:
  consumer:
    path: nested/consumer
    runner:
      run: [consumer]
    dependencies:
      old-service:
        localService: old-service
        remoteEnv:
          OLD_SERVICE: remote
  old-service:
    path: old-service
    runner:
      run: [old-service]
`)
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	_, err = DiscoverWorkspace(manifestPath, workspace, true)
	if err == nil || !strings.Contains(err.Error(), "localService references unknown service") {
		t.Fatalf("error = %v", err)
	}
	after, readErr := os.ReadFile(manifestPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(before) {
		t.Fatal("failed prune changed the manifest")
	}
}

func TestDiscoverWorkspacePrunePreservesExistingUnsupportedRepository(t *testing.T) {
	workspace := t.TempDir()
	mustMkdirAll(t, filepath.Join(workspace, "java-service", ".git"))
	mustMkdirAll(t, filepath.Join(workspace, "java-service", "src"))
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, `version: 2
workspace:
  name: test
services:
  java-service:
    path: java-service
    runner:
      run: [java, -jar, app.jar]
  removed-service:
    path: removed-service
    runner:
      run: [removed-service]
`)

	result, err := DiscoverWorkspace(manifestPath, workspace, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Pruned, []string{"removed-service"}) {
		t.Fatalf("pruned = %v", result.Pruned)
	}
	manifest, err := Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ServiceNames(manifest), []string{"java-service"}) {
		t.Fatalf("services = %v", ServiceNames(manifest))
	}
}

func TestDiscoverWorkspaceRejectsNamePathConflictsWithoutWriting(t *testing.T) {
	workspace := t.TempDir()
	writeGoServiceRepository(t, workspace, "alpha-service", false, "example.com/alpha-service", "main")
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, `version: 2
workspace:
  name: test
services:
  alpha-service:
    path: another-repository
    runner:
      run: [alpha-service]
  custom-name:
    path: alpha-service
    runner:
      run: [custom]
`)
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	_, err = DiscoverWorkspace(manifestPath, workspace, false)
	if err == nil || !strings.Contains(err.Error(), "conflicts with existing path") {
		t.Fatalf("error = %v", err)
	}
	after, readErr := os.ReadFile(manifestPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(before) {
		t.Fatal("conflict changed the manifest")
	}
}

func TestDiscoverWorkspaceRejectsPruneThatLeavesDanglingYAMLAlias(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, `version: 2
workspace:
  name: test
services:
  removed:
    path: removed
    runner: &shared-runner
      run: [service]
  retained:
    path: nested/retained
    runner: *shared-runner
`)
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	_, err = DiscoverWorkspace(manifestPath, workspace, true)
	if err == nil || !strings.Contains(err.Error(), "validate updated Conven manifest") {
		t.Fatalf("error = %v", err)
	}
	after, readErr := os.ReadFile(manifestPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(before) {
		t.Fatal("invalid alias prune changed the manifest")
	}
}

func TestDiscoverWorkspaceRejectsSymbolicLinkManifest(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "manifest-target.yaml")
	writeDiscoveryFile(t, target, `version: 2
workspace:
  name: test
services:
  retained:
    path: nested/retained
    runner:
      run: [retained]
`)
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	mustMkdirAll(t, filepath.Dir(manifestPath))
	if err := os.Symlink(target, manifestPath); err != nil {
		t.Fatal(err)
	}

	_, err := DiscoverWorkspace(manifestPath, workspace, false)
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("error = %v", err)
	}
	info, statErr := os.Lstat(manifestPath)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("discovery replaced the manifest symbolic link")
	}
}

func TestSaveManifestDocumentRejectsConcurrentEdit(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, `version: 2
workspace:
  name: test
services:
  retained:
    path: nested/retained
    runner:
      run: [retained]
`)
	source, sourceInfo, err := readManifestForUpdate(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	document, _, err := loadManifestDocument(source, manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := decodeManifest(source, manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	edited := append(append([]byte(nil), source...), []byte("# concurrent edit\n")...)
	if err := os.WriteFile(manifestPath, edited, 0600); err != nil {
		t.Fatal(err)
	}

	err = saveManifestDocument(manifestPath, document, source, sourceInfo, expected)
	if err == nil || !strings.Contains(err.Error(), "edited during discovery") {
		t.Fatalf("error = %v", err)
	}
	after, readErr := os.ReadFile(manifestPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(edited) {
		t.Fatal("concurrent edit was overwritten")
	}
}

func TestDiscoverWorkspaceRejectsPruneHiddenByYAMLMerge(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, `version: 2
workspace:
  name: test
services:
  <<:
    removed:
      path: removed
      runner:
        run: [removed]
  retained:
    path: nested/retained
    runner:
      run: [retained]
`)
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	_, err = DiscoverWorkspace(manifestPath, workspace, true)
	if err == nil || !strings.Contains(err.Error(), "does not match the validated discovery result") {
		t.Fatalf("error = %v", err)
	}
	after, readErr := os.ReadFile(manifestPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(before) {
		t.Fatal("failed merged-key prune changed the manifest")
	}
}

func TestDiscoverWorkspaceAddsSpringBootServiceWithUniquePolicyAndPort(t *testing.T) {
	workspace := t.TempDir()
	writeSpringBootServiceRepository(t, workspace, "data-mart-service")
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, `version: 2
workspace:
  name: test
  policy: go-workspace
policies:
  go-workspace: {}
  spring-boot-consul:
    drivers:
      framework: spring-boot
      configSource: repository
      discovery: consul
      materializer: yaml-overlay
    config:
      sourceDir: src/main/resources
      application: application.yml
    process:
      args:
        - "--spring.config.location=file:${configDir}/"
        - "--service.registration.enabled=false"
        - "--spring.cloud.consul.discovery.register=false"
    routing:
      servers:
        rpc:
          port: rpc
          args:
            - "--grpc.server.address=127.0.0.1"
            - "--grpc.server.port=${port.rpc}"
          isolation:
            registration:
              mode: config
              path: service.registration.enabled
              disabledValue: false
            listener:
              path: grpc.server.address
              value: 127.0.0.1
services:
  existing-service:
    path: existing-service
    runner:
      run: [existing]
    ports:
      rpc: 18086
`)

	result, err := DiscoverWorkspace(manifestPath, workspace, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Added, []string{"data-mart-service"}) || !reflect.DeepEqual(result.Assigned, []string{"data-mart-service.rpc=18080"}) {
		t.Fatalf("discovery result = %#v", result)
	}
	manifest, err := Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	service := manifest.Services["data-mart-service"]
	if service.Policy != "spring-boot-consul" || service.Kind != "rpc" || service.Ports["rpc"] != 18080 {
		t.Fatalf("Spring service = %#v", service)
	}
	if service.Runner.Artifact != "${serviceDir}/build/libs/datamart-0.0.1-SNAPSHOT.jar" || service.Health.Address != "127.0.0.1:${port.rpc}" {
		t.Fatalf("Spring runner/health = %#v / %#v", service.Runner, service.Health)
	}
}

func TestDiscoverWorkspaceFailsAtomicallyWithoutUniqueSpringPolicy(t *testing.T) {
	workspace := t.TempDir()
	writeSpringBootServiceRepository(t, workspace, "data-mart-service")
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	source := `version: 2
workspace:
  name: test
  policy: go-workspace
policies:
  go-workspace: {}
services: {}
`
	writeDiscoveryFile(t, manifestPath, source)

	_, err := DiscoverWorkspace(manifestPath, workspace, false)
	if err == nil || !strings.Contains(err.Error(), "requires exactly one compatible") {
		t.Fatalf("missing Spring policy error = %v", err)
	}
	data, readErr := os.ReadFile(manifestPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != source {
		t.Fatalf("failed discovery changed manifest:\n%s", data)
	}
}

func TestDiscoverWorkspaceFailsAtomicallyForUnprotectedSpringCustomConsulRegistration(t *testing.T) {
	workspace := t.TempDir()
	writeSpringBootServiceRepository(t, workspace, "data-mart-service")
	writeDiscoveryFile(t, filepath.Join(workspace, "data-mart-service", "src", "main", "java", "ConsulConfig.java"), `
@Configuration
public class ConsulConfig {
    public void register() {
        client.agentServiceRegister(newService);
    }
}
`)
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	source := `version: 2
workspace:
  name: test
services: {}
`
	writeDiscoveryFile(t, manifestPath, source)

	_, err := DiscoverWorkspace(manifestPath, workspace, false)
	if err == nil {
		t.Fatal("unprotected Spring custom registration was accepted")
	}
	for _, expected := range []string{"custom Consul registration", "src/main/java/ConsulConfig.java", "@ConditionalOnProperty(", "--service.registration.enabled=false"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("custom registration error is missing %q: %v", expected, err)
		}
	}
	data, readErr := os.ReadFile(manifestPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != source {
		t.Fatalf("failed custom registration discovery changed manifest:\n%s", data)
	}
}

func TestDiscoveredPolicyRejectsMultipleCompatibleSpringPolicies(t *testing.T) {
	policy := model.Policy{
		Drivers: model.PolicyDrivers{Framework: "spring-boot", ConfigSource: "repository", Discovery: "consul", Materializer: "yaml-overlay"},
		Routing: model.PolicyRouting{Servers: map[string]model.ServerRoute{"rpc": {Port: "rpc"}}},
	}
	manifest := &model.Manifest{Policies: map[string]model.Policy{"spring-a": policy, "spring-b": policy}}
	_, _, err := certifyDiscoveredService(manifest, DiscoveredService{Name: "data-mart-service", Framework: "spring-boot", Runtime: "spring-boot", DiscoveryDriver: "consul", Kind: "rpc", Kinds: []string{"rpc"}}, "")
	if err == nil || !strings.Contains(err.Error(), "candidates: spring-a, spring-b") {
		t.Fatalf("multiple Spring policies error = %v", err)
	}
}

func TestBackfillSynchronizesKafkaConsumerIsolationEvidence(t *testing.T) {
	existing := model.Service{Discovery: model.ServiceDiscovery{Analyzer: "go-subdirectory-module"}}
	discovered := DiscoveredService{Analyzer: "go-subdirectory-module", Consumers: []RepositoryConsumerEvidence{{Driver: "kafka", Protected: true}}}
	updated, changed := backfillDiscoveredDescription(existing, discovered, 3)
	if !changed || !reflect.DeepEqual(updated.Discovery.Consumers, []string{"kafka"}) {
		t.Fatalf("added consumer evidence = %#v, changed=%t", updated, changed)
	}
	if isolation := updated.Isolation.Consumers["kafka"]; isolation.Mode != KafkaConsumerGuardMode || isolation.Env != KafkaConsumersEnabledEnv {
		t.Fatalf("added Kafka isolation = %#v", updated.Isolation)
	}

	removed, changed := backfillDiscoveredDescription(updated, DiscoveredService{Analyzer: "go-subdirectory-module"}, 3)
	if !changed || len(removed.Discovery.Consumers) != 0 || len(removed.Isolation.Consumers) != 0 {
		t.Fatalf("removed consumer evidence = %#v, changed=%t", removed, changed)
	}
}

func writeSpringBootServiceRepository(t *testing.T, workspace string, name string) {
	t.Helper()
	repository := filepath.Join(workspace, name)
	mustMkdirAll(t, filepath.Join(repository, ".git"))
	writeDiscoveryFile(t, filepath.Join(repository, "gradlew"), "#!/bin/sh\n")
	writeDiscoveryFile(t, filepath.Join(repository, "settings.gradle"), "rootProject.name = 'datamart'\n")
	writeDiscoveryFile(t, filepath.Join(repository, "build.gradle"), "plugins { id 'org.springframework.boot' version '2.6.5' }\nversion = '0.0.1-SNAPSHOT'\ndependencies { implementation 'net.devh:grpc-server-spring-boot-starter:2.13.1.RELEASE'; implementation 'org.springframework.cloud:spring-cloud-starter-consul-discovery' }\n")
	writeDiscoveryFile(t, filepath.Join(repository, "src", "main", "resources", "application.yml"), "spring:\n  application:\n    name: datamart.rpc\n")
	writeDiscoveryFile(t, filepath.Join(repository, "src", "main", "java", "Application.java"), "@SpringBootApplication public class Application { public static void main(String[] args) { SpringApplication.run(Application.class, args); } }\n")
	writeDiscoveryFile(t, filepath.Join(repository, "src", "main", "java", "GrpcServer.java"), "@GrpcService public class GrpcServer {}\n")
}

func writeGoServiceRepository(t *testing.T, workspace string, name string, gitFile bool, module string, packageName string) {
	t.Helper()
	repository := filepath.Join(workspace, name)
	mustMkdirAll(t, repository)
	if gitFile {
		writeDiscoveryFile(t, filepath.Join(repository, ".git"), "gitdir: /tmp/example\n")
	} else {
		mustMkdirAll(t, filepath.Join(repository, ".git"))
	}
	writeDiscoveryFile(t, filepath.Join(repository, "go", "go.mod"), "module "+module+"\n")
	mainSource := "package "+packageName+"\n"
	if packageName == "main" {
		serverType := "rest.RestConf"
		if strings.Contains(name, "customer") {
			serverType = "zrpc.RpcServerConf"
		}
		mainSource += "type Config struct { Server "+serverType+" }\n"
		mainSource += "var _ = Getenv(\"HOST\")\nvar _ = Getenv(\"PORT\")\nvar _ = Getenv(\"HTTP_PORT\")\nvar _ = Getenv(\"RPC_PORT\")\n"
	}
	writeDiscoveryFile(t, filepath.Join(repository, "go", "main.go"), mainSource)
}

func writeDiscoveryFile(t *testing.T, path string, contents string) {
	t.Helper()
	mustMkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
	if filepath.Base(path) == "go.mod" {
		if err := os.WriteFile(filepath.Join(filepath.Dir(path), "go.sum"), []byte(""), 0600); err != nil {
			t.Fatalf("write go.sum: %v", err)
		}
	}
}
