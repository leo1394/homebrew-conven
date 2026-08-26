package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/leo1394/homebrew-conven/internal/config"
	"github.com/leo1394/homebrew-conven/internal/dependency"
	"github.com/leo1394/homebrew-conven/internal/model"
)

type CommonOptions struct {
	Cwd         string
	Environment string
	Kubeconfig  string
	Context     string
	Namespace   string
}

type WorkspaceData struct {
	Root       string
	ConfigPath string
	Manifest   *model.Manifest
	Settings   map[string]string
	Store      *Store
}

type Plan struct {
	Workspace       *WorkspaceData
	EnvironmentName string
	Environment     model.Environment
	Selected        []string
	DeclaredRemote  []string
	Order           []string
	Groups          [][]string
	RunDir          string
	ReuseCurrent    bool
	Services        map[string]PlannedService
	Resolutions     map[string]map[string]dependency.Resolution
	Connection      ConnectionConfig
}

type PlannedService struct {
	Name        string
	Kind        string
	Directory   string
	Workdir     string
	RunWorkdir  string
	Artifact    string
	Ports       map[string]int
	Config      *PlannedConfig
	Prepare     []string
	Build       []string
	Run         []string
	Environment []string
	Health      HealthCheck
	LogPath     string
}

func OpenWorkspace(options CommonOptions) (*WorkspaceData, error) {
	configPath, workspace, err := config.ResolvePath(options.Cwd)
	if err != nil {
		return nil, err
	}
	manifest, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	settings, err := config.EffectiveSettings(workspace, "")
	if err != nil {
		return nil, err
	}
	store, err := NewStore(workspace)
	if err != nil {
		return nil, err
	}
	return &WorkspaceData{
		Root:       workspace,
		ConfigPath: configPath,
		Manifest:   manifest,
		Settings:   settings,
		Store:      store,
	}, nil
}

func BuildPlan(workspace *WorkspaceData, options CommonOptions, selected []string) (*Plan, error) {
	return buildPlan(workspace, options, selected, true, false)
}

func BuildRestartPlan(workspace *WorkspaceData, options CommonOptions, selected []string) (*Plan, error) {
	return buildPlan(workspace, options, selected, false, true)
}

