package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestDiscoverWorkspaceCompletesSpringHTTPRoute(t *testing.T) {
	workspace := t.TempDir()
	writeSpringBootServiceRepository(t, workspace, "smartview-api-service")
	writeDiscoveryFile(t, filepath.Join(workspace, "smartview-api-service", "build.gradle"), "plugins { id 'org.springframework.boot' version '2.5.6' }\nversion = '0.0.1-SNAPSHOT'\ndependencies { implementation 'org.springframework.boot:spring-boot-starter-web'; implementation 'org.springframework.cloud:spring-cloud-starter-consul-discovery' }\n")
	writeDiscoveryFile(t, filepath.Join(workspace, "smartview-api-service", "src", "main", "java", "ReportController.java"), "@RestController public class ReportController {}\n")
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, `version: 2
workspace:
  name: test
policies:
  spring-boot-consul:
    drivers:
      framework: spring-boot
      configSource: repository
      discovery: consul
      materializer: yaml-overlay
    config:
      sourceDir: src/main/resources
      application: application.yml
    routing:
      servers:
        rpc: # Preserve the existing RPC route.
          port: rpc
          args: ["--grpc.server.port=${port.rpc}"]
          isolation:
            registration:
              mode: config
              path: service.registration.enabled
              disabledValue: false
            listener:
              path: grpc.server.address
              value: 127.0.0.1
services: {}
`)
	before, err := Load(manifestPath)
	if err != nil { t.Fatal(err) }
	result, err := DiscoverWorkspace(manifestPath, workspace, false)
	if err != nil { t.Fatal(err) }
	if !reflect.DeepEqual(result.Added, []string{"smartview-api-service"}) {
		t.Fatalf("added = %#v", result.Added)
	}
	manifest, err := Load(manifestPath)
	if err != nil { t.Fatal(err) }
	service := manifest.Services["smartview-api-service"]
	if service.Policy != "spring-boot-consul" || service.Kind != "http" || service.Ports["http"] != 18080 {
		t.Fatalf("HTTP service = %#v", service)
	}
	policy := manifest.Policies[service.Policy]
	if !reflect.DeepEqual(policy.Routing.Servers["rpc"], before.Policies[service.Policy].Routing.Servers["rpc"]) {
		t.Fatal("existing RPC route was changed")
	}
	http := policy.Routing.Servers["http"]
	if http.Port != "http" || http.Isolation.Listener.Path != "server.address" || http.Isolation.Listener.Value != "127.0.0.1" || http.Isolation.Registration.Path != "service.registration.enabled" || http.Isolation.Registration.DisabledValue != false {
		t.Fatalf("HTTP route = %#v", http)
	}
	source, err := os.ReadFile(manifestPath)
	if err != nil { t.Fatal(err) }
	if _, err := DiscoverWorkspace(manifestPath, workspace, false); err != nil { t.Fatal(err) }
	after, err := os.ReadFile(manifestPath)
	if err != nil { t.Fatal(err) }
	if string(source) != string(after) { t.Fatal("second discovery changed the manifest") }
}

func TestDiscoveredSpringHTTPPolicyCompletionPreservesExplicitRoutes(t *testing.T) {
	policy := springBootCertifierPolicy("http")
	policy.Routing.Servers["http"] = model.ServerRoute{Port: "custom", Args: []string{"custom"}}
	manifest := &model.Manifest{Policies: map[string]model.Policy{"spring": policy}}
	_, _, err := certifyDiscoveredService(manifest, discoveredSpringHTTP(), "spring")
	if err != nil { t.Fatal(err) }
	if !reflect.DeepEqual(manifest.Policies["spring"], policy) { t.Fatal("explicit HTTP route was overwritten") }
}

func TestDiscoveredSpringHTTPPolicyCompletionRejectsAmbiguity(t *testing.T) {
	policy := springBootCertifierPolicy("rpc")
	manifest := &model.Manifest{Policies: map[string]model.Policy{"spring-a": policy, "spring-b": policy}}
	if _, _, err := certifyDiscoveredService(manifest, discoveredSpringHTTP(), ""); err == nil { t.Fatal("ambiguous policies were accepted") }
	for _, policy := range manifest.Policies {
		if _, exists := policy.Routing.Servers["http"]; exists { t.Fatal("failed certification changed a policy") }
	}
	if name, _, err := certifyDiscoveredService(manifest, discoveredSpringHTTP(), "spring-a"); err != nil || name != "spring-a" {
		t.Fatalf("explicit policy = %q, error = %v", name, err)
	}
	if _, exists := manifest.Policies["spring-b"].Routing.Servers["http"]; exists { t.Fatal("unselected policy was changed") }
}

func TestDiscoveredSpringHTTPPolicyCompletionKeepsRegistrationChecks(t *testing.T) {
	policy := springBootCertifierPolicy("rpc")
	manifest := &model.Manifest{Policies: map[string]model.Policy{"spring": policy}}
	service := discoveredSpringHTTP()
	service.Registrations = []RepositoryRegistrationEvidence{{Provider: "consul", File: "ConsulConfig.java"}}
	if _, _, err := certifyDiscoveredService(manifest, service, ""); err == nil { t.Fatal("unguarded registration was accepted") }
	if !reflect.DeepEqual(manifest.Policies["spring"], policy) { t.Fatal("failed certification changed a policy") }
}

func discoveredSpringHTTP() DiscoveredService {
	return DiscoveredService{Name: "smartview-api-service", Framework: "spring-boot", Runtime: "spring-boot", DiscoveryDriver: "consul", Kind: "http", Kinds: []string{"http"}}
}
