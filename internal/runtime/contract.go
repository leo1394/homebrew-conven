package runtime

import (
	"fmt"
	"sort"
	"sync"

	"github.com/leo1394/homebrew-conven/internal/config"
	"github.com/leo1394/homebrew-conven/internal/materialize"
	"github.com/leo1394/homebrew-conven/internal/model"
)

type RuntimeContractAdapter interface {
	ConfigDeliveryAdapter
	ListenerAdapter
	RegistrationAdapter
	Name() string
	Matches(model.Policy, string) bool
	MatchesPlanned(*PlannedConfig) bool
}

type ConfigDeliveryAdapter interface {
	AllowGuardCreation() bool
	RuntimeConfigGuards(materialize.Plan, config.ExpandContext) ([]materialize.Guard, error)
	ValidateRuntimeConfig(string, *PlannedConfig, []string, map[string]string) error
}

type ListenerAdapter interface {
	ServerArguments([]string, string, PlannedIsolation) []string
	ValidateIsolation(string, string, *PlannedConfig) error
	EffectiveListener(*PlannedConfig) string
}

type RegistrationAdapter interface {
	ExternalDependencies(PlannedService, string) ([]ExternalConsulDependency, bool, error)
}

type RoutingAdapter interface {
	Rule(model.PolicyRouting, bool) model.RouteRule
}

type HealthAdapter interface {
	Checks(model.Service) []model.ServiceHealthCheck
}

type RuntimeContractCompiler struct {
	ConfigDelivery ConfigDeliveryAdapter
	Listener       ListenerAdapter
	Registration   RegistrationAdapter
	Routing        RoutingAdapter
	Health         HealthAdapter
}

func runtimeContractCompiler(adapter RuntimeContractAdapter) RuntimeContractCompiler {
	return RuntimeContractCompiler{
		ConfigDelivery: adapter,
		Listener:       adapter,
		Registration:   adapter,
		Routing:        policyRoutingAdapter{},
		Health:         declaredHealthAdapter{},
	}
}

type policyRoutingAdapter struct{}

func (policyRoutingAdapter) Rule(routing model.PolicyRouting, local bool) model.RouteRule {
	if local {
		return routing.LocalDependency
	}
	return routing.RemoteDependency
}

type declaredHealthAdapter struct{}

func (declaredHealthAdapter) Checks(service model.Service) []model.ServiceHealthCheck {
	return service.EffectiveHealthChecks()
}

type protectedArgumentCompiler interface {
	ProtectedServerArguments([]string, string, *PlannedConfig) []string
}

type protectedEnvironmentCompiler interface {
	ProtectedServerEnvironment(map[string]string, string, *PlannedConfig) (map[string]string, error)
}

type runtimeSourceValidator interface {
	ValidateSource(string, string, string) error
}

type disabledBindingCompiler interface {
	DisabledBindingPatches(string, []string) []materialize.Patch
}

var runtimeContractRegistry = struct {
	sync.RWMutex
	adapters map[string]RuntimeContractAdapter
}{adapters: make(map[string]RuntimeContractAdapter)}

func RegisterRuntimeContractAdapter(adapter RuntimeContractAdapter) {
	if adapter == nil || adapter.Name() == "" {
		panic("runtime contract adapter must have a name")
	}
	runtimeContractRegistry.Lock()
	defer runtimeContractRegistry.Unlock()
	if _, found := runtimeContractRegistry.adapters[adapter.Name()]; found {
		panic("duplicate runtime contract adapter " + adapter.Name())
	}
	runtimeContractRegistry.adapters[adapter.Name()] = adapter
}

func builtinRuntimeContractAdapters() []RuntimeContractAdapter {
	runtimeContractRegistry.RLock()
	defer runtimeContractRegistry.RUnlock()
	names := make([]string, 0, len(runtimeContractRegistry.adapters))
	for name := range runtimeContractRegistry.adapters {
		names = append(names, name)
	}
	sort.Strings(names)
	adapters := make([]RuntimeContractAdapter, 0, len(names))
	for _, name := range names {
		adapters = append(adapters, runtimeContractRegistry.adapters[name])
	}
	return adapters
}

func resolveRuntimeContractAdapter(policy model.Policy, kind string) (RuntimeContractAdapter, bool, error) {
	matches := make([]RuntimeContractAdapter, 0, 1)
	for _, adapter := range builtinRuntimeContractAdapters() {
		if adapter.Matches(policy, kind) {
			matches = append(matches, adapter)
		}
	}
	if len(matches) == 0 {
		return nil, false, nil
	}
	if len(matches) > 1 {
		return nil, false, fmt.Errorf("runtime contract matches multiple adapters: %s and %s", matches[0].Name(), matches[1].Name())
	}
	return matches[0], true, nil
}