func buildPlan(workspace *WorkspaceData, options CommonOptions, selected []string, includeConnection bool, reuseCurrent bool) (*Plan, error) {
	if len(selected) == 0 {
		return nil, fmt.Errorf("at least one local service is required")
	}
	if err := config.ValidateSelection(workspace.Manifest, selected); err != nil {
		return nil, err
	}
	environmentName := options.Environment
	if environmentName == "" {
		var err error
		environmentName, err = defaultEnvironmentName(workspace.Manifest)
		if err != nil {
			return nil, err
		}
	}
	environment, found := workspace.Manifest.Environments[environmentName]
	if !found && len(workspace.Manifest.Environments) > 0 {
		names := make([]string, 0, len(workspace.Manifest.Environments))
		for name := range workspace.Manifest.Environments {
			names = append(names, name)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("environment %q is not declared (available: %s)", environmentName, strings.Join(names, ", "))
	}
	selected = append([]string(nil), selected...)
	groups, err := dependencyStartGroups(workspace.Manifest, selected)
	if err != nil {
		return nil, err
	}
	order := make([]string, 0, len(selected))
	for _, group := range groups {
		order = append(order, group...)
	}
	plan := &Plan{
		Workspace:       workspace,
		EnvironmentName: environmentName,
		Environment:     environment,
		Selected:        selected,
		Order:           order,
		Groups:          groups,
		RunDir:          workspace.Store.CurrentDir,
		ReuseCurrent:    reuseCurrent,
		Services:        make(map[string]PlannedService, len(selected)),
	}
	environmentValues := make(map[string]string, len(environment.Env))
	mergeValues(environmentValues, environment.Env)
	var resolutions map[string]map[string]dependency.Resolution
	if workspace.Manifest.Version == 1 {
		resolutions = resolveLegacyDependencies(workspace.Manifest, selected)
	} else {
		environmentValues, err = config.LoadEnvironmentValues(workspace.Root, environment.Env, environment.EnvFile)
		if err != nil {
			return nil, fmt.Errorf("load environment %s: %w", environmentName, err)
		}
		resolutions, err = dependency.Resolve(workspace.Manifest, environmentName, selected, environmentValues)
		if err != nil {
			return nil, err
		}
	}
	plan.Resolutions = resolutions
	plan.DeclaredRemote = remoteResolutionNames(resolutions)
	plan.Environment.Env = environmentValues
	if err := validateInboundRouting(ConnectionConfig{Driver: environment.Connection.Driver}); err != nil {
		return nil, err
	}
	selectedSet := make(map[string]bool, len(selected))
	for _, name := range selected {
		selectedSet[name] = true
	}
	for _, name := range order {
		service, err := planService(plan, name, selectedSet)
		if err != nil {
			return nil, err
		}
		plan.Services[name] = service
	}
	if includeConnection {
		connection, err := planConnection(plan, options)
		if err != nil {
			return nil, err
		}
		if err := validateInboundRouting(connection); err != nil {
			return nil, err
		}
		plan.Connection = connection
	}
	return plan, nil
}

func defaultEnvironmentName(manifest *model.Manifest) (string, error) {
	if manifest != nil && manifest.Version == 1 {
		return "dev", nil
	}
	if manifest == nil || len(manifest.Environments) == 0 {
		return "dev", nil
	}
	if _, found := manifest.Environments["dev"]; found {
		return "dev", nil
	}
	if len(manifest.Environments) == 1 {
		for name := range manifest.Environments {
			return name, nil
		}
	}
	if _, found := manifest.Environments["local"]; found {
		return "local", nil
	}
	names := make([]string, 0, len(manifest.Environments))
	for name := range manifest.Environments {
		names = append(names, name)
	}
	sort.Strings(names)
	return "", fmt.Errorf("no default environment is available (profiles: %s); pass --env NAME", strings.Join(names, ", "))
}

func ExpandLocalServiceDependencies(manifest *model.Manifest, selected []string) ([]string, error) {
	if err := config.ValidateSelection(manifest, selected); err != nil {
		return nil, err
	}
	expanded := append([]string(nil), selected...)
	seen := make(map[string]bool, len(selected))
	for _, name := range selected {
		seen[name] = true
	}
	for index := 0; index < len(expanded); index++ {
		service := manifest.Services[expanded[index]]
		dependencies := make([]string, 0, len(service.Dependencies))
		for alias, declaration := range service.Dependencies {
			dependencyName := localServiceForDependency(manifest, alias, declaration)
			if dependencyName != "" && !seen[dependencyName] {
				dependencies = append(dependencies, dependencyName)
			}
		}
		sort.Strings(dependencies)
		for _, dependencyName := range dependencies {
			if seen[dependencyName] {
				continue
			}
			seen[dependencyName] = true
			expanded = append(expanded, dependencyName)
		}
	}
	return expanded, nil
}

func resolveLegacyDependencies(manifest *model.Manifest, selected []string) map[string]map[string]dependency.Resolution {
	selectedSet := make(map[string]bool, len(selected))
	for _, name := range selected {
		selectedSet[name] = true
	}
	resolutions := make(map[string]map[string]dependency.Resolution, len(selected))
	for _, owner := range selected {
		service := manifest.Services[owner]
		aliases := make([]string, 0, len(service.Dependencies))
		for alias := range service.Dependencies {
			aliases = append(aliases, alias)
		}
		sort.Strings(aliases)
		resolutions[owner] = make(map[string]dependency.Resolution, len(aliases))
		for _, alias := range aliases {
			declaration := service.Dependencies[alias]
			resolution := dependency.Resolution{Owner: owner, Alias: alias, Target: alias}
			if selectedSet[alias] {
				resolution.Mode = "local"
				resolution.Env = copyStringMap(declaration.LocalEnv)
				resolution.Host = "127.0.0.1"
				resolution.Ports = copyPorts(manifest.Services[alias].Ports)
			} else {
				resolution.Mode = "remote"
				resolution.Env = copyStringMap(declaration.RemoteEnv)
			}
			resolutions[owner][alias] = resolution
		}
	}
	return resolutions
}

func copyStringMap(values map[string]string) map[string]string {
	copied := make(map[string]string, len(values))
	for key, value := range values {
		copied[key] = value
	}
	return copied
}

func planService(plan *Plan, name string, selected map[string]bool) (PlannedService, error) {
	service := plan.Workspace.Manifest.Services[name]
	directory := service.Path
	if !filepath.IsAbs(directory) {
		directory = filepath.Join(plan.Workspace.Root, directory)
	}
	directory = filepath.Clean(directory)
	info, err := os.Stat(directory)
	if err != nil {
		return PlannedService{}, fmt.Errorf("inspect service %s directory %s: %w", name, directory, err)
	}
	if !info.IsDir() {
		return PlannedService{}, fmt.Errorf("service %s path is not a directory: %s", name, directory)
	}
	workdir := directory
	if service.Runner.Workdir != "" {
		workdir = service.Runner.Workdir
		if !filepath.IsAbs(workdir) {
			workdir = filepath.Join(directory, workdir)
		}
		workdir = filepath.Clean(workdir)
	}
	if info, err := os.Stat(workdir); err != nil || !info.IsDir() {
		if err == nil {
			err = fmt.Errorf("not a directory")
		}
		return PlannedService{}, fmt.Errorf("inspect service %s workdir %s: %w", name, workdir, err)
	}
	artifact := filepath.Join(plan.RunDir, "artifacts", name)
	configDirectory := filepath.Join(plan.RunDir, "configs", name)
	baseContext := config.ExpandContext{
		Workspace:   plan.Workspace.Root,
		Service:     name,
		ServiceDir:  directory,
		StateDir:    plan.Workspace.Store.Root,
		RunDir:      plan.RunDir,
		ConfigDir:   configDirectory,
		Artifact:    artifact,
		Environment: plan.EnvironmentName,
		Manifest:    plan.Workspace.Manifest,
	}
	if service.Runner.Artifact != "" {
		artifact, err = config.Expand(service.Runner.Artifact, baseContext)
		if err != nil {
			return PlannedService{}, fmt.Errorf("expand %s artifact: %w", name, err)
		}
		if !filepath.IsAbs(artifact) {
			artifact = filepath.Join(plan.RunDir, "artifacts", artifact)
		}
		baseContext.Artifact = filepath.Clean(artifact)
	}
	plannedConfig, err := planServiceConfig(plan, name, service, directory, baseContext, selected)
	if err != nil {
		return PlannedService{}, err
	}
	if err := validatePlannedIsolation(name, service.Kind, plannedConfig); err != nil {
		return PlannedService{}, err
	}
	_, policy, hasPolicy := policyForService(plan.Workspace.Manifest, service)
	runWorkdir := workdir
	if service.Runner.RunWorkdir != "" {
		runWorkdir, err = config.Expand(service.Runner.RunWorkdir, baseContext)
		if err != nil {
			return PlannedService{}, fmt.Errorf("expand %s run workdir: %w", name, err)
		}
		if !filepath.IsAbs(runWorkdir) {
			runWorkdir = filepath.Join(directory, runWorkdir)
		}
		runWorkdir = filepath.Clean(runWorkdir)
	}
	prepare, err := expandCommand(service.Runner.Prepare, baseContext)
	if err != nil {
		return PlannedService{}, fmt.Errorf("expand %s prepare command: %w", name, err)
	}
	if err := inspectPlannedRunWorkdir(name, runWorkdir, prepare, plan.RunDir, plan.ReuseCurrent); err != nil {
		return PlannedService{}, err
	}
	build, err := expandCommand(service.Runner.Build, baseContext)
	if err != nil {
		return PlannedService{}, fmt.Errorf("expand %s build command: %w", name, err)
	}
	runCommand := append([]string(nil), service.Runner.Run...)
	if hasPolicy {
		runCommand = append(runCommand, policy.Process.Args...)
	}
	run, err := expandCommand(runCommand, baseContext)
	if err != nil {
		return PlannedService{}, fmt.Errorf("expand %s run command: %w", name, err)
	}
	environmentValues := make(map[string]string)
	mergeValues(environmentValues, plan.Environment.Env)
	if hasPolicy {
		mergeValues(environmentValues, policy.Process.Env)
	}
	mergeValues(environmentValues, service.Env)
	mergeValues(environmentValues, service.LocalEnv)
	dependencyEnvironmentValues := make(map[string]string)
	dependencyEnvironmentOwners := make(map[string]string)
	dependencyNames := make([]string, 0, len(service.Dependencies))
	for dependencyName := range service.Dependencies {
		dependencyNames = append(dependencyNames, dependencyName)
	}
	sort.Strings(dependencyNames)
	for _, dependencyName := range dependencyNames {
		declaration := service.Dependencies[dependencyName]
		resolution, found := plan.Resolutions[name][dependencyName]
		if !found {
			return PlannedService{}, fmt.Errorf("service %s dependency %s has no resolved route", name, dependencyName)
		}
		dependencyEnvironment := resolution.Env
		if resolution.Mode == "local" {
			if len(dependencyEnvironment) == 0 && !configRoutesLocalDependency(plannedConfig, resolution.Target) {
				if plan.Workspace.Manifest.Version == 1 {
					return PlannedService{}, fmt.Errorf("selected local dependency %s -> %s has no localEnv routing override", name, resolution.Target)
				}
				return PlannedService{}, fmt.Errorf("selected local dependency %s -> %s has no env routing override", name, resolution.Target)
			}
			if len(dependencyEnvironment) == 0 {
				dependencyEnvironment = declaration.LocalEnv
			}
		}
		keys := make([]string, 0, len(dependencyEnvironment))
		for key := range dependencyEnvironment {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value := dependencyEnvironment[key]
			if previousValue, found := dependencyEnvironmentValues[key]; found && previousValue != value {
				return PlannedService{}, fmt.Errorf("service %s dependency environment key %q conflicts between %s=%q and %s=%q", name, key, dependencyEnvironmentOwners[key], previousValue, dependencyName, value)
			}
			dependencyEnvironmentValues[key] = value
			if _, found := dependencyEnvironmentOwners[key]; !found {
				dependencyEnvironmentOwners[key] = dependencyName
			}
			environmentValues[key] = value
		}
	}
	mergeValues(environmentValues, map[string]string{
		"CONVEN_WORKSPACE":         plan.Workspace.Root,
		"CONVEN_SERVICE":           name,
		"CONVEN_ENV":               plan.EnvironmentName,
		"CONVEN_STATE_DIR":         plan.Workspace.Store.Root,
		"CONVEN_RUN_DIR":           plan.RunDir,
		"CONVEN_CONFIG_DIR":        filepath.Join(plan.RunDir, "configs", name),
		"CONVEN_ARTIFACT":          baseContext.Artifact,
		"CONVEN_SELECTED_SERVICES": strings.Join(plan.Selected, ","),
	})
	expandedValues, err := expandValues(environmentValues, baseContext)
	if err != nil {
		return PlannedService{}, fmt.Errorf("expand %s environment: %w", name, err)
	}
	if err := validateRuntimeConfigConsumption(name, plannedConfig, run, expandedValues); err != nil {
		return PlannedService{}, err
	}
	health, err := expandHealth(service.Health, baseContext, runWorkdir, CommandEnvironment(expandedValues))
	if err != nil {
		return PlannedService{}, fmt.Errorf("expand %s health check: %w", name, err)
	}
	return PlannedService{
		Name:        name,
		Kind:        service.Kind,
		Directory:   directory,
		Workdir:     workdir,
		RunWorkdir:  runWorkdir,
		Artifact:    baseContext.Artifact,
		Ports:       copyPorts(service.Ports),
		Config:      plannedConfig,
		Prepare:     prepare,
		Build:       build,
		Run:         run,
		Environment: CommandEnvironment(expandedValues),
		Health:      health,
		LogPath:     filepath.Join(plan.RunDir, "logs", name+".log"),
	}, nil
}

func copyPorts(ports map[string]int) map[string]int {
	copied := make(map[string]int, len(ports))
	for name, port := range ports {
		copied[name] = port
	}
	return copied
}

func inspectRunWorkdir(service PlannedService) error {
	info, err := os.Stat(service.RunWorkdir)
	if err != nil {
		return fmt.Errorf("inspect service %s run workdir %s before run: %w", service.Name, service.RunWorkdir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("service %s run workdir is not a directory before run: %s", service.Name, service.RunWorkdir)
	}
	return nil
}

func inspectPlannedRunWorkdir(name string, directory string, prepare []string, currentDir string, reuseCurrent bool) error {
	if !reuseCurrent && pathWithinDirectory(currentDir, directory) {
		if freshRuntimeDirectory(name, directory, currentDir) || len(prepare) > 0 {
			return nil
		}
		return fmt.Errorf("service %s run workdir %s will be removed by the fresh runtime reset and runner.prepare is empty", name, directory)
	}
	info, err := os.Stat(directory)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("service %s run workdir is not a directory: %s", name, directory)
		}
		return nil
	}
	if os.IsNotExist(err) && len(prepare) > 0 {
		return nil
	}
	if os.IsNotExist(err) {
		return fmt.Errorf("service %s run workdir %s does not exist and runner.prepare is empty", name, directory)
	}
	return fmt.Errorf("inspect service %s run workdir %s: %w", name, directory, err)
}

