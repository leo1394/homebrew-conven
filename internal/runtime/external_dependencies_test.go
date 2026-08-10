package runtime

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leo1394/homebrew-conven/internal/materialize"
	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestDetectExternalConsulDependenciesUsesFinalClientConfig(t *testing.T) {
	service := externalDependencyTestService(t, `name: rpc-service
discovType: ""
consul:
  host: consul-server.default
  port: 8500
  key: rpc-service.rpc
localRpc:
  target: 127.0.0.1:18081
deviceRpc:
  discovType: consul
  consul:
    host: consul-server.default
    port: 8500
    key: device.rpc
disabledRpc:
  discovType: ""
nonActiveRpc:
  discovType: " consul "
  consul:
    host: consul-server.default
    port: 8500
    key: ignored.rpc
`, nil)
	dependencies, err := detectExternalConsulDependencies(service, "rpc")
	if err != nil {
		t.Fatal(err)
	}
	if len(dependencies) != 1 {
		t.Fatalf("dependencies = %#v", dependencies)
	}
	dependency := dependencies[0]
	if dependency.Owner != "api" || dependency.Path != "deviceRpc" || dependency.Host != "consul-server.default" || dependency.Port != 8500 || dependency.Key != "device.rpc" {
		t.Fatalf("dependency = %#v", dependency)
	}
	if dependency.Reference() != "api.deviceRpc" {
		t.Fatalf("reference = %q", dependency.Reference())
	}
}

func TestDetectExternalConsulDependenciesOnlyRunsForSupportedPolicy(t *testing.T) {
	for _, config := range []*PlannedConfig{
		nil,
		{Framework: "spring", Discovery: "consul", Plan: materialize.Plan{Driver: materialize.DriverYAMLOverlay}},
		{Framework: "go-zero", Discovery: "etcd", Plan: materialize.Plan{Driver: materialize.DriverYAMLOverlay}},
		{Framework: "go-zero", Discovery: "consul", Plan: materialize.Plan{Driver: "other"}},
	} {
		dependencies, err := detectExternalConsulDependencies(PlannedService{Name: "api", Config: config}, "http")
		if err != nil {
			t.Fatal(err)
		}
		if len(dependencies) != 0 {
			t.Fatalf("unsupported config dependencies = %#v", dependencies)
		}
	}
}

func TestDetectExternalConsulDependenciesRejectsMalformedActiveClient(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "missing consul mapping",
			yaml: "deviceRpc:\n  discovType: consul\n",
			want: "requires a consul mapping",
		},
		{
			name: "missing host",
			yaml: "deviceRpc:\n  discovType: consul\n  consul:\n    port: 8500\n    key: device.rpc\n",
			want: "requires consul.host",
		},
		{
			name: "missing port",
			yaml: "deviceRpc:\n  discovType: consul\n  consul:\n    host: consul\n    key: device.rpc\n",
			want: "invalid consul.port",
		},
		{
			name: "out of range port",
			yaml: "deviceRpc:\n  discovType: consul\n  consul:\n    host: consul\n    port: 70000\n    key: device.rpc\n",
			want: "between 1 and 65535",
		},
		{
			name: "missing key",
			yaml: "deviceRpc:\n  discovType: consul\n  consul:\n    host: consul\n    port: 8500\n",
			want: "requires consul.key",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := externalDependencyTestService(t, test.yaml, nil)
			_, err := detectExternalConsulDependencies(service, "http")
			if err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), "deviceRpc") {
				t.Fatalf("error = %v, want %q with client path", err, test.want)
			}
		})
	}
}

func TestDetectExternalConsulDependenciesRejectsLocalRouteStillUsingConsul(t *testing.T) {
	service := externalDependencyTestService(t, `partnerRpc:
  target: 127.0.0.1:18081
  discovType: consul
  consul:
    host: consul-server.default
    port: 8500
    key: partner.rpc
`, []PlannedRoute{{Dependency: "partner", Binding: "partnerRpc", Local: true, Mode: "replace"}})
	_, err := detectExternalConsulDependencies(service, "http")
	if err == nil || !strings.Contains(err.Error(), "local route partnerRpc still uses Consul") {
		t.Fatalf("error = %v", err)
	}
}

