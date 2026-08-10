package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/config"
	"github.com/leo1394/homebrew-conven/internal/materialize"
	"github.com/leo1394/homebrew-conven/internal/model"
)

type PlannedConfig struct {
	Policy    string
	Framework string
	Discovery string
	Plan      materialize.Plan
	Routes    []PlannedRoute
	Isolation PlannedIsolation
}

type PlannedIsolation struct {
	RegistrationMode  string
	RegistrationGuard *materialize.Guard
	ListenerGuard     materialize.Guard
	ListenerPort      int
	RuntimeConfigDir  bool
	RuntimeConfigRef  string
}

type PlannedRoute struct {
	Dependency string
	Binding    string
	Local      bool
	Mode       string
}

func policyForService(manifest *model.Manifest, service model.Service) (string, model.Policy, bool) {
	name := service.Policy
	if name == "" {
		name = manifest.Workspace.Policy
	}
	if name == "" {
		return "", model.Policy{}, false
	}
	policy, found := manifest.Policies[name]
	return name, policy, found
}

func planServiceConfig(plan *Plan, name string, service model.Service, directory string, context config.ExpandContext, selected map[string]bool) (*PlannedConfig, error) {
	policyName, policy, found := policyForService(plan.Workspace.Manifest, service)
	if !found || policy.Drivers.Materializer == "" {
		return nil, nil
	}
	if policy.Drivers.Materializer != string(materialize.DriverYAMLOverlay) {
		return nil, fmt.Errorf("service %s policy %s uses unsupported materializer %q", name, policyName, policy.Drivers.Materializer)
	}
	sourceValue, err := config.Expand(policy.Config.SourceDir, context)
	if err != nil {
		return nil, fmt.Errorf("expand %s policy config source: %w", name, err)
	}
	sourceDirectory := sourceValue
	if !filepath.IsAbs(sourceDirectory) {
		sourceDirectory = filepath.Join(directory, sourceDirectory)
	}
	sourceDirectory = filepath.Clean(sourceDirectory)
	if !pathWithinDirectory(directory, sourceDirectory) {
		return nil, fmt.Errorf("service %s policy config source escapes the service directory: %s", name, sourceDirectory)
	}
	info, err := os.Lstat(sourceDirectory)
	if err != nil {
		return nil, fmt.Errorf("inspect service %s policy config source %s: %w", name, sourceDirectory, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("service %s policy config source must be a real directory: %s", name, sourceDirectory)
	}
	application, err := config.Expand(policy.Config.Application, context)
	if err != nil {
		return nil, fmt.Errorf("expand %s policy application: %w", name, err)
	}
	bootstrap := policy.Config.Bootstrap
	runtimeBootstrap := policy.Config.RuntimeBootstrap
	if runtimeBootstrap == "" && bootstrap != "" {
		runtimeBootstrap = bootstrap
	}
	bootstrap, err = config.Expand(bootstrap, context)
	if err != nil {
		return nil, fmt.Errorf("expand %s policy bootstrap: %w", name, err)
	}
	runtimeBootstrap, err = config.Expand(runtimeBootstrap, context)
	if err != nil {
		return nil, fmt.Errorf("expand %s policy runtime bootstrap: %w", name, err)
	}
	required := []string{bootstrap}
	if policy.Drivers.ConfigSource == string(materialize.SourceRepository) {
		required = append(required, application)
	}
	if err := inspectPolicyConfigSource(sourceDirectory, required...); err != nil {
		return nil, fmt.Errorf("inspect service %s policy config source: %w", name, err)
	}
	retryDelay, err := parseDuration(policy.Config.Apollo.RetryDelay, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s Apollo retry delay: %w", name, err)
	}
	timeout, err := parseDuration(policy.Config.Apollo.Timeout, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s Apollo timeout: %w", name, err)
	}
	planned := &PlannedConfig{
		Policy:    policyName,
		Framework: policy.Drivers.Framework,
		Discovery: policy.Drivers.Discovery,
		Plan: materialize.Plan{
			Service:          name,
			Driver:           materialize.Driver(policy.Drivers.Materializer),
			SourceDriver:     materialize.SourceDriver(policy.Drivers.ConfigSource),
			SourceDir:        sourceDirectory,
			ConfigRoot:       filepath.Join(plan.RunDir, "configs"),
			TargetDir:        context.ConfigDir,
			Application:      application,
			Bootstrap:        bootstrap,
			RuntimeBootstrap: runtimeBootstrap,
			Apollo: materialize.Apollo{
				Attempts:   policy.Config.Apollo.Attempts,
				RetryDelay: retryDelay,
				Timeout:    timeout,
			},
		},
	}
	server, hasServer := policy.Routing.Servers[service.Kind]
	patches := make([]model.ConfigPatch, 0, len(policy.Config.Patches)+len(service.Config.Patches))
	patches = append(patches, policy.Config.Patches...)
	if hasServer {
		patches = append(patches, server.Patches...)
	}
	patches = append(patches, service.Config.Patches...)
	for index, patch := range patches {
		plannedPatch, err := planConfigPatch(patch, application, context, "", 0)
		if err != nil {
			return nil, fmt.Errorf("plan %s config patch %d: %w", name, index, err)
		}
		planned.Plan.Patches = append(planned.Plan.Patches, plannedPatch)
	}
	dependencyNames := make([]string, 0, len(service.Dependencies))
	for dependency := range service.Dependencies {
		dependencyNames = append(dependencyNames, dependency)
	}
	sort.Strings(dependencyNames)
	for _, dependencyName := range dependencyNames {
		dependency := service.Dependencies[dependencyName]
		if dependency.Binding == "" {
			continue
		}
		rule := policy.Routing.RemoteDependency
		local := selected[dependencyName]
		if local {
			rule = policy.Routing.LocalDependency
		}
		route := PlannedRoute{
			Dependency: dependencyName,
			Binding:    dependency.Binding,
			Local:      local,
			Mode:       rule.Mode,
		}
		planned.Routes = append(planned.Routes, route)
		if rule.Mode == "" || rule.Mode == "preserve" {
			continue
		}
		port := plan.Workspace.Manifest.Services[dependencyName].Ports[dependency.Port]
		patch := model.ConfigPatch{
			File:  application,
			Path:  dependency.Binding,
			Mode:  rule.Mode,
			Value: rule.Value,
		}
		plannedPatch, err := planConfigPatch(patch, application, context, dependencyName, port)
		if err != nil {
			return nil, fmt.Errorf("plan %s -> %s config route: %w", name, dependencyName, err)
		}
		planned.Plan.Patches = append(planned.Plan.Patches, plannedPatch)
	}
	if hasServer {
		isolation, guards, err := planServerIsolation(server, application, context)
		if err != nil {
			return nil, fmt.Errorf("plan %s local isolation: %w", name, err)
		}
		planned.Isolation = isolation
		planned.Plan.Guards = append(planned.Plan.Guards, guards...)
		runtimeGuards, err := planRuntimeConfigGuards(planned.Plan, context)
		if err != nil {
			return nil, fmt.Errorf("plan %s runtime config isolation: %w", name, err)
		}
		if len(runtimeGuards) > 0 {
			planned.Isolation.RuntimeConfigDir = true
			planned.Plan.Guards = append(planned.Plan.Guards, runtimeGuards...)
		}
	}
	return planned, nil
}

func planRuntimeConfigGuards(plan materialize.Plan, context config.ExpandContext) ([]materialize.Guard, error) {
	if filepath.Clean(plan.RuntimeBootstrap) != "config-local.yaml" {
		return nil, nil
	}
	applicationPath := filepath.Join(plan.TargetDir, plan.Application)
	patches := []model.ConfigPatch{
		{File: plan.RuntimeBootstrap, Path: "localConfigEnable", Value: true},
		{File: plan.RuntimeBootstrap, Path: "localConfigPath", Value: applicationPath},
	}
	guards := make([]materialize.Guard, 0, len(patches))
	for _, patch := range patches {
		planned, err := planConfigPatch(patch, plan.Application, context, "", 0)
		if err != nil {
			return nil, err
		}
		guards = append(guards, materialize.Guard{File: planned.File, Path: planned.Path, Value: planned.Value, AllowCreate: true})
	}
	return guards, nil
}

func planServerIsolation(server model.ServerRoute, application string, context config.ExpandContext) (PlannedIsolation, []materialize.Guard, error) {
	if server.Isolation.Registration.Mode != "config" && server.Isolation.Registration.Mode != "not-applicable" {
		return PlannedIsolation{}, nil, fmt.Errorf("registration mode must be config or not-applicable")
	}
	if strings.TrimSpace(server.Isolation.Listener.Path) == "" || server.Isolation.Listener.Value == nil {
		return PlannedIsolation{}, nil, fmt.Errorf("listener path and value are required")
	}
	isolation := PlannedIsolation{
		RegistrationMode: server.Isolation.Registration.Mode,
		ListenerPort:     context.Manifest.Services[context.Service].Ports[server.Port],
	}
	guards := make([]materialize.Guard, 0, 2)
	if server.Isolation.Registration.Mode == "config" {
		patch := model.ConfigPatch{
			File:  server.Isolation.Registration.File,
			Path:  server.Isolation.Registration.Path,
			Value: server.Isolation.Registration.DisabledValue,
		}
		planned, err := planConfigPatch(patch, application, context, "", 0)
		if err != nil {
			return PlannedIsolation{}, nil, fmt.Errorf("registration guard: %w", err)
		}
		guard := materialize.Guard{File: planned.File, Path: planned.Path, Value: planned.Value}
		isolation.RegistrationGuard = &guard
		guards = append(guards, guard)
	}
	listenerPatch := model.ConfigPatch{
		File:  server.Isolation.Listener.File,
		Path:  server.Isolation.Listener.Path,
		Value: server.Isolation.Listener.Value,
	}
	plannedListener, err := planConfigPatch(listenerPatch, application, context, "", 0)
	if err != nil {
		return PlannedIsolation{}, nil, fmt.Errorf("listener guard: %w", err)
	}
	listener := materialize.Guard{File: plannedListener.File, Path: plannedListener.Path, Value: plannedListener.Value}
	isolation.ListenerGuard = listener
	guards = append(guards, listener)
	return isolation, guards, nil
}

func inspectPolicyConfigSource(sourceDirectory string, required ...string) error {
	err := filepath.WalkDir(sourceDirectory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("source path %q must not be a symbolic link", path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, relative := range required {
		if relative == "" {
			continue
		}
		if filepath.IsAbs(relative) {
			return fmt.Errorf("required config file must be relative: %s", relative)
		}
		path := filepath.Clean(filepath.Join(sourceDirectory, relative))
		if !pathWithinDirectory(sourceDirectory, path) {
			return fmt.Errorf("required config file escapes source directory: %s", relative)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect required config file %s: %w", relative, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("required config file must be a real file: %s", relative)
		}
	}
	return nil
}

func planConfigPatch(patch model.ConfigPatch, defaultFile string, context config.ExpandContext, dependency string, dependencyPort int) (materialize.Patch, error) {
	file := patch.File
	if file == "" {
		file = defaultFile
	}
	file, err := config.Expand(file, context)
	if err != nil {
		return materialize.Patch{}, err
	}
	path, err := config.Expand(patch.Path, context)
	if err != nil {
		return materialize.Patch{}, err
	}
	value, err := expandConfigValue(patch.Value, context, dependency, dependencyPort)
	if err != nil {
		return materialize.Patch{}, err
	}
	return materialize.Patch{File: file, Path: path, Value: value}, nil
}

func expandConfigValue(value interface{}, context config.ExpandContext, dependency string, dependencyPort int) (interface{}, error) {
	switch typed := value.(type) {
	case string:
		expression := typed
		if dependency != "" {
			expression = strings.ReplaceAll(expression, "${dependency.name}", dependency)
			expression = strings.ReplaceAll(expression, "${dependency.port}", strconv.Itoa(dependencyPort))
		}
		expanded, err := config.Expand(expression, context)
		if err != nil {
			return nil, err
		}
		if exactNumericConfigTemplate(typed) {
			if number, err := strconv.Atoi(expanded); err == nil {
				return number, nil
			}
		}
		return expanded, nil
	case []interface{}:
		result := make([]interface{}, len(typed))
		for index, item := range typed {
			expanded, err := expandConfigValue(item, context, dependency, dependencyPort)
			if err != nil {
				return nil, err
			}
			result[index] = expanded
		}
		return result, nil
	case map[string]interface{}:
		result := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			expanded, err := expandConfigValue(item, context, dependency, dependencyPort)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			result[key] = expanded
		}
		return result, nil
	case map[interface{}]interface{}:
		result := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			keyText, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("config patch map key must be a string")
			}
			expanded, err := expandConfigValue(item, context, dependency, dependencyPort)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", keyText, err)
			}
			result[keyText] = expanded
		}
		return result, nil
	default:
		return value, nil
	}
}

func exactNumericConfigTemplate(value string) bool {
	if value == "${dependency.port}" {
		return true
	}
	return strings.HasPrefix(value, "${port.") && strings.HasSuffix(value, "}") && strings.Count(value, "${") == 1
}

func configRoutesLocalDependency(config *PlannedConfig, dependency string) bool {
	if config == nil {
		return false
	}
	for _, route := range config.Routes {
		if route.Dependency == dependency && route.Local && route.Mode == "replace" {
			return true
		}
	}
	return false
}
