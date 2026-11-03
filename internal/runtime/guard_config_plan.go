package runtime

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/config"
	"github.com/leo1394/homebrew-conven/internal/dependency"
	"github.com/leo1394/homebrew-conven/internal/materialize"
	"gopkg.in/yaml.v3"
)

type GuardConfigurationRequest struct {
	Service string `json:"service"`
	Authority string `json:"authority"`
	AppID string `json:"appId"`
	Cluster string `json:"cluster"`
	Namespace string `json:"namespace"`
	Path string `json:"path"`
}

// PlanGuardConfigurationReads resolves the same contract as materialization,
// without reading Apollo, modifying sources or granting network access.
func PlanGuardConfigurationReads(workspace *WorkspaceData, environment string, selected []string) ([]GuardConfigurationRequest, error) {
	if workspace == nil || workspace.Manifest == nil || len(selected) == 0 { return nil, fmt.Errorf("workspace and selected services are required") }
	if err := config.ValidateSelection(workspace.Manifest, selected); err != nil { return nil, err }
	env, ok := workspace.Manifest.Environments[environment]; if !ok { return nil, fmt.Errorf("environment is not declared") }
	values, err := config.LoadEnvironmentValues(workspace.Root, env.Env, env.EnvFile); if err != nil { return nil, err }
	resolutions, err := dependency.Resolve(workspace.Manifest, environment, selected, values); if err != nil { return nil, err }
	plan := &Plan{Workspace: workspace, EnvironmentName: environment, Environment: env, RunDir: "/work/.conven/runtime/current", Resolutions: resolutions}
	selection := map[string]bool{}; for _, name := range selected { selection[name] = true }
	segment := regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]{0,199}$`)
	requests := []GuardConfigurationRequest{}
	for _, name := range selected {
		service := workspace.Manifest.Services[name]; directory := service.Path
		if !filepath.IsAbs(directory) { directory = filepath.Join(workspace.Root, directory) }
		expand := config.ExpandContext{Workspace: "/work", Service: name, ServiceDir: directory, StateDir: "/work/.conven/runtime", RunDir: plan.RunDir, ConfigDir: filepath.Join(plan.RunDir, "configs", name), Artifact: filepath.Join(plan.RunDir, "artifacts", name), Environment: environment, Manifest: workspace.Manifest}
		planned, err := planServiceConfig(plan, name, service, directory, expand, selection); if err != nil { return nil, err }
		if planned == nil || planned.Plan.SourceDriver != materialize.SourceApollo { return nil, fmt.Errorf("service %s: unsupported protected configuration source", name) }
		data, err := os.ReadFile(filepath.Join(planned.Plan.SourceDir, planned.Plan.Bootstrap)); if err != nil { return nil, fmt.Errorf("service %s: bootstrap unavailable", name) }
		var identity struct { AppID string `yaml:"appId"`; Cluster string `yaml:"cluster"`; Namespace string `yaml:"namespaceName"`; IP string `yaml:"ip"` }
		if yaml.Unmarshal(data, &identity) != nil { return nil, fmt.Errorf("service %s: invalid bootstrap", name) }
		for _, value := range []string{identity.AppID, identity.Cluster, identity.Namespace} { if !segment.MatchString(value) { return nil, fmt.Errorf("service %s: invalid Apollo identity", name) } }
		authority := identity.IP; if !strings.Contains(authority, "://") { authority = "http://" + authority }
		u, err := url.Parse(authority)
		if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" { return nil, fmt.Errorf("service %s: unsupported Apollo endpoint; review required", name) }
		requests = append(requests, GuardConfigurationRequest{Service: name, Authority: u.Host, AppID: identity.AppID, Cluster: identity.Cluster, Namespace: identity.Namespace, Path: "/configs/" + identity.AppID + "/" + identity.Cluster + "/" + identity.Namespace + "?ip=127.0.0.1"})
	}
	return requests, nil
}
