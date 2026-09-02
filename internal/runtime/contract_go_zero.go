package runtime

import (
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/config"
	"github.com/leo1394/homebrew-conven/internal/materialize"
	"github.com/leo1394/homebrew-conven/internal/model"
)

type goZeroConsulRuntimeContract struct{}

func init() {
	RegisterRuntimeContractAdapter(goZeroConsulRuntimeContract{})
}

func (goZeroConsulRuntimeContract) Name() string {
	return "go-zero-consul-yaml-overlay"
}

func (goZeroConsulRuntimeContract) Matches(policy model.Policy, kind string) bool {
	if policyRuntime(policy) != "go-zero" || policy.Drivers.Discovery != "consul" || materialize.Driver(policy.Drivers.Materializer) != materialize.DriverYAMLOverlay {
		return false
	}
	server, found := policy.Routing.Servers[kind]
	if !found {
		return false
	}
	if kind == "rpc" {
		return server.Isolation.Registration.Mode == "config" && server.Isolation.Registration.Path == "discovType" && server.Isolation.Listener.Path == "listenOn"
	}
	return kind == "http" && server.Isolation.Registration.Mode == "not-applicable" && server.Isolation.Listener.Path == "host"
}

func (goZeroConsulRuntimeContract) MatchesPlanned(planned *PlannedConfig) bool {
	runtimeName := planned.Runtime
	if runtimeName == "" { runtimeName = planned.Framework }
	return runtimeName == "go-zero" && planned.Discovery == "consul" && planned.Plan.Driver == materialize.DriverYAMLOverlay && planned.Plan.Application == "application.yaml"
}

func (goZeroConsulRuntimeContract) AllowGuardCreation() bool {
	return false
}

func (goZeroConsulRuntimeContract) RuntimeConfigGuards(plan materialize.Plan, context config.ExpandContext) ([]materialize.Guard, error) {
	return planRuntimeConfigGuards(plan, context)
}

func (goZeroConsulRuntimeContract) ServerArguments(arguments []string, _ string, _ PlannedIsolation) []string {
	return append([]string(nil), arguments...)
}

func (goZeroConsulRuntimeContract) ValidateIsolation(name string, kind string, config *PlannedConfig) error {
	isolation := config.Isolation
	if isolation.RegistrationMode == "" {
		if kind != "" {
			return fmt.Errorf("service %s kind %s has no local isolation contract", name, kind)
		}
		return nil
	}
	if kind == "rpc" && isolation.RegistrationMode != "config" {
		return fmt.Errorf("service %s kind rpc must verify disabled registration through its final runtime config", name)
	}
	if kind == "http" && isolation.RegistrationMode != "not-applicable" {
		return fmt.Errorf("service %s kind http registration isolation must be not-applicable for the trusted go-zero Consul adapter", name)
	}
	if kind != "rpc" && kind != "http" {
		return fmt.Errorf("service %s kind %s has no trusted go-zero Consul local isolation contract", name, kind)
	}
	if isolation.RegistrationMode == "config" && isolation.RegistrationGuard == nil {
		return fmt.Errorf("service %s registration isolation guard is missing", name)
	}
	if isolation.RegistrationMode == "config" {
		guard := *isolation.RegistrationGuard
		disabledValue, ok := guard.Value.(string)
		if guard.File != config.Plan.Application || guard.Path != "discovType" || !ok || disabledValue != "" {
			return fmt.Errorf("service %s go-zero Consul registration isolation must enforce %s:discovType to the empty string", name, config.Plan.Application)
		}
	}
	listener, ok := isolation.ListenerGuard.Value.(string)
	if !ok {
		return fmt.Errorf("service %s listener isolation value must be a string", name)
	}
	wantListenerPath := "host"
	if kind == "rpc" {
		wantListenerPath = "listenOn"
	}
	if isolation.ListenerGuard.File != config.Plan.Application || isolation.ListenerGuard.Path != wantListenerPath {
		return fmt.Errorf("service %s kind %s listener isolation must enforce %s:%s", name, kind, config.Plan.Application, wantListenerPath)
	}
	if err := validateServiceListener(listener, isolation.ListenerPort, isolationListenerMode(isolation)); err != nil {
		return fmt.Errorf("service %s listener isolation: %w", name, err)
	}
	if kind == "rpc" {
		if _, _, err := net.SplitHostPort(listener); err != nil {
			return fmt.Errorf("service %s kind rpc listener isolation must include its declared port", name)
		}
	} else if net.ParseIP(listener) == nil {
		return fmt.Errorf("service %s kind http listener isolation must be an IP without a port", name)
	}
	return nil
}

func (goZeroConsulRuntimeContract) ValidateRuntimeConfig(name string, config *PlannedConfig, run []string, environment map[string]string) error {
	target := filepath.Clean(config.Plan.TargetDir)
	accepted := false
	if config.Isolation.RuntimeConfigDir && environment["PROFILE_ACTIVE"] == "local" {
		accepted = true
	}
	config.Isolation.RuntimeConfigRef = ""
	found := 0
	for index := 1; index < len(run); index++ {
		argument := run[index]
		candidate := ""
		if argument == "-f" {
			if index+1 >= len(run) {
				return fmt.Errorf("service %s run command has a go-zero -f flag without a value", name)
			}
			candidate = run[index+1]
			index++
		} else if strings.HasPrefix(argument, "-f=") {
			candidate = strings.TrimPrefix(argument, "-f=")
		} else {
			return fmt.Errorf("service %s trusted go-zero adapter run command supports only its executable and one -f runtime config flag, got %q", name, argument)
		}
		found++
		if found > 1 {
			return fmt.Errorf("service %s trusted go-zero adapter run command must contain exactly one -f runtime config flag", name)
		}
		if accepted && filepath.IsAbs(candidate) && filepath.Clean(candidate) == target {
			config.Isolation.RuntimeConfigRef = "guarded-bootstrap(" + config.Plan.RuntimeBootstrap + "->" + config.Plan.Application + ")"
			continue
		}
		return fmt.Errorf("service %s run command has an unverified go-zero -f path %q", name, candidate)
	}
	if found == 1 {
		return nil
	}
	return fmt.Errorf("service %s run command does not pass its verified runtime config directory through the go-zero -f flag", name)
}

func (goZeroConsulRuntimeContract) EffectiveListener(config *PlannedConfig) string {
	listener := config.Isolation.ListenerGuard.Value.(string)
	if config.Isolation.ListenerGuard.Path == "host" && config.Isolation.ListenerPort > 0 {
		return net.JoinHostPort(listener, strconv.Itoa(config.Isolation.ListenerPort))
	}
	return listener
}

func (goZeroConsulRuntimeContract) ExternalDependencies(service PlannedService, kind string) ([]ExternalConsulDependency, bool, error) {
	dependencies, err := inspectExternalConsulDependencies(service, kind)
	return dependencies, true, err
}