func TestDetectExternalConsulDependenciesRejectsRootRegistration(t *testing.T) {
	service := externalDependencyTestService(t, `discovType: consul
consul:
  host: consul-server.default
  port: 8500
  key: root-client.rpc
`, nil)
	_, err := detectExternalConsulDependencies(service, "http")
	if err == nil || !strings.Contains(err.Error(), "remote Consul registration enabled") {
		t.Fatalf("root registration error = %v", err)
	}
}

func TestRuntimePreflightDoesNotClaimIsolationBeforeFinalConfigInspection(t *testing.T) {
	service := externalDependencyTestService(t, `discovType: consul
consul:
  host: consul-server.default
  port: 8500
  key: root-client.rpc
`, nil)
	service.Kind = "http"
	plan := &Plan{
		Order:      []string{"api"},
		Services:   map[string]PlannedService{"api": service},
		Connection: ConnectionConfig{Driver: "none"},
	}
	var output strings.Builder
	err := runRuntimePreflight(context.Background(), plan, &output, true)
	if err == nil || !strings.Contains(err.Error(), "remote Consul registration enabled") {
		t.Fatalf("preflight error = %v", err)
	}
	if strings.Contains(output.String(), "✓ Local isolation") || strings.Contains(output.String(), "inbound routing contract") {
		t.Fatalf("preflight claimed isolation before inspection completed:\n%s", output.String())
	}
	if !strings.Contains(output.String(), "Final config isolation and dependency inspection failed") {
		t.Fatalf("preflight output did not report inspection failure:\n%s", output.String())
	}
}

func TestDetectExternalConsulDependenciesRejectsYAMLMergeKeys(t *testing.T) {
	service := externalDependencyTestService(t, `defaults: &server
  discovType: consul
  consul:
    host: consul-server.default
    port: 8500
    key: api.http
<<: *server
host: 127.0.0.1
`, nil)
	_, err := detectExternalConsulDependencies(service, "http")
	if err == nil || !strings.Contains(err.Error(), "unsupported YAML merge key") {
		t.Fatalf("YAML merge key error = %v", err)
	}
}

func TestDetectExternalConsulDependenciesRejectsAmbiguousYAML(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "custom scalar tag",
			yaml: "deviceRpc:\n  discovType: !unsafe consul\n",
			want: "unsupported YAML tag",
		},
		{
			name: "binary scalar tag",
			yaml: "deviceRpc:\n  discovType: !!binary Y29uc3Vs\n",
			want: "unsupported YAML tag",
		},
		{
			name: "binary mapping key",
			yaml: "deviceRpc:\n  ? !!binary ZGlzY292VHlwZQ==\n  : consul\n",
			want: "unsupported YAML mapping key",
		},
		{
			name: "duplicate mapping key",
			yaml: "deviceRpc:\n  discovType: \"\"\n  discovType: consul\n",
			want: "duplicate YAML key",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := externalDependencyTestService(t, test.yaml, nil)
			_, err := detectExternalConsulDependencies(service, "http")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestPreflightExternalConsulDependenciesDeduplicatesPassingQuery(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.URL.Path != "/v1/health/service/device.rpc" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if request.URL.Query().Get("passing") != "true" {
			t.Errorf("passing = %q", request.URL.Query().Get("passing"))
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `[{"Service":{"ID":"device.rpc-1"}}]`)
	}))
	defer server.Close()
	host, port := externalDependencyTestServerAddress(t, server)
	dependencies := []ExternalConsulDependency{
		{Owner: "api-a", Path: "deviceRpc", Host: host, Port: port, Key: "device.rpc"},
		{Owner: "api-b", Path: "deviceRpc", Host: host, Port: port, Key: "device.rpc"},
	}
	if err := preflightExternalConsulDependencies(context.Background(), dependencies); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
}

