package runtime

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/materialize"
	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestGoZeroRuntimeContractRejectsUnparsedConfigFlagBeforeStartup(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "api")
	workdir := filepath.Join(directory, "go")
	if err := os.MkdirAll(workdir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "go.mod"), []byte("module example.com/api\n"), 0600); err != nil {
		t.Fatal(err)
	}
	mainSource := `package main
import "flag"
var configDir = flag.String("f", "../resources", "the config file")
func main() { start(*configDir) }
func start(string) {}
`
	if err := os.WriteFile(filepath.Join(workdir, "main.go"), []byte(mainSource), 0600); err != nil {
		t.Fatal(err)
	}
	err := validateRuntimeContractSource("api", &PlannedConfig{Contract: "go-zero-consul-yaml-overlay"}, directory, workdir)
	if err == nil || !strings.Contains(err.Error(), "flag.Parse()") {
		t.Fatalf("unparsed runtime config source error = %v", err)
	}
}

func TestRuntimeContractAdaptersResolveByDriverContract(t *testing.T) {
	tests := []struct {
		name   string
		policy model.Policy
		want   string
	}{
		{
			name:   "go-zero",
			policy: goZeroRuntimeContractPolicy(),
			want:   "go-zero-consul-yaml-overlay",
		},
		{
			name:   "spring-boot",
			policy: springBootRuntimeContractPolicy(),
			want:   "spring-boot-repository-overlay",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter, found, err := runtimeContractForPolicy(test.policy, "rpc")
			if err != nil || !found {
				t.Fatalf("runtime contract found = %t, error = %v", found, err)
			}
			if adapter.Name() != test.want {
				t.Fatalf("runtime contract = %q, want %q", adapter.Name(), test.want)
			}
		})
	}
}

func TestRuntimeContractRejectsUnsupportedDriverContract(t *testing.T) {
	adapter, found, err := runtimeContractForPolicy(model.Policy{Drivers: model.PolicyDrivers{
		Framework:    "python",
		ConfigSource: "repository",
		Discovery:    "consul",
		Materializer: "yaml-overlay",
	}}, "http")
	if err != nil || found || adapter != nil {
		t.Fatalf("unsupported runtime contract = %#v, found = %t, error = %v", adapter, found, err)
	}
}

func TestSpringRuntimeContractSupportsPassiveRegistration(t *testing.T) {
	policy := springBootRuntimeContractPolicy()
	policy.Drivers.Discovery = "passive"
	server := policy.Routing.Servers["rpc"]
	server.Isolation.Registration = model.RegistrationGuard{Mode: "not-applicable"}
	policy.Routing.Servers["rpc"] = server
	adapter, found, err := runtimeContractForPolicy(policy, "rpc")
	if err != nil || !found || adapter.Name() != "spring-boot-repository-overlay" {
		t.Fatalf("passive Spring contract = %#v, found=%t, error=%v", adapter, found, err)
	}
}

func TestRuntimeContractTrustDoesNotDispatchOnFrameworkMetadata(t *testing.T) {
	policy := goZeroRuntimeContractPolicy()
	policy.Drivers.Runtime = "go-zero"
	policy.Drivers.Framework = "unrecognized-display-name"
	adapter, found, err := runtimeContractForPolicy(policy, "rpc")
	if err != nil || !found {
		t.Fatalf("capability contract found = %t, error = %v", found, err)
	}
	if adapter.Name() != "go-zero-consul-yaml-overlay" {
		t.Fatalf("runtime contract = %q", adapter.Name())
	}
}

func TestRuntimeContractOwnsFrameworkSpecificServerArguments(t *testing.T) {
	isolation := PlannedIsolation{RegistrationMode: "config", ListenerMode: model.NetworkListenAllInterfaces}
	spring := &PlannedConfig{
		Contract:  "spring-boot-repository-overlay",
		Framework: "spring-boot",
		Discovery: "consul",
		Plan: materialize.Plan{
			SourceDriver: materialize.SourceRepository,
			Driver:       materialize.DriverYAMLOverlay,
		},
		Isolation: isolation,
	}
	arguments := []string{"--grpc.server.address=127.0.0.1", "--grpc.server.port=18087"}
	got, err := runtimeContractServerArguments("data-mart-service", spring, "rpc", arguments)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--grpc.server.address=0.0.0.0", "--grpc.server.port=18087"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Spring server arguments = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(arguments, []string{"--grpc.server.address=127.0.0.1", "--grpc.server.port=18087"}) {
		t.Fatalf("source server arguments were mutated: %#v", arguments)
	}

	goZero := &PlannedConfig{Contract: "go-zero-consul-yaml-overlay", Isolation: isolation}
	got, err = runtimeContractServerArguments("partner-service", goZero, "rpc", []string{"-f", "/tmp/config"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"-f", "/tmp/config"}) {
		t.Fatalf("go-zero server arguments = %#v", got)
	}
}

func TestRuntimeContractFailsClosedForUnknownRecordedImplementation(t *testing.T) {
	_, err := requireRuntimeContract("customer-service", &PlannedConfig{Contract: "missing-contract"})
	if err == nil || !strings.Contains(err.Error(), `runtime contract adapter "missing-contract" is not registered`) {
		t.Fatalf("missing recorded contract error = %v", err)
	}
}

func goZeroRuntimeContractPolicy() model.Policy {
	return model.Policy{
		Drivers: model.PolicyDrivers{
			Framework:    "go-zero",
			ConfigSource: "apollo",
			Discovery:    "consul",
			Materializer: "yaml-overlay",
		},
		Config: model.PolicyConfig{Application: "application.yaml"},
		Routing: model.PolicyRouting{Servers: map[string]model.ServerRoute{
			"rpc": {
				Port: "rpc",
				Isolation: model.ServerIsolation{
					Registration: model.RegistrationGuard{Mode: "config", Path: "discovType", DisabledValue: ""},
					Listener:     model.ListenerGuard{Path: "listenOn", Value: "127.0.0.1:${port.rpc}"},
				},
			},
		}},
	}
}

func springBootRuntimeContractPolicy() model.Policy {
	return model.Policy{
		Drivers: model.PolicyDrivers{
			Framework:    "spring-boot",
			ConfigSource: "repository",
			Discovery:    "consul",
			Materializer: "yaml-overlay",
		},
		Config: model.PolicyConfig{Application: "application.yml"},
		Routing: model.PolicyRouting{Servers: map[string]model.ServerRoute{
			"rpc": {
				Port: "rpc",
				Isolation: model.ServerIsolation{
					Registration: model.RegistrationGuard{Mode: "config", Path: "service.registration.enabled", DisabledValue: false},
					Listener:     model.ListenerGuard{Path: "grpc.server.address", Value: "127.0.0.1"},
				},
			},
		}},
	}
}
