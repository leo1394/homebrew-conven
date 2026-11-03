package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/leo1394/homebrew-conven/internal/config"
	"github.com/leo1394/homebrew-conven/internal/dependency"
	"github.com/leo1394/homebrew-conven/internal/materialize"
	"github.com/leo1394/homebrew-conven/internal/model"
	"gopkg.in/yaml.v3"
)

// PrepareGuardConfigurations is a host-only preparation transaction. It returns
// a fresh private directory, never starts processes, never authorizes egress and
// never writes the source workspace. It deliberately returns no executable Plan.
func PrepareGuardConfigurations(ctx context.Context, workspace *WorkspaceData, environment string, selected []string, responseDirectory, outputParent string) (string, error) {
	return prepareGuardConfigurations(ctx, workspace, environment, selected, responseDirectory, outputParent, false)
}

// PrepareGuardDependencyReport retains independent per-service preparation
// failures as inventory issues. Its partial sources are never launch authority.
func PrepareGuardDependencyReport(ctx context.Context, workspace *WorkspaceData, environment string, selected []string, responseDirectory, outputParent string) (string, error) {
	return prepareGuardConfigurations(ctx, workspace, environment, selected, responseDirectory, outputParent, true)
}

func prepareGuardConfigurations(ctx context.Context, workspace *WorkspaceData, environment string, selected []string, responseDirectory, outputParent string, collectFailures bool) (string, error) {
	if workspace == nil || workspace.Manifest == nil || len(selected) == 0 { return "", fmt.Errorf("Guard preparation requires a workspace and service selection") }
	manifestBytes, err := os.ReadFile(workspace.ConfigPath)
	if err != nil { return "", fmt.Errorf("Guard preparation requires the source manifest") }
	manifestDigest := sha256.Sum256(manifestBytes)
	sources := guardPreparedSources{Version: 1, Environment: environment, Workspace: "/work", ManifestSHA256: hex.EncodeToString(manifestDigest[:]), Services: map[string]guardPreparedSource{}}
	inventory := guardDependencyInventory{Version: 1, Environment: environment, Items: []guardDependencyItem{}}
	if err := config.ValidateSelection(workspace.Manifest, selected); err != nil { return "", err }
	for _, name := range selected {
		service := workspace.Manifest.Services[name]
		address := "127.0.0.1"
		if service.Network.EffectiveListen() == model.NetworkListenAllInterfaces { address = "0.0.0.0" }
		for kind, port := range service.Ports {
			if kind != "http" && kind != "rpc" { continue }
			sources.Ingress = append(sources.Ingress, guardPreparedIngress{Service: name, Kind: kind, Address: address, Port: port})
		}
	}
	sort.Slice(sources.Ingress, func(i, j int) bool { return sources.Ingress[i].Port < sources.Ingress[j].Port })
	env, ok := workspace.Manifest.Environments[environment]
	if !ok { return "", fmt.Errorf("Guard preparation requires a declared environment") }
	values, err := config.LoadEnvironmentValues(workspace.Root, env.Env, env.EnvFile)
	if err != nil { return "", err }
	resolutions, err := dependency.Resolve(workspace.Manifest, environment, selected, values)
	if err != nil { return "", err }
	groups, err := dependencyStartGroups(workspace.Manifest, selected)
	if err != nil { return "", err }
	dir, err := os.MkdirTemp(outputParent, "guard-prepared-")
	if err != nil { return "", err }
	complete := false
	defer func() { if !complete { os.RemoveAll(dir) } }()
	configRoot := filepath.Join(dir, "configs")
	if err := os.Mkdir(configRoot, 0700); err != nil { return "", err }
	const guestRuntime = "/work/.conven/runtime/current"
	plan := &Plan{Workspace: workspace, EnvironmentName: environment, Environment: env, RunDir: guestRuntime, Resolutions: resolutions}
	selection := map[string]bool{}
	for _, name := range selected { selection[name] = true }
	for _, group := range groups { for _, name := range group {
		if err := ctx.Err(); err != nil { return "", err }
		_, prepareErr := func() (string, error) {
		service := workspace.Manifest.Services[name]
		serviceDirectory := service.Path
		if !filepath.IsAbs(serviceDirectory) { serviceDirectory = filepath.Join(workspace.Root, serviceDirectory) }
		expand := config.ExpandContext{Workspace: "/work", Service: name, ServiceDir: serviceDirectory, StateDir: "/work/.conven/runtime", RunDir: guestRuntime, ConfigDir: filepath.Join(guestRuntime, "configs", name), Artifact: filepath.Join(guestRuntime, "artifacts", name), Environment: environment, Manifest: workspace.Manifest}
		planned, err := planServiceConfig(plan, name, service, serviceDirectory, expand, selection)
		if err != nil { return "", err }
		if planned == nil || planned.Plan.SourceDriver != materialize.SourceApollo { return "", fmt.Errorf("service %s host preparation currently requires an Apollo config contract", name) }
		bootstrap, err := os.ReadFile(filepath.Join(planned.Plan.SourceDir, planned.Plan.Bootstrap))
		if err != nil { return "", fmt.Errorf("service %s bootstrap unavailable", name) }
		var identity struct { AppID string `yaml:"appId"`; Cluster string `yaml:"cluster"`; Namespace string `yaml:"namespaceName"` }
		if err := yaml.Unmarshal(bootstrap, &identity); err != nil { return "", fmt.Errorf("service %s bootstrap invalid", name) }
		data, err := readGuardResponse(filepath.Join(responseDirectory, name + ".json"))
		if os.IsNotExist(err) { return "", fmt.Errorf("service %s host response missing; configuration read policy must include this service", name) }
		if err != nil { return "", fmt.Errorf("service %s host response cannot be read (requires a regular file of at most 16 MiB)", name) }
		var response struct {
			AppID string `json:"appId"`
			Cluster string `json:"cluster"`
			Namespace string `json:"namespaceName"`
			Configurations struct { Content string `json:"content"` } `json:"configurations"`
		}
		if json.Unmarshal(data, &response) != nil || identity.AppID == "" || identity.Cluster == "" || identity.Namespace == "" || response.AppID != identity.AppID || (response.Cluster != identity.Cluster && response.Cluster != "default") || response.Namespace != identity.Namespace {
			return "", fmt.Errorf("service %s host response identity does not match bootstrap", name)
		}
		if response.Cluster != identity.Cluster { fmt.Fprintf(os.Stderr, "[Guard] service %s: Apollo returned default cluster fallback; requested cluster retained in runtime bootstrap\n", name) }
		snapshot, err := materialize.NewHostSnapshot(bootstrap, []byte(response.Configurations.Content))
		if err != nil { return "", fmt.Errorf("service %s host response invalid", name) }
		sources.Services[name] = guardPreparedSource{Bootstrap: bootstrap, Application: []byte(response.Configurations.Content)}
		planned.Plan.ConfigRoot = configRoot
		planned.Plan.TargetDir = filepath.Join(configRoot, name)
		if _, err := materialize.MaterializeFromSource(ctx, planned.Plan, snapshot); err != nil { return "", fmt.Errorf("service %s host materialization failed: %w", name, err) }
		// Guard adapters must see the effective configuration, including repository
		// fallbacks and routes, rather than only the remote Apollo fragment.
		effective, err := os.ReadFile(filepath.Join(planned.Plan.TargetDir, planned.Plan.Application))
		if err != nil { return "", fmt.Errorf("service %s effective configuration unavailable", name) }
		sources.Services[name] = guardPreparedSource{Bootstrap: bootstrap, Application: effective}
		bindings := map[string]string{}
		for _, binding := range service.Discovery.EffectiveConsumerBindings() { bindings[binding] = "remote" }
		for alias, declaration := range service.Dependencies {
			if declaration.Binding == "" { continue }
			mode := resolutions[name][alias].Mode
			if mode != "local" && mode != "disabled" { mode = "remote" }
			bindings[declaration.Binding] = mode
		}
		endpointKey := "endpoints"
		var endpointErr error
		if planned.Runtime == "go-zero" || planned.Framework == "go-zero" {
			workdir, expandErr := config.Expand(service.Runner.Workdir, expand)
			if expandErr != nil { return "", expandErr }
			declared, inspectErr := config.InspectGoRPCClientBindings(serviceDirectory, workdir)
			if inspectErr != nil {
				inventory.add(guardDependencyItem{Consumer: name, Source: "application", Mode: "unsupported", Code: "rpc-source-inspection-failed"})
			}
			for _, binding := range declared { if bindings[binding.YAMLKey] == "" { bindings[binding.YAMLKey] = "remote" } }
			endpointKey, endpointErr = config.InspectGoRPCClientEndpointYAMLKey(serviceDirectory, workdir)
		}
		for _, route := range planned.Routes { if route.Local { bindings[route.Binding] = "local" } }
		for _, binding := range workspace.Manifest.Workspace.DisabledBindings { if _, found := bindings[binding]; found { bindings[binding] = "disabled" } }
		inventory.inspect(name, effective, bindings, endpointKey, endpointErr)
		return "", nil
		}()
		if prepareErr != nil {
			if !collectFailures { return "", prepareErr }
			if err := ctx.Err(); err != nil { return "", err }
			delete(sources.Services, name)
			inventory.add(guardDependencyItem{Consumer: name, Source: "application", Mode: "invalid", Code: "CONFIG_PREPARATION_FAILED"})
		}
	} }
	data, err := json.Marshal(sources)
	if err != nil { return "", err }
	if err := os.WriteFile(filepath.Join(dir, "sources.json"), data, 0600); err != nil { return "", err }
	inventory.sort()
	data, err = json.Marshal(inventory)
	if err != nil { return "", err }
	if err := os.WriteFile(filepath.Join(dir, "dependencies.json"), data, 0600); err != nil { return "", err }
	complete = true
	return dir, nil
}

// This is preparation input, not an isolation certificate. Only the VM's
// privileged initialization may publish a runtime certificate after installing
// the matching host-approved network policy.
type guardPreparedSources struct {
	Version int `json:"version"`
	Environment string `json:"environment"`
	Workspace string `json:"workspace"`
	ManifestSHA256 string `json:"manifestSHA256"`
	Services map[string]guardPreparedSource `json:"services"`
	Ingress []guardPreparedIngress `json:"ingress,omitempty"`
}

type guardPreparedIngress struct {
	Service string `json:"service"`
	Kind string `json:"kind"`
	Address string `json:"address"`
	Port int `json:"port"`
}

type guardPreparedSource struct {
	Bootstrap []byte `json:"bootstrap"`
	Application []byte `json:"application"`
}

func readGuardResponse(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil { return nil, err }
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16 << 20 { return nil, fmt.Errorf("invalid host response file") }
	data, err := io.ReadAll(io.LimitReader(file, (16 << 20) + 1))
	if err != nil || len(data) > 16 << 20 { return nil, fmt.Errorf("host response exceeds limit") }
	return data, nil
}