func TestPreflightExternalConsulDependenciesReportsMissingOwnerPathAndKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `[]`)
	}))
	defer server.Close()
	host, port := externalDependencyTestServerAddress(t, server)
	err := preflightExternalConsulDependencies(context.Background(), []ExternalConsulDependency{{
		Owner: "portal-api-service",
		Path:  "deviceRpc",
		Host:  host,
		Port:  port,
		Key:   "device.rpc",
	}})
	if err == nil {
		t.Fatal("missing dependency preflight succeeded")
	}
	for _, expected := range []string{"portal-api-service.deviceRpc -> device.rpc", "has no passing instances"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("error = %q, missing %q", err, expected)
		}
	}
}

func TestPreflightExternalConsulDependenciesRejectsInvalidResponses(t *testing.T) {
	tests := []struct {
		name string
		handler http.HandlerFunc
		want string
	}{
		{
			name: "http status",
			handler: func(writer http.ResponseWriter, request *http.Request) {
				writer.WriteHeader(http.StatusServiceUnavailable)
			},
			want: "HTTP 503 Service Unavailable",
		},
		{
			name: "invalid json",
			handler: func(writer http.ResponseWriter, request *http.Request) {
				fmt.Fprint(writer, `{not-json}`)
			},
			want: "invalid JSON",
		},
		{
			name: "response limit",
			handler: func(writer http.ResponseWriter, request *http.Request) {
				fmt.Fprint(writer, strings.Repeat("x", externalDependencyResponseLimit+1))
			},
			want: "response exceeds",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			defer server.Close()
			host, port := externalDependencyTestServerAddress(t, server)
			err := preflightExternalConsulDependencies(context.Background(), []ExternalConsulDependency{{Owner: "api", Path: "rpc", Host: host, Port: port, Key: "service.rpc"}})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestPreflightExternalConsulDependenciesHonorsContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer server.Close()
	host, port := externalDependencyTestServerAddress(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := preflightExternalConsulDependencies(ctx, []ExternalConsulDependency{{Owner: "api", Path: "rpc", Host: host, Port: port, Key: "service.rpc"}})
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("error = %v", err)
	}
}

func TestStartExternalConsulPreflightFailsBeforeStartingService(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `[]`)
	}))
	defer server.Close()
	host, port := externalDependencyTestServerAddress(t, server)
	workspaceRoot := t.TempDir()
	serviceDirectory := filepath.Join(workspaceRoot, "api")
	resources := filepath.Join(serviceDirectory, "resources")
	if err := os.MkdirAll(resources, 0700); err != nil {
		t.Fatal(err)
	}
	application := fmt.Sprintf("host: 0.0.0.0\nport: 8080\ndeviceRpc:\n  discovType: consul\n  consul:\n    host: %s\n    port: %d\n    key: device.rpc\n", host, port)
	if err := os.WriteFile(filepath.Join(resources, "application.yaml"), []byte(application), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resources, "config-dev.yaml"), []byte("appId: test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(serviceDirectory, "start-test-service")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\n: > \"$START_MARKER\"\nwhile :; do sleep 1; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(serviceDirectory, "started.marker")
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{
		Version:   1,
		Workspace: model.Workspace{Name: "start-preflight", Policy: "retail"},
		Policies: map[string]model.Policy{
			"retail": {
				Drivers: model.PolicyDrivers{Framework: "go-zero", ConfigSource: "repository", Discovery: "consul", Materializer: "yaml-overlay"},
				Config:  model.PolicyConfig{SourceDir: "resources", Application: "application.yaml", Bootstrap: "config-dev.yaml", RuntimeBootstrap: "config-local.yaml"},
				Process: model.PolicyProcess{Env: map[string]string{"PROFILE_ACTIVE": "local"}, Args: []string{"-f", "${configDir}"}},
				Routing: model.PolicyRouting{Servers: map[string]model.ServerRoute{
					"http": {
						Port: "http",
						Isolation: model.ServerIsolation{
							Registration: model.RegistrationGuard{Mode: "not-applicable"},
							Listener:     model.ListenerGuard{Path: "host", Value: "127.0.0.1"},
						},
					},
				}},
			},
		},
		Services: map[string]model.Service{
			"api": {
				Path:   "api",
				Kind:   "http",
				Ports:  map[string]int{"http": 18080},
				Env:    map[string]string{"START_MARKER": marker},
				Runner: model.Runner{Run: []string{launcher}},
			},
		},
	})
	var output strings.Builder
	_, err := Start(context.Background(), workspace, StartOptions{Services: []string{"api"}, Output: &output})
	if err == nil || !strings.Contains(err.Error(), "api.deviceRpc -> device.rpc") {
		t.Fatalf("start preflight error = %v\n%s", err, output.String())
	}
	for _, expected := range []string{"External Consul dependency preflight", "deviceRpc -> device.rpc", "preflight failed"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("start preflight output is missing %q:\n%s", expected, output.String())
		}
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("service command ran before failed preflight: marker error = %v", statErr)
	}
	if strings.Contains(output.String(), "Starting api") {
		t.Fatalf("start stage was reached after failed preflight:\n%s", output.String())
	}
}

