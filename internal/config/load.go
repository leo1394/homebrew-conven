package config

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/leo1394/homebrew-conven/internal/model"
	"gopkg.in/yaml.v3"
)

func Load(path string) (*model.Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("open Conven manifest %q: %w", path, err)
	}
	return decodeManifest(data, path)
}

func decodeManifest(data []byte, path string) (*model.Manifest, error) {
	manifest := &model.Manifest{}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(manifest); err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("Conven manifest %q is empty", path)
		}
		return nil, fmt.Errorf("decode Conven manifest %q: %w", path, err)
	}

	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("decode Conven manifest %q: %w", path, err)
		}
		return nil, fmt.Errorf("Conven manifest %q contains multiple YAML documents", path)
	}

	if err := validateManifest(manifest); err != nil {
		return nil, fmt.Errorf("validate Conven manifest %q: %w", path, err)
	}
	return manifest, nil
}

func ServiceNames(manifest *model.Manifest) []string {
	if manifest == nil {
		return nil
	}

	names := make([]string, 0, len(manifest.Services))
	for name := range manifest.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func ValidateSelection(manifest *model.Manifest, selected []string) error {
	if manifest == nil {
		return fmt.Errorf("manifest is nil")
	}

	seen := make(map[string]struct{}, len(selected))
	duplicates := make([]string, 0)
	unknown := make([]string, 0)
	for _, name := range selected {
		if _, ok := seen[name]; ok {
			duplicates = append(duplicates, name)
			continue
		}
		seen[name] = struct{}{}
		if _, ok := manifest.Services[name]; !ok {
			unknown = append(unknown, name)
		}
	}

	if len(duplicates) == 0 && len(unknown) == 0 {
		return nil
	}

	sort.Strings(duplicates)
	sort.Strings(unknown)
	problems := make([]string, 0, 2)
	if len(duplicates) > 0 {
		problems = append(problems, fmt.Sprintf("duplicate services: %s", strings.Join(duplicates, ", ")))
	}
	if len(unknown) > 0 {
		problems = append(problems, fmt.Sprintf("unknown services: %s (available: %s)", strings.Join(unknown, ", "), strings.Join(ServiceNames(manifest), ", ")))
	}
	return fmt.Errorf("invalid service selection: %s", strings.Join(problems, "; "))
}

func validateManifest(manifest *model.Manifest) error {
	switch manifest.Version {
	case 1, 2:
	default:
		return fmt.Errorf("version must be 1 or 2, got %d", manifest.Version)
	}
	if strings.TrimSpace(manifest.Workspace.Name) == "" {
		return fmt.Errorf("workspace.name is required")
	}
	if manifest.Version == 1 && len(manifest.Services) == 0 {
		return fmt.Errorf("services must contain at least one service")
	}

	for _, environmentName := range sortedEnvironmentNames(manifest) {
		if invalidName(environmentName) {
			return fmt.Errorf("environment name %q must be non-empty and contain no whitespace", environmentName)
		}
		if manifest.Version == 2 {
			environment := manifest.Environments[environmentName]
			if environment.EnvFile != "" {
				if err := validateWorkspaceRelativePath(environment.EnvFile, fmt.Sprintf("environments.%s.envFile", environmentName)); err != nil {
					return err
				}
			}
			endpointNames := make([]string, 0, len(environment.Endpoints))
			for name := range environment.Endpoints {
				endpointNames = append(endpointNames, name)
			}
			sort.Strings(endpointNames)
			for _, name := range endpointNames {
				endpoint := environment.Endpoints[name]
				if !validServiceName(name) {
					return fmt.Errorf("environments.%s.endpoints contains invalid endpoint name %q", environmentName, name)
				}
				if strings.TrimSpace(endpoint.Address) == "" {
					return fmt.Errorf("environments.%s.endpoints.%s.address is required", environmentName, name)
				}
				if _, _, err := net.SplitHostPort(endpoint.Address); err != nil {
					return fmt.Errorf("environments.%s.endpoints.%s.address must use host:port", environmentName, name)
				}
				if endpoint.Protocol != "" && endpoint.Protocol != "tcp" && endpoint.Protocol != "http" && endpoint.Protocol != "rpc" {
					return fmt.Errorf("environments.%s.endpoints.%s.protocol must be tcp, http, or rpc", environmentName, name)
				}
				if err := validateHealth(endpoint.Readiness, fmt.Sprintf("environments.%s.endpoints.%s.readiness", environmentName, name)); err != nil {
					return err
				}
			}
		}
	}
	for _, name := range sortedPolicyNames(manifest) {
		if !validServiceName(name) {
			return fmt.Errorf("policy name %q must start with a letter or digit and contain only letters, digits, '.', '_' or '-'", name)
		}
		if err := validatePolicy(name, manifest.Policies[name]); err != nil {
			return err
		}
	}
	if manifest.Workspace.Policy != "" {
		if _, found := manifest.Policies[manifest.Workspace.Policy]; !found {
			return fmt.Errorf("workspace.policy references unknown policy %q", manifest.Workspace.Policy)
		}
	}
	for _, name := range ServiceNames(manifest) {
		if !validServiceName(name) {
			return fmt.Errorf("service name %q must start with a letter or digit and contain only letters, digits, '.', '_' or '-'", name)
		}
		service := manifest.Services[name]
		if strings.TrimSpace(service.Path) == "" {
			return fmt.Errorf("services.%s.path is required", name)
		}
		policyName := service.Policy
		if policyName == "" {
			policyName = manifest.Workspace.Policy
		}
		if policyName != "" {
			if _, found := manifest.Policies[policyName]; !found {
				return fmt.Errorf("services.%s.policy references unknown policy %q", name, policyName)
			}
		}
		if service.Kind != "" && invalidName(service.Kind) {
			return fmt.Errorf("services.%s.kind must contain no whitespace", name)
		}
		switch service.Network.Listen {
		case "", model.NetworkListenLoopback, model.NetworkListenAllInterfaces:
		default:
			return fmt.Errorf("services.%s.network.listen must be loopback or all-interfaces, got %q", name, service.Network.Listen)
		}
		if service.Network.Listen != "" && service.Kind == "" {
			return fmt.Errorf("services.%s.network.listen requires a typed service kind", name)
		}
		if service.Discovery.Analyzer == "" && len(service.Discovery.Bindings) > 0 {
			return fmt.Errorf("services.%s.discovery.analyzer is required when bindings are declared", name)
		}
		if service.Discovery.Analyzer != "" && invalidName(service.Discovery.Analyzer) {
			return fmt.Errorf("services.%s.discovery.analyzer must contain no whitespace", name)
		}
		seenBindings := make(map[string]bool, len(service.Discovery.Bindings))
		for _, binding := range service.Discovery.Bindings {
			if invalidName(binding) {
				return fmt.Errorf("services.%s.discovery.bindings contains invalid binding %q", name, binding)
			}
			if seenBindings[binding] {
				return fmt.Errorf("services.%s.discovery.bindings contains duplicate binding %q", name, binding)
			}
			seenBindings[binding] = true
		}
		for index, patch := range service.Config.Patches {
			if err := validateConfigPatch(patch, fmt.Sprintf("services.%s.config.patches[%d]", name, index)); err != nil {
				return err
			}
		}
		if err := validateCommand(service.Runner.Prepare, fmt.Sprintf("services.%s.runner.prepare", name), false); err != nil {
			return err
		}
		if err := validateCommand(service.Runner.Build, fmt.Sprintf("services.%s.runner.build", name), false); err != nil {
			return err
		}
		if err := validateCommand(service.Runner.Run, fmt.Sprintf("services.%s.runner.run", name), true); err != nil {
			return err
		}

		portNames := make([]string, 0, len(service.Ports))
		for portName := range service.Ports {
			portNames = append(portNames, portName)
		}
		sort.Strings(portNames)
		for _, portName := range portNames {
			if invalidName(portName) {
				return fmt.Errorf("services.%s.ports contains invalid port name %q", name, portName)
			}
			port := service.Ports[portName]
			if port < 1 || port > 65535 {
				return fmt.Errorf("services.%s.ports.%s must be between 1 and 65535, got %d", name, portName, port)
			}
		}

		dependencies := make([]string, 0, len(service.Dependencies))
		for dependency := range service.Dependencies {
			dependencies = append(dependencies, dependency)
		}
		sort.Strings(dependencies)
		for _, dependencyName := range dependencies {
			declaration := service.Dependencies[dependencyName]
			localServiceName := dependencyName
			if manifest.Version == 2 {
				localServiceName = declaration.LocalService
				if localServiceName == "" {
					if _, found := manifest.Services[dependencyName]; found {
						localServiceName = dependencyName
					}
				}
			}
			if localServiceName == name {
				return fmt.Errorf("services.%s.dependencies must not reference itself", name)
			}
			dependencyService, hasLocalService := manifest.Services[localServiceName]
			if !hasLocalService {
				if manifest.Version == 1 {
					return fmt.Errorf("services.%s.dependencies.%s references an unknown service", name, dependencyName)
				}
				if localServiceName != "" {
					return fmt.Errorf("services.%s.dependencies.%s.localService references unknown service %q", name, dependencyName, localServiceName)
				}
			}
			if (declaration.Binding == "") != (declaration.Port == "") {
				return fmt.Errorf("services.%s.dependencies.%s.binding and port must be declared together", name, dependencyName)
			}
			if declaration.Binding != "" {
				if invalidName(declaration.Binding) {
					return fmt.Errorf("services.%s.dependencies.%s.binding must contain no whitespace", name, dependencyName)
				}
				if invalidName(declaration.Port) {
					return fmt.Errorf("services.%s.dependencies.%s.port must contain no whitespace", name, dependencyName)
				}
				if hasLocalService {
					if _, found := dependencyService.Ports[declaration.Port]; !found {
						return fmt.Errorf("services.%s.dependencies.%s.port references unknown port %q", name, dependencyName, declaration.Port)
					}
				}
			}
		}
		if policyName != "" && service.Kind != "" {
			policy := manifest.Policies[policyName]
			if server, found := policy.Routing.Servers[service.Kind]; found {
				if _, found := service.Ports[server.Port]; !found {
					return fmt.Errorf("services.%s kind %q requires policy %q server port %q", name, service.Kind, policyName, server.Port)
				}
				if service.Kind == "rpc" && server.Isolation.Registration.Mode != "config" {
					return fmt.Errorf("services.%s kind %q requires policy %q registration isolation mode config", name, service.Kind, policyName)
				}
			} else {
				return fmt.Errorf("services.%s kind %q requires policy %q to declare a matching server route", name, service.Kind, policyName)
			}
		}
	}
	if manifest.Version == 2 {
		if err := validateEnvironmentResolutions(manifest); err != nil {
			return err
		}
		if err := validateLocalPorts(manifest); err != nil {
			return err
		}
	}

	return nil
}

func sortedPolicyNames(manifest *model.Manifest) []string {
	names := make([]string, 0, len(manifest.Policies))
	for name := range manifest.Policies {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func validateWorkspaceRelativePath(value string, field string) error {
	path := strings.TrimSpace(value)
	if path == "" || filepath.IsAbs(path) {
		return fmt.Errorf("%s must be a non-empty workspace-relative path", field)
	}
	clean := filepath.Clean(path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s must stay within the workspace", field)
	}
	return nil
}

func validateHealth(health model.Health, field string) error {
	if health.Type == "" {
		return nil
	}
	switch health.Type {
	case "tcp":
		if strings.TrimSpace(health.Address) == "" {
			return fmt.Errorf("%s.address is required for tcp readiness", field)
		}
	case "http":
		if strings.TrimSpace(health.URL) == "" {
			return fmt.Errorf("%s.url is required for http readiness", field)
		}
	case "command":
		if err := validateCommand(health.Command, field+".command", true); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%s.type must be tcp, http, or command", field)
	}
	if health.Timeout != "" {
		duration, err := time.ParseDuration(health.Timeout)
		if err != nil || duration <= 0 {
			return fmt.Errorf("%s.timeout must be a positive duration", field)
		}
	}
	return nil
}

func validateEnvironmentResolutions(manifest *model.Manifest) error {
	for _, environmentName := range sortedEnvironmentNames(manifest) {
		environment := manifest.Environments[environmentName]
		owners := make([]string, 0, len(environment.Resolutions))
		for owner := range environment.Resolutions {
			owners = append(owners, owner)
		}
		sort.Strings(owners)
		for _, owner := range owners {
			service, found := manifest.Services[owner]
			if !found {
				return fmt.Errorf("environments.%s.resolutions references unknown service %q", environmentName, owner)
			}
			aliases := make([]string, 0, len(environment.Resolutions[owner]))
			for alias := range environment.Resolutions[owner] {
				aliases = append(aliases, alias)
			}
			sort.Strings(aliases)
			for _, alias := range aliases {
				resolution := environment.Resolutions[owner][alias]
				declaration, found := service.Dependencies[alias]
				if !found {
					return fmt.Errorf("environments.%s.resolutions.%s references unknown dependency alias %q", environmentName, owner, alias)
				}
				field := fmt.Sprintf("environments.%s.resolutions.%s.%s", environmentName, owner, alias)
				switch resolution.Mode {
				case "endpoint":
					if _, found := environment.Endpoints[resolution.Target]; !found {
						return fmt.Errorf("%s.target references unknown endpoint %q", field, resolution.Target)
					}
				case "remote", "disabled", "error":
					if resolution.Target != "" {
						return fmt.Errorf("%s.target must be empty for mode %s", field, resolution.Mode)
					}
				default:
					return fmt.Errorf("%s.mode must be endpoint, remote, disabled, or error", field)
				}
				required := declaration.Required == nil || *declaration.Required
				if required && resolution.Mode == "disabled" {
					return fmt.Errorf("%s cannot disable a required dependency", field)
				}
			}
		}
	}
	return nil
}

func validateLocalPorts(manifest *model.Manifest) error {
	for _, environmentName := range sortedEnvironmentNames(manifest) {
		used := make(map[int]string)
		claim := func(port int, owner string) error {
			if previous, found := used[port]; found {
				return fmt.Errorf("environment %s local port %d conflicts between %s and %s", environmentName, port, previous, owner)
			}
			used[port] = owner
			return nil
		}
		for _, serviceName := range ServiceNames(manifest) {
			service := manifest.Services[serviceName]
			portNames := make([]string, 0, len(service.Ports))
			for name := range service.Ports {
				portNames = append(portNames, name)
			}
			sort.Strings(portNames)
			for _, name := range portNames {
				if err := claim(service.Ports[name], "service "+serviceName+"."+name); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validatePolicy(name string, policy model.Policy) error {
	prefix := "policies." + name
	for field, value := range map[string]string{
		"drivers.framework": policy.Drivers.Framework,
		"drivers.discovery": policy.Drivers.Discovery,
	} {
		if value != "" && invalidName(value) {
			return fmt.Errorf("%s.%s must contain no whitespace", prefix, field)
		}
	}
	if policy.Drivers.Materializer != "" && policy.Drivers.Materializer != "yaml-overlay" {
		return fmt.Errorf("%s.drivers.materializer must be yaml-overlay, got %q", prefix, policy.Drivers.Materializer)
	}
	if policy.Drivers.ConfigSource != "" && policy.Drivers.ConfigSource != "repository" && policy.Drivers.ConfigSource != "apollo" {
		return fmt.Errorf("%s.drivers.configSource must be repository or apollo, got %q", prefix, policy.Drivers.ConfigSource)
	}
	if policy.Drivers.Materializer == "yaml-overlay" {
		if policy.Drivers.ConfigSource == "" {
			return fmt.Errorf("%s.drivers.configSource is required for yaml-overlay", prefix)
		}
		if err := validateRelativeConfigPath(policy.Config.SourceDir, prefix+".config.sourceDir", true); err != nil {
			return err
		}
		if err := validateRelativeConfigPath(policy.Config.Application, prefix+".config.application", true); err != nil {
			return err
		}
	}
	if policy.Config.Bootstrap != "" {
		if err := validateRelativeConfigPath(policy.Config.Bootstrap, prefix+".config.bootstrap", false); err != nil {
			return err
		}
	}
	if policy.Config.RuntimeBootstrap != "" {
		if err := validateRelativeConfigPath(policy.Config.RuntimeBootstrap, prefix+".config.runtimeBootstrap", false); err != nil {
			return err
		}
	}
	if policy.Drivers.ConfigSource == "apollo" && (policy.Config.Bootstrap == "" || policy.Config.RuntimeBootstrap == "") {
		return fmt.Errorf("%s.config.bootstrap and runtimeBootstrap are required for the apollo config source", prefix)
	}
	if policy.Config.Apollo.Attempts < 0 {
		return fmt.Errorf("%s.config.apollo.attempts must not be negative", prefix)
	}
	for field, value := range map[string]string{
		"retryDelay": policy.Config.Apollo.RetryDelay,
		"timeout":    policy.Config.Apollo.Timeout,
	} {
		if value == "" {
			continue
		}
		duration, err := time.ParseDuration(value)
		if err != nil || duration < 0 {
			return fmt.Errorf("%s.config.apollo.%s must be a non-negative duration", prefix, field)
		}
	}
	for index, patch := range policy.Config.Patches {
		if err := validateConfigPatch(patch, fmt.Sprintf("%s.config.patches[%d]", prefix, index)); err != nil {
			return err
		}
	}
	for index, argument := range policy.Process.Args {
		if strings.TrimSpace(argument) == "" {
			return fmt.Errorf("%s.process.args[%d] must not be empty", prefix, index)
		}
	}
	serverKinds := make([]string, 0, len(policy.Routing.Servers))
	for kind := range policy.Routing.Servers {
		serverKinds = append(serverKinds, kind)
	}
	sort.Strings(serverKinds)
	for _, kind := range serverKinds {
		if invalidName(kind) {
			return fmt.Errorf("%s.routing.servers contains invalid kind %q", prefix, kind)
		}
		server := policy.Routing.Servers[kind]
		if invalidName(server.Port) {
			return fmt.Errorf("%s.routing.servers.%s.port is required and must contain no whitespace", prefix, kind)
		}
		for index, patch := range server.Patches {
			if err := validateConfigPatch(patch, fmt.Sprintf("%s.routing.servers.%s.patches[%d]", prefix, kind, index)); err != nil {
				return err
			}
		}
		for index, argument := range server.Args {
			if strings.TrimSpace(argument) == "" {
				return fmt.Errorf("%s.routing.servers.%s.args[%d] must not be empty", prefix, kind, index)
			}
		}
		if err := validateServerIsolation(server.Isolation, fmt.Sprintf("%s.routing.servers.%s.isolation", prefix, kind)); err != nil {
			return err
		}
	}
	if err := validateRouteRule(policy.Routing.LocalDependency, prefix+".routing.localDependency"); err != nil {
		return err
	}
	return validateRouteRule(policy.Routing.RemoteDependency, prefix+".routing.remoteDependency")
}

func validateRelativeConfigPath(value string, field string, required bool) error {
	if strings.TrimSpace(value) == "" {
		if required {
			return fmt.Errorf("%s is required", field)
		}
		return nil
	}
	if filepath.IsAbs(value) {
		return fmt.Errorf("%s must be relative", field)
	}
	clean := filepath.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s must stay within its config directory", field)
	}
	return nil
}

func validateConfigPatch(patch model.ConfigPatch, field string) error {
	if patch.File != "" {
		if err := validateRelativeConfigPath(patch.File, field+".file", false); err != nil {
			return err
		}
	}
	if strings.TrimSpace(patch.Path) == "" {
		return fmt.Errorf("%s.path is required", field)
	}
	for _, segment := range strings.Split(patch.Path, ".") {
		if invalidName(segment) {
			return fmt.Errorf("%s.path contains invalid segment %q", field, segment)
		}
	}
	if patch.Mode != "" && patch.Mode != "replace" {
		return fmt.Errorf("%s.mode must be replace, got %q", field, patch.Mode)
	}
	if patch.Value == nil {
		return fmt.Errorf("%s.value is required", field)
	}
	return nil
}

func validateServerIsolation(isolation model.ServerIsolation, field string) error {
	registrationField := field + ".registration"
	switch isolation.Registration.Mode {
	case "config":
		if err := validateConfigGuardLocation(isolation.Registration.File, isolation.Registration.Path, registrationField); err != nil {
			return err
		}
		if isolation.Registration.DisabledValue == nil {
			return fmt.Errorf("%s.disabledValue is required", registrationField)
		}
		if !scalarGuardValue(isolation.Registration.DisabledValue) {
			return fmt.Errorf("%s.disabledValue must be a scalar", registrationField)
		}
	case "not-applicable":
		if isolation.Registration.File != "" || isolation.Registration.Path != "" || isolation.Registration.DisabledValue != nil {
			return fmt.Errorf("%s must not declare file, path, or disabledValue for not-applicable mode", registrationField)
		}
	default:
		return fmt.Errorf("%s.mode must be config or not-applicable, got %q", registrationField, isolation.Registration.Mode)
	}
	listenerField := field + ".listener"
	if err := validateConfigGuardLocation(isolation.Listener.File, isolation.Listener.Path, listenerField); err != nil {
		return err
	}
	if isolation.Listener.Value == nil {
		return fmt.Errorf("%s.value is required", listenerField)
	}
	if _, ok := isolation.Listener.Value.(string); !ok {
		return fmt.Errorf("%s.value must be a string", listenerField)
	}
	return nil
}

func validateConfigGuardLocation(file string, path string, field string) error {
	if file != "" {
		if err := validateRelativeConfigPath(file, field+".file", false); err != nil {
			return err
		}
	}
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%s.path is required", field)
	}
	for _, segment := range strings.Split(path, ".") {
		if invalidName(segment) {
			return fmt.Errorf("%s.path contains invalid segment %q", field, segment)
		}
	}
	return nil
}

func scalarGuardValue(value interface{}) bool {
	switch value.(type) {
	case string, bool, int, int64, uint64, float64:
		return true
	default:
		return false
	}
}

func validateRouteRule(rule model.RouteRule, field string) error {
	if rule.Mode == "" {
		if rule.Value != nil {
			return fmt.Errorf("%s.mode is required when value is declared", field)
		}
		return nil
	}
	if rule.Mode != "preserve" && rule.Mode != "replace" {
		return fmt.Errorf("%s.mode must be preserve or replace, got %q", field, rule.Mode)
	}
	if rule.Mode != "preserve" && rule.Value == nil {
		return fmt.Errorf("%s.value is required for %s mode", field, rule.Mode)
	}
	return nil
}

func sortedEnvironmentNames(manifest *model.Manifest) []string {
	names := make([]string, 0, len(manifest.Environments))
	for name := range manifest.Environments {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func invalidName(value string) bool {
	return strings.TrimSpace(value) == "" || strings.IndexFunc(value, unicode.IsSpace) >= 0
}

func validServiceName(value string) bool {
	if value == "." || value == ".." || value == "" {
		return false
	}
	for index, character := range value {
		letter := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
		digit := character >= '0' && character <= '9'
		if index == 0 && !letter && !digit {
			return false
		}
		if !letter && !digit && character != '.' && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

func validateCommand(command []string, field string, required bool) error {
	if len(command) == 0 {
		if required {
			return fmt.Errorf("%s is required", field)
		}
		return nil
	}
	for index, argument := range command {
		if strings.TrimSpace(argument) == "" {
			if index == 0 {
				return fmt.Errorf("%s executable is required", field)
			}
			return fmt.Errorf("%s argument %d must not be empty", field, index)
		}
	}
	return nil
}
