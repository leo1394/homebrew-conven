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

	"github.com/leo1394/homebrew-conven/internal/config"
	"github.com/leo1394/homebrew-conven/internal/dependency"
	"github.com/leo1394/homebrew-conven/internal/materialize"
	"gopkg.in/yaml.v3"
)

// PrepareGuardConfigurations is a host-only preparation transaction. It returns
// a fresh private directory, never starts processes, never authorizes egress and
// never writes the source workspace. It deliberately returns no executable Plan.
func PrepareGuardConfigurations(ctx context.Context, workspace *WorkspaceData, environment string, selected []string, responseDirectory, outputParent string) (string, error) {
	if workspace == nil || workspace.Manifest == nil || len(selected) == 0 { return "", fmt.Errorf("Guard preparation requires a workspace and service selection") }
	manifestBytes, err := os.ReadFile(workspace.ConfigPath)
	if err != nil { return "", fmt.Errorf("Guard preparation requires the source manifest") }
	manifestDigest := sha256.Sum256(manifestBytes)
	sources := guardPreparedSources{Version: 1, Environment: environment, Workspace: "/work", ManifestSHA256: hex.EncodeToString(manifestDigest[:]), Services: map[string]guardPreparedSource{}}
	if err := config.ValidateSelection(workspace.Manifest, selected); err != nil { return "", err }
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
		if err != nil { return "", fmt.Errorf("service %s host response unavailable or too large", name) }
		var response struct {
			AppID string `json:"appId"`
			Cluster string `json:"cluster"`
			Namespace string `json:"namespaceName"`
			Configurations struct { Content string `json:"content"` } `json:"configurations"`
		}
		if json.Unmarshal(data, &response) != nil || identity.AppID == "" || identity.Cluster == "" || identity.Namespace == "" || response.AppID != identity.AppID || response.Cluster != identity.Cluster || response.Namespace != identity.Namespace {
			return "", fmt.Errorf("service %s host response identity does not match bootstrap", name)
		}
		snapshot, err := materialize.NewHostSnapshot(bootstrap, []byte(response.Configurations.Content))
		if err != nil { return "", fmt.Errorf("service %s host response invalid", name) }
		sources.Services[name] = guardPreparedSource{Bootstrap: bootstrap, Application: []byte(response.Configurations.Content)}
		planned.Plan.ConfigRoot = configRoot
		planned.Plan.TargetDir = filepath.Join(configRoot, name)
		if _, err := materialize.MaterializeFromSource(ctx, planned.Plan, snapshot); err != nil { return "", fmt.Errorf("service %s host materialization failed: %w", name, err) }
	} }
	data, err := json.Marshal(sources)
	if err != nil { return "", err }
	if err := os.WriteFile(filepath.Join(dir, "sources.json"), data, 0600); err != nil { return "", err }
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