func TestStartRejectsPrepareRedirectedRuntimeBootstrapBeforeStartingService(t *testing.T) {
	workspaceRoot := t.TempDir()
	serviceDirectory := filepath.Join(workspaceRoot, "api")
	resources := filepath.Join(serviceDirectory, "resources")
	if err := os.MkdirAll(resources, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resources, "application.yaml"), []byte("host: 0.0.0.0\nport: 8080\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resources, "config-dev.yaml"), []byte("appId: api\ncluster: dev\n"), 0600); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(serviceDirectory, "start-test-service")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\n: > \"$START_MARKER\"\nwhile :; do sleep 1; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(serviceDirectory, "started.marker")
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{
		Version:   1,
		Workspace: model.Workspace{Name: "bootstrap-preflight", Policy: "retail"},
		Policies: map[string]model.Policy{
			"retail": {
				Drivers: model.PolicyDrivers{Framework: "go-zero", ConfigSource: "repository", Discovery: "consul", Materializer: "yaml-overlay"},
				Config: model.PolicyConfig{
					SourceDir:        "resources",
					Application:      "application.yaml",
					Bootstrap:        "config-dev.yaml",
					RuntimeBootstrap: "config-local.yaml",
				},
				Process: model.PolicyProcess{
					Env:  map[string]string{"PROFILE_ACTIVE": "local"},
					Args: []string{"-f", "${configDir}"},
				},
				Routing: model.PolicyRouting{Servers: map[string]model.ServerRoute{
					"http": {
						Port: "http",
						Isolation: model.ServerIsolation{
							Registration: model.RegistrationGuard{Mode: "not-applicable"},
							Listener:     model.ListenerGuard{Path: "host", Value: "127.0.0.1"},
						},
					},
				}},
			},
		},
		Services: map[string]model.Service{
			"api": {
				Path:  "api",
				Kind:  "http",
				Ports: map[string]int{"http": 18080},
				Env:   map[string]string{"START_MARKER": marker},
				Runner: model.Runner{
					Prepare: []string{"sh", "-c", `printf 'localConfigEnable: false\nlocalConfigPath: ../resources/application.yaml\n' > "$CONVEN_CONFIG_DIR/config-local.yaml"`},
					Run:     []string{launcher},
				},
			},
		},
	})
	var output strings.Builder
	_, err := Start(context.Background(), workspace, StartOptions{Services: []string{"api"}, Output: &output})
	if err == nil || !strings.Contains(err.Error(), "config-local.yaml") || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("bootstrap guard error = %v\n%s", err, output.String())
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("service command ran after bootstrap redirect: marker error = %v", statErr)
	}
	if strings.Contains(output.String(), "Starting api") {
		t.Fatalf("start stage was reached after bootstrap redirect:\n%s", output.String())
	}
	if !strings.Contains(output.String(), "Final runtime isolation recheck failed") {
		t.Fatalf("bootstrap redirect did not report the final recheck failure:\n%s", output.String())
	}
}

