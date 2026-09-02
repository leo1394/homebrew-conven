package runtime

import (
	"fmt"

	"github.com/leo1394/homebrew-conven/internal/model"
)

type javaNativeEnvironmentRuntimeContract struct {
	environmentRuntimeContract
}

func init() {
	RegisterRuntimeContractAdapter(javaNativeEnvironmentRuntimeContract{})
}

func (javaNativeEnvironmentRuntimeContract) Name() string {
	return "java-native-environment"
}

func (javaNativeEnvironmentRuntimeContract) Matches(policy model.Policy, kind string) bool {
	runtimeName := policyRuntime(policy)
	if runtimeName != "quarkus" && runtimeName != "micronaut" {
		return false
	}
	if policy.Drivers.ConfigSource != "environment" || policy.Drivers.Materializer != "environment" || !environmentDiscoverySupported(policy.Drivers.Discovery) {
		return false
	}
	_, found := policy.Routing.Servers[kind]
	return found && (kind == "http" || kind == "rpc")
}

func (javaNativeEnvironmentRuntimeContract) MatchesPlanned(planned *PlannedConfig) bool {
	return planned != nil && planned.Contract == "java-native-environment"
}

func (javaNativeEnvironmentRuntimeContract) ValidateIsolation(name string, kind string, planned *PlannedConfig) error {
	base := environmentRuntimeContract{}
	if err := base.ValidateIsolation(name, kind, planned); err != nil {
		return err
	}
	hostKey, portKey := javaNativeListenerKeys(planned.Runtime, kind)
	if hostKey == "" || planned.Isolation.ListenerEnv != hostKey || planned.Isolation.PortEnv != portKey {
		return fmt.Errorf("service %s %s %s listener contract requires environment keys %s and %s", name, planned.Runtime, kind, hostKey, portKey)
	}
	return nil
}

func javaNativeListenerKeys(runtimeName string, kind string) (string, string) {
	if runtimeName == "quarkus" {
		if kind == "rpc" {
			return "QUARKUS_GRPC_SERVER_HOST", "QUARKUS_GRPC_SERVER_PORT"
		}
		return "QUARKUS_HTTP_HOST", "QUARKUS_HTTP_PORT"
	}
	if runtimeName == "micronaut" {
		if kind == "rpc" {
			return "GRPC_SERVER_HOST", "GRPC_SERVER_PORT"
		}
		return "MICRONAUT_SERVER_HOST", "MICRONAUT_SERVER_PORT"
	}
	return "", ""
}