func runtimeContractForPolicy(policy model.Policy, kind string) (RuntimeContractAdapter, bool, error) {
	return resolveRuntimeContractAdapter(policy, kind)
}

func runtimeContractForConfig(planned *PlannedConfig) (RuntimeContractAdapter, bool, error) {
	if planned == nil {
		return nil, false, nil
	}
	if planned.Contract != "" {
		for _, adapter := range builtinRuntimeContractAdapters() {
			if adapter.Name() == planned.Contract {
				return adapter, true, nil
			}
		}
		return nil, false, fmt.Errorf("runtime contract adapter %q is not registered", planned.Contract)
	}
	matches := make([]RuntimeContractAdapter, 0, 1)
	for _, adapter := range builtinRuntimeContractAdapters() {
		if adapter.MatchesPlanned(planned) {
			matches = append(matches, adapter)
		}
	}
	if len(matches) == 0 {
		return nil, false, nil
	}
	if len(matches) > 1 {
		return nil, false, fmt.Errorf("planned runtime contract matches multiple adapters: %s and %s", matches[0].Name(), matches[1].Name())
	}
	return matches[0], true, nil
}

func requireRuntimeContract(name string, planned *PlannedConfig) (RuntimeContractAdapter, error) {
	adapter, found, err := runtimeContractForConfig(planned)
	if err != nil {
		return nil, fmt.Errorf("service %s: %w", name, err)
	}
	if found {
		return adapter, nil
	}
	return nil, fmt.Errorf("service %s has no certified runtime contract adapter", name)
}

func runtimeContractServerArguments(name string, planned *PlannedConfig, kind string, arguments []string) ([]string, error) {
	if planned == nil || planned.Isolation.RegistrationMode == "" {
		return append([]string(nil), arguments...), nil
	}
	adapter, err := requireRuntimeContract(name, planned)
	if err != nil {
		return nil, err
	}
	return runtimeContractCompiler(adapter).Listener.ServerArguments(arguments, kind, planned.Isolation), nil
}

func runtimeContractProtectedArguments(name string, planned *PlannedConfig, kind string, arguments []string) ([]string, error) {
	if planned == nil || planned.Isolation.RegistrationMode == "" {
		return arguments, nil
	}
	adapter, err := requireRuntimeContract(name, planned)
	if err != nil {
		return nil, err
	}
	implementation, ok := adapter.(protectedArgumentCompiler)
	if !ok {
		return arguments, nil
	}
	return implementation.ProtectedServerArguments(arguments, kind, planned), nil
}

func runtimeContractServerEnvironment(name string, planned *PlannedConfig, kind string, environment map[string]string) (map[string]string, error) {
	if planned == nil || planned.Isolation.RegistrationMode == "" {
		return environment, nil
	}
	adapter, err := requireRuntimeContract(name, planned)
	if err != nil {
		return nil, err
	}
	implementation, ok := adapter.(protectedEnvironmentCompiler)
	if !ok {
		return environment, nil
	}
	return implementation.ProtectedServerEnvironment(environment, kind, planned)
}

func validateRuntimeContractSource(name string, planned *PlannedConfig, directory string, workdir string) error {
	if planned == nil {
		return nil
	}
	adapter, found, err := runtimeContractForConfig(planned)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	implementation, ok := adapter.(runtimeSourceValidator)
	if !ok {
		return nil
	}
	return implementation.ValidateSource(name, directory, workdir)
}

func runtimeContractDisabledBindingPatches(planned *PlannedConfig, application string, bindings []string) ([]materialize.Patch, error) {
	if planned == nil || len(bindings) == 0 {
		return nil, nil
	}
	adapter, found, err := runtimeContractForConfig(planned)
	if err != nil || !found {
		return nil, err
	}
	implementation, ok := adapter.(disabledBindingCompiler)
	if !ok {
		return nil, nil
	}
	return implementation.DisabledBindingPatches(application, bindings), nil
}

func inspectRuntimeContractExternalDependencies(service PlannedService, kind string) ([]ExternalConsulDependency, bool, error) {
	if service.Config == nil {
		return nil, false, nil
	}
	adapter, found, err := runtimeContractForConfig(service.Config)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}
	return runtimeContractCompiler(adapter).Registration.ExternalDependencies(service, kind)
}