func TestRestartExternalConsulPreflightFailsBeforeStoppingOldProcess(t *testing.T) {
	var passing atomic.Bool
	passing.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if passing.Load() {
			fmt.Fprint(writer, `[{"Service":{"ID":"device.rpc-1"}}]`)
			return
		}
		fmt.Fprint(writer, `[]`)
	}))
	defer server.Close()
	host, port := externalDependencyTestServerAddress(t, server)
	workspaceRoot := t.TempDir()
	resources := filepath.Join(workspaceRoot, "api", "resources")
	if err := os.MkdirAll(resources, 0700); err != nil {
		t.Fatal(err)
	}
	application := fmt.Sprintf("host: 0.0.0.0\nport: 8080\ndeviceRpc:\n  discovType: consul\n  consul:\n    host: %s\n    port: %d\n    key: device.rpc\n", host, port)
	if err := os.WriteFile(filepath.Join(resources, "application.yaml"), []byte(application), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resources, "config-dev.yaml"), []byte("appId: test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(workspaceRoot, "api", "source.txt")
	if err := os.WriteFile(sourcePath, []byte("one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(workspaceRoot, "api", "start-test-service")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nwhile :; do sleep 1; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{
		Version:   1,
		Workspace: model.Workspace{Name: "restart-preflight", Policy: "retail"},
		Policies: map[string]model.Policy{
			"retail": {
				Drivers: model.PolicyDrivers{Framework: "go-zero", ConfigSource: "repository", Discovery: "consul", Materializer: "yaml-overlay"},
				Config:  model.PolicyConfig{SourceDir: "resources", Application: "application.yaml", Bootstrap: "config-dev.yaml", RuntimeBootstrap: "config-local.yaml"},
				Process: model.PolicyProcess{Env: map[string]string{"PROFILE_ACTIVE": "local"}, Args: []string{"-f", "${configDir}"}},
				Routing: model.PolicyRouting{Servers: map[string]model.ServerRoute{
					"http": {
						Port:    "http",
						Patches: []model.ConfigPatch{{Path: "port", Value: "${port.http}"}},
						Isolation: model.ServerIsolation{
							Registration: model.RegistrationGuard{Mode: "not-applicable"},
							Listener:     model.ListenerGuard{Path: "host", Value: "127.0.0.1"},
						},
					},
				}},
			},
		},
		Services: map[string]model.Service{
			"api": {
				Path:   "api",
				Kind:   "http",
				Ports:  map[string]int{"http": 18080},
				Runner: model.Runner{Run: []string{launcher}},
			},
		},
	})
	var output strings.Builder
	session, err := Start(context.Background(), workspace, StartOptions{Services: []string{"api"}, Output: &output})
	if err != nil {
		t.Fatalf("start failed: %v\n%s", err, output.String())
	}
	defer Stop(context.Background(), workspace, nil, true, false, &output)
	before := sessionProcess(session, "api")
	passing.Store(false)
	if err := os.WriteFile(sourcePath, []byte("two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = Restart(context.Background(), workspace, RestartOptions{Output: &output})
	if err == nil || !strings.Contains(err.Error(), "api.deviceRpc -> device.rpc") {
		t.Fatalf("restart preflight error = %v\n%s", err, output.String())
	}
	stored, loadErr := workspace.Store.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	after := sessionProcess(stored, "api")
	if after.PID != before.PID || !ProcessAlive(before.PID) {
		t.Fatalf("old process changed before failed preflight: before=%d after=%d alive=%v", before.PID, after.PID, ProcessAlive(before.PID))
	}
}

func externalDependencyTestService(t *testing.T, application string, routes []PlannedRoute) PlannedService {
	t.Helper()
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "application.yaml"), []byte(application), 0600); err != nil {
		t.Fatal(err)
	}
	return PlannedService{
		Name: "api",
		Config: &PlannedConfig{
			Framework: "go-zero",
			Discovery: "consul",
			Plan: materialize.Plan{
				Driver:      materialize.DriverYAMLOverlay,
				TargetDir:   target,
				Application: "application.yaml",
			},
			Routes: routes,
		},
	}
}

func externalDependencyTestServerAddress(t *testing.T, server *httptest.Server) (string, int) {
	t.Helper()
	host, portText, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}
