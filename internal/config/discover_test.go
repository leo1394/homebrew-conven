package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
	if service.Kind != "http" {
		t.Fatalf("service kind = %q", service.Kind)
	}
	if service.Discovery.Analyzer != "go-subdirectory-module" || !reflect.DeepEqual(service.Discovery.Bindings, []string{"partnerRpc"}) {
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
version: 1
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

	manual := strings.Replace(string(data), "kind: http", "kind: rpc", 1)
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
	if manifest.Services["custom-api"].Kind != "rpc" || manifest.Services["custom-api"].Discovery.Analyzer != "manual" {
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
	writeDiscoveryFile(t, manifestPath, `version: 1
workspace:
  name: test
services:
  consumer:
    path: nested/consumer
    runner:
      run: [consumer]
    dependencies:
      old-service:
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
	if err == nil || !strings.Contains(err.Error(), "references an unknown service") {
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
	writeDiscoveryFile(t, manifestPath, `version: 1
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
	writeDiscoveryFile(t, manifestPath, `version: 1
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
	writeDiscoveryFile(t, manifestPath, `version: 1
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
	writeDiscoveryFile(t, target, `version: 1
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
	writeDiscoveryFile(t, manifestPath, `version: 1
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
	writeDiscoveryFile(t, manifestPath, `version: 1
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
	writeDiscoveryFile(t, filepath.Join(repository, "go", "main.go"), "package "+packageName+"\n")
}

func writeDiscoveryFile(t *testing.T, path string, contents string) {
	t.Helper()
	mustMkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}