func freshRuntimeDirectory(name string, directory string, currentDir string) bool {
	directory = filepath.Clean(directory)
	for _, created := range []string{
		currentDir,
		filepath.Join(currentDir, "artifacts"),
		filepath.Join(currentDir, "configs"),
		filepath.Join(currentDir, "configs", name),
		filepath.Join(currentDir, "logs"),
	} {
		if directory == filepath.Clean(created) {
			return true
		}
	}
	return false
}

func pathWithinDirectory(directory string, path string) bool {
	relative, err := filepath.Rel(filepath.Clean(directory), filepath.Clean(path))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func planConnection(plan *Plan, options CommonOptions) (ConnectionConfig, error) {
	connection := plan.Environment.Connection
	if connection.Driver == "" || connection.Driver == "none" {
		return ConnectionConfig{Driver: connection.Driver}, nil
	}
	context := config.ExpandContext{
		Workspace:   plan.Workspace.Root,
		StateDir:    plan.Workspace.Store.Root,
		RunDir:      plan.RunDir,
		Environment: plan.EnvironmentName,
		Manifest:    plan.Workspace.Manifest,
	}
	command := ""
	var err error
	if connection.Driver == "ktctl" {
		if configured := strings.TrimSpace(plan.Workspace.Settings["ktctl.path"]); configured != "" {
			command, err = config.ResolveExecutable(configured)
			if err != nil {
				return ConnectionConfig{}, fmt.Errorf("resolve ktctl.path: %w", err)
			}
		}
	}
	if command == "" {
		command, err = config.Expand(connection.Command, context)
		if err != nil {
			return ConnectionConfig{}, fmt.Errorf("expand connection command: %w", err)
		}
	}
	if command != "" && strings.ContainsRune(command, filepath.Separator) && !filepath.IsAbs(command) {
		command = filepath.Clean(filepath.Join(plan.Workspace.Root, command))
	}
	args, err := expandCommand(connection.Args, context)
	if err != nil {
		return ConnectionConfig{}, fmt.Errorf("expand connection args: %w", err)
	}
	connection.Kubeconfig, err = config.Expand(connection.Kubeconfig, context)
	if err != nil {
		return ConnectionConfig{}, fmt.Errorf("expand connection kubeconfig: %w", err)
	}
	connection.Context, err = config.Expand(connection.Context, context)
	if err != nil {
		return ConnectionConfig{}, fmt.Errorf("expand connection context: %w", err)
	}
	connection.Namespace, err = config.Expand(connection.Namespace, context)
	if err != nil {
		return ConnectionConfig{}, fmt.Errorf("expand connection namespace: %w", err)
	}
	kubeconfig := ""
	if connection.Driver == "ktctl" {
		kubeconfig, err = config.ResolveKubeconfig(connection, options.Kubeconfig, plan.Workspace.Settings["ktctl.kubeconfig"])
		if err != nil {
			return ConnectionConfig{}, err
		}
		if !filepath.IsAbs(kubeconfig) {
			kubeconfig = filepath.Join(plan.Workspace.Root, kubeconfig)
		}
		kubeconfig = filepath.Clean(kubeconfig)
	}
	connectionContext := connection.Context
	if options.Context != "" {
		connectionContext = options.Context
	}
	namespace := connection.Namespace
	if options.Namespace != "" {
		namespace = options.Namespace
	}
	timeout, err := parseDuration(connection.Timeout, 60*time.Second)
	if err != nil {
		return ConnectionConfig{}, fmt.Errorf("parse connection timeout: %w", err)
	}
	readiness := make([]ConnectionEndpoint, 0, len(connection.Readiness))
	for _, endpoint := range connection.Readiness {
		address, err := config.Expand(endpoint.Address, context)
		if err != nil {
			return ConnectionConfig{}, fmt.Errorf("expand connection readiness %q: %w", endpoint.Name, err)
		}
		readiness = append(readiness, ConnectionEndpoint{Name: endpoint.Name, Address: address})
	}
	result := ConnectionConfig{
		Driver:     connection.Driver,
		Command:    command,
		Args:       args,
		Kubeconfig: kubeconfig,
		Context:    connectionContext,
		Namespace:  namespace,
		Sudo:       connection.Sudo,
		Timeout:    timeout,
		Readiness:  readiness,
	}
	if len(result.Readiness) == 0 {
		return ConnectionConfig{}, fmt.Errorf("connection readiness must contain at least one TCP endpoint")
	}
	for _, endpoint := range result.Readiness {
		if strings.TrimSpace(endpoint.Address) == "" {
			return ConnectionConfig{}, fmt.Errorf("connection readiness %q address is empty", endpoint.Name)
		}
	}
	if _, err := BuildConnectionCommand(result); err != nil {
		return ConnectionConfig{}, err
	}
	return result, nil
}

func expandHealth(health model.Health, context config.ExpandContext, directory string, environment []string) (HealthCheck, error) {
	address, err := config.Expand(health.Address, context)
	if err != nil {
		return HealthCheck{}, err
	}
	url, err := config.Expand(health.URL, context)
	if err != nil {
		return HealthCheck{}, err
	}
	command, err := expandCommand(health.Command, context)
	if err != nil {
		return HealthCheck{}, err
	}
	timeout, err := parseDuration(health.Timeout, 60*time.Second)
	if err != nil {
		return HealthCheck{}, err
	}
	result := HealthCheck{
		Type:        health.Type,
		Address:     address,
		URL:         url,
		Command:     command,
		Directory:   directory,
		Environment: environment,
		Timeout:     timeout,
	}
	switch result.Type {
	case "", "process":
	case "tcp":
		if strings.TrimSpace(result.Address) == "" {
			return HealthCheck{}, fmt.Errorf("tcp health address is required")
		}
	case "http":
		if strings.TrimSpace(result.URL) == "" {
			return HealthCheck{}, fmt.Errorf("http health URL is required")
		}
	case "command":
		if len(result.Command) == 0 {
			return HealthCheck{}, fmt.Errorf("command health check is required")
		}
	default:
		return HealthCheck{}, fmt.Errorf("unsupported health type %q", result.Type)
	}
	return result, nil
}

func expandCommand(command []string, context config.ExpandContext) ([]string, error) {
	result := make([]string, len(command))
	for index, argument := range command {
		expanded, err := config.Expand(argument, context)
		if err != nil {
			return nil, fmt.Errorf("argument %d: %w", index+1, err)
		}
		result[index] = expanded
	}
	return result, nil
}

func expandValues(values map[string]string, context config.ExpandContext) (map[string]string, error) {
	result := make(map[string]string, len(values))
	for key, value := range values {
		expanded, err := config.Expand(value, context)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		result[key] = expanded
	}
	return result, nil
}

func mergeValues(target map[string]string, source map[string]string) {
	for key, value := range source {
		target[key] = value
	}
}

func parseDuration(value string, fallback time.Duration) (time.Duration, error) {
	if value == "" {
		return fallback, nil
	}
	return time.ParseDuration(value)
}

func dependencyOrder(manifest *model.Manifest, selected []string) ([]string, error) {
	groups, err := dependencyStartGroups(manifest, selected)
	if err != nil {
		return nil, err
	}
	order := make([]string, 0, len(selected))
	for _, group := range groups {
		order = append(order, group...)
	}
	return order, nil
}

func dependencyStartGroups(manifest *model.Manifest, selected []string) ([][]string, error) {
	selectedSet := make(map[string]bool, len(selected))
	for _, name := range selected {
		selectedSet[name] = true
	}
	components := dependencyComponents(manifest, selectedSet)
	componentByService := make(map[string]int, len(selected))
	for componentIndex, component := range components {
		for _, name := range component {
			componentByService[name] = componentIndex
		}
	}
	remainingDependencies := make([]int, len(components))
	dependents := make([]map[int]bool, len(components))
	dependencies := make([]map[int]bool, len(components))
	for componentIndex := range components {
		dependents[componentIndex] = make(map[int]bool)
		dependencies[componentIndex] = make(map[int]bool)
	}
	for _, name := range selected {
		componentIndex := componentByService[name]
		for alias, declaration := range manifest.Services[name].Dependencies {
			dependency := localServiceForDependency(manifest, alias, declaration)
			if dependency == "" || !selectedSet[dependency] {
				continue
			}
			dependencyComponent := componentByService[dependency]
			if dependencyComponent == componentIndex || dependencies[componentIndex][dependencyComponent] {
				continue
			}
			dependencies[componentIndex][dependencyComponent] = true
			remainingDependencies[componentIndex]++
			dependents[dependencyComponent][componentIndex] = true
		}
	}
	ready := make([]int, 0)
	for componentIndex := range components {
		if remainingDependencies[componentIndex] == 0 {
			ready = append(ready, componentIndex)
		}
	}
	sortComponentIndexes(ready, components)
	groups := make([][]string, 0, len(components))
	orderedCount := 0
	for len(ready) > 0 {
		componentIndex := ready[0]
		ready = ready[1:]
		group := append([]string(nil), components[componentIndex]...)
		groups = append(groups, group)
		orderedCount += len(group)
		componentDependents := make([]int, 0, len(dependents[componentIndex]))
		for dependent := range dependents[componentIndex] {
			componentDependents = append(componentDependents, dependent)
		}
		sortComponentIndexes(componentDependents, components)
		for _, dependent := range componentDependents {
			remainingDependencies[dependent]--
			if remainingDependencies[dependent] == 0 {
				ready = append(ready, dependent)
				sortComponentIndexes(ready, components)
			}
		}
	}
	if orderedCount != len(selected) {
		return nil, fmt.Errorf("could not order selected local dependency components")
	}
	return groups, nil
}

func dependencyComponents(manifest *model.Manifest, selected map[string]bool) [][]string {
	indices := make(map[string]int, len(selected))
	lowLinks := make(map[string]int, len(selected))
	onStack := make(map[string]bool, len(selected))
	stack := make([]string, 0, len(selected))
	components := make([][]string, 0)
	nextIndex := 0

	var visit func(string)
	visit = func(name string) {
		indices[name] = nextIndex
		lowLinks[name] = nextIndex
		nextIndex++
		stack = append(stack, name)
		onStack[name] = true

		dependencies := make([]string, 0)
		for alias, declaration := range manifest.Services[name].Dependencies {
			dependency := localServiceForDependency(manifest, alias, declaration)
			if dependency != "" && selected[dependency] {
				dependencies = append(dependencies, dependency)
			}
		}
		sort.Strings(dependencies)
		for _, dependency := range dependencies {
			dependencyIndex, visited := indices[dependency]
			if !visited {
				visit(dependency)
				if lowLinks[dependency] < lowLinks[name] {
					lowLinks[name] = lowLinks[dependency]
				}
			} else if onStack[dependency] && dependencyIndex < lowLinks[name] {
				lowLinks[name] = dependencyIndex
			}
		}

		if lowLinks[name] != indices[name] {
			return
		}
		component := make([]string, 0)
		for len(stack) > 0 {
			lastIndex := len(stack) - 1
			member := stack[lastIndex]
			stack = stack[:lastIndex]
			onStack[member] = false
			component = append(component, member)
			if member == name {
				break
			}
		}
		sort.Strings(component)
		components = append(components, component)
	}

	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, visited := indices[name]; !visited {
			visit(name)
		}
	}
	return components
}

func sortComponentIndexes(indexes []int, components [][]string) {
	sort.Slice(indexes, func(left int, right int) bool {
		return components[indexes[left]][0] < components[indexes[right]][0]
	})
}

func remoteResolutionNames(resolutions map[string]map[string]dependency.Resolution) []string {
	remoteSet := make(map[string]bool)
	for _, serviceDependencies := range resolutions {
		for alias, resolution := range serviceDependencies {
			if resolution.Mode == "remote" {
				remoteSet[alias] = true
			}
		}
	}
	remote := make([]string, 0, len(remoteSet))
	for name := range remoteSet {
		remote = append(remote, name)
	}
	sort.Strings(remote)
	return remote
}

func localServiceForDependency(manifest *model.Manifest, alias string, declaration model.Dependency) string {
	if declaration.LocalService != "" {
		return declaration.LocalService
	}
	if _, found := manifest.Services[alias]; found {
		return alias
	}
	return ""
}
