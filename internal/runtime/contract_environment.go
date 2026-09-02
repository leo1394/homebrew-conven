package runtime

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/config"
	"github.com/leo1394/homebrew-conven/internal/materialize"
	"github.com/leo1394/homebrew-conven/internal/model"
)

type environmentRuntimeContract struct{}

func init() {
	RegisterRuntimeContractAdapter(environmentRuntimeContract{})
}

func (environmentRuntimeContract) Name() string {
	return "standard-environment"
}

func (environmentRuntimeContract) Matches(policy model.Policy, kind string) bool {
	runtimeName := policyRuntime(policy)
	if runtimeName == "" || runtimeName == "generic-runner" || runtimeName == "go-zero" || runtimeName == "spring-boot" || runtimeName == "asgi-uvicorn" || runtimeName == "wsgi-gunicorn" || runtimeName == "quarkus" || runtimeName == "micronaut" {
		return false
	}
	if policy.Drivers.ConfigSource != "environment" || policy.Drivers.Materializer != "environment" || !environmentDiscoverySupported(policy.Drivers.Discovery) {
		return false
	}
	_, found := policy.Routing.Servers[kind]
	return found && (kind == "http" || kind == "rpc")
}

func (environmentRuntimeContract) MatchesPlanned(planned *PlannedConfig) bool {
	return planned != nil && planned.Contract == "standard-environment" && planned.Plan.Driver == materialize.DriverEnvironment
}

func (environmentRuntimeContract) AllowGuardCreation() bool {
	return false
}

func (environmentRuntimeContract) RuntimeConfigGuards(_ materialize.Plan, _ config.ExpandContext) ([]materialize.Guard, error) {
	return nil, nil
}

func (environmentRuntimeContract) ServerArguments(arguments []string, _ string, _ PlannedIsolation) []string {
	return append([]string(nil), arguments...)
}

func (environmentRuntimeContract) ProtectedServerEnvironment(environment map[string]string, _ string, planned *PlannedConfig) (map[string]string, error) {
	protected := map[string]string{
		planned.Isolation.ListenerEnv: planned.Isolation.ListenerGuard.Value.(string),
		planned.Isolation.PortEnv: strconv.Itoa(planned.Isolation.ListenerPort),
	}
	if planned.Isolation.RegistrationMode == "config" {
		protected[planned.Isolation.RegistrationEnv] = "false"
	}
	for key, value := range protected {
		if existing, found := environment[key]; found && existing != value {
			return nil, fmt.Errorf("protected environment %s=%q conflicts with required value %q", key, existing, value)
		}
		environment[key] = value
	}
	return environment, nil
}

func (environmentRuntimeContract) ValidateIsolation(name string, kind string, planned *PlannedConfig) error {
	if kind != "http" && kind != "rpc" {
		return fmt.Errorf("service %s kind %s is not supported by the standard environment contract", name, kind)
	}
	isolation := planned.Isolation
	if isolation.ListenerEnv == "" || isolation.PortEnv == "" || isolation.ListenerPort < 1 {
		return fmt.Errorf("service %s kind %s has an incomplete HOST/PORT environment listener contract", name, kind)
	}
	listener, ok := isolation.ListenerGuard.Value.(string)
	if !ok || net.ParseIP(listener) == nil {
		return fmt.Errorf("service %s kind %s environment listener host must be an IP", name, kind)
	}
	if err := validateServiceListener(listener, 0, isolationListenerMode(isolation)); err != nil {
		return fmt.Errorf("service %s kind %s environment listener isolation: %w", name, kind, err)
	}
	if isolation.RegistrationMode == "config" && isolation.RegistrationEnv == "" {
		return fmt.Errorf("service %s kind %s registry contract requires SERVICE_REGISTRATION_ENABLED", name, kind)
	}
	return nil
}

func (environmentRuntimeContract) ValidateRuntimeConfig(name string, planned *PlannedConfig, _ []string, environment map[string]string) error {
	want := map[string]string{
		planned.Isolation.ListenerEnv: planned.Isolation.ListenerGuard.Value.(string),
		planned.Isolation.PortEnv: strconv.Itoa(planned.Isolation.ListenerPort),
	}
	if planned.Isolation.RegistrationMode == "config" {
		want[planned.Isolation.RegistrationEnv] = "false"
	}
	for key, value := range want {
		if environment[key] != value {
			return fmt.Errorf("service %s environment contract requires %s=%q", name, key, value)
		}
	}
	planned.Isolation.RuntimeConfigRef = "environment(" + strings.Join(sortedEnvironmentKeys(want), ",") + ")"
	return nil
}

func (environmentRuntimeContract) EffectiveListener(planned *PlannedConfig) string {
	return net.JoinHostPort(planned.Isolation.ListenerGuard.Value.(string), strconv.Itoa(planned.Isolation.ListenerPort))
}

func (environmentRuntimeContract) ExternalDependencies(_ PlannedService, _ string) ([]ExternalConsulDependency, bool, error) {
	return nil, false, nil
}

func environmentDiscoverySupported(driver string) bool {
	switch driver {
	case "passive", "kubernetes-dns", "consul", "nacos", "eureka", "etcd":
		return true
	}
	return false
}

func sortedEnvironmentKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	for left := 0; left < len(keys); left++ {
		for right := left + 1; right < len(keys); right++ {
			if keys[right] < keys[left] {
				keys[left], keys[right] = keys[right], keys[left]
			}
		}
	}
	return keys
}
