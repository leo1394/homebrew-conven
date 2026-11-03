package config

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/model"
	"gopkg.in/yaml.v3"
)

var ErrWorkspaceConfigurationConflict = errors.New("configuration changed; reload before saving")

type EnvironmentConfiguration struct {
	Name string `json:"name"`
	Kubeconfig string `json:"kubeconfig"`
	Context string `json:"context"`
	Namespace string `json:"namespace"`
}

type ServiceConfiguration struct {
	Name string `json:"name"`
	Listen string `json:"listen"`
}

// ReadConfigurationManifest refuses symlinks, just like manifest updates.
func ReadConfigurationManifest(path string) (*model.Manifest, string, error) {
	data, _, err := readManifestForUpdate(path)
	if err != nil { return nil, "", err }
	manifest, err := decodeManifest(data, path)
	return manifest, fmt.Sprintf("%x", sha256.Sum256(data)), err
}

// ListenerConfigurationReason reports whether both supported listener choices
// are available. It makes no claim about runtime reachability.
func ListenerConfigurationReason(manifest *model.Manifest, service model.Service) string {
	kinds := service.EffectiveKinds()
	if len(kinds) == 0 { return "service must declare http or rpc kinds" }
	policyName := service.Policy
	if policyName == "" { policyName = manifest.Workspace.Policy }
	policy, found := manifest.Policies[policyName]
	if policyName == "" || !found { return "service has no policy-backed listener adapter" }
	for _, kind := range kinds {
		if kind != "http" && kind != "rpc" { return "unsupported listener kind: " + kind }
		if _, found := policy.Routing.Servers[kind]; !found { return "policy has no " + kind + " listener adapter" }
	}
	return ""
}

// SaveWorkspaceConfiguration validates a full replacement of the editable
// fields, then uses the existing no-follow, locked atomic manifest publisher.
func SaveWorkspaceConfiguration(path, manifestRevision string, connectionByEnvironment bool, environments []EnvironmentConfiguration, services []ServiceConfiguration, disabled []string, beforeCommit func() error) error {
	source, info, err := readManifestForUpdate(path)
	if err != nil { return err }
	if fmt.Sprintf("%x", sha256.Sum256(source)) != manifestRevision { return ErrWorkspaceConfigurationConflict }
	manifest, err := decodeManifest(source, path)
	if err != nil { return err }
	if len(environments) != len(manifest.Environments) || len(services) != len(manifest.Services) { return fmt.Errorf("complete environment and service lists are required") }
	document, serviceNodes, err := loadManifestDocument(source, path)
	if err != nil { return err }
	root := document.Content[0]
	environmentNodes := mappingValue(root, "environments")
	seen := map[string]bool{}
	for _, edit := range environments {
		env, found := manifest.Environments[edit.Name]
		if !found || seen[edit.Name] { return fmt.Errorf("unknown or duplicate environment %q", edit.Name) }
		seen[edit.Name] = true
		for _, value := range []string{edit.Kubeconfig, edit.Context, edit.Namespace} {
			if strings.ContainsAny(value, "\x00\r\n") { return fmt.Errorf("environment %s contains invalid connection characters", edit.Name) }
		}
		if edit.Kubeconfig != "" { if _, err := normalizeKubeconfig(edit.Kubeconfig, "connection.kubeconfig"); err != nil { return err } }
		node := mappingValue(environmentNodes, edit.Name)
		if node == nil || node.Kind != yaml.MappingNode { return fmt.Errorf("environment %s must be a mapping", edit.Name) }
		connection := mappingValue(node, "connection")
		if connection == nil { connection = &yaml.Node{Kind:yaml.MappingNode, Tag:"!!map"}; setMappingValue(node, "connection", connection) }
		if connection.Kind != yaml.MappingNode { return fmt.Errorf("environment %s connection must be a mapping", edit.Name) }
		for key, value := range map[string]string{"kubeconfig":edit.Kubeconfig, "context":edit.Context, "namespace":edit.Namespace} {
			if value == "" { removeMappingEntries(connection, []string{key}) } else { setMappingValue(connection, key, &yaml.Node{Kind:yaml.ScalarNode, Tag:"!!str", Value:value}) }
		}
		env.Connection.Kubeconfig, env.Connection.Context, env.Connection.Namespace = edit.Kubeconfig, edit.Context, edit.Namespace
		manifest.Environments[edit.Name] = env
	}
	seen = map[string]bool{}
	for _, edit := range services {
		service, found := manifest.Services[edit.Name]
		if !found || seen[edit.Name] { return fmt.Errorf("unknown or duplicate service %q", edit.Name) }
		seen[edit.Name] = true
		current := service.Network.Listen
		if current == "" { current = model.NetworkListenLoopback }
		if current == edit.Listen { continue }
		if edit.Listen != model.NetworkListenLoopback && edit.Listen != model.NetworkListenAllInterfaces { return fmt.Errorf("unsupported listener scope %q", edit.Listen) }
		if edit.Listen == model.NetworkListenAllInterfaces { if reason := ListenerConfigurationReason(manifest, service); reason != "" { return fmt.Errorf("service %s: %s", edit.Name, reason) } }
		node := mappingValue(serviceNodes, edit.Name)
		if node == nil || node.Kind != yaml.MappingNode { return fmt.Errorf("service %s must be a mapping", edit.Name) }
		if edit.Listen == model.NetworkListenLoopback { removeMappingEntries(node, []string{"network"}); service.Network = model.ServiceNetwork{} } else { setServiceListenerNode(node, edit.Listen); service.Network.Listen = edit.Listen }
		manifest.Services[edit.Name] = service
	}
	known := map[string]bool{}
	for _, name := range manifest.Workspace.DisabledBindings { known[name] = true }
	for _, service := range manifest.Services {
		for _, name := range service.Discovery.EffectiveConsumerBindings() { known[name] = true }
		for alias, dependency := range service.Dependencies { name := dependency.Binding; if name == "" { name = alias }; known[name] = true }
	}
	seen = map[string]bool{}
	for _, name := range disabled { if !validBindingName(name) || !known[name] || seen[name] { return fmt.Errorf("unknown, invalid or duplicate binding %q", name) }; seen[name] = true }
	manifest.Workspace.DisabledBindings = append([]string{}, disabled...)
	workspace := mappingValue(root, "workspace")
	if workspace == nil || workspace.Kind != yaml.MappingNode { return fmt.Errorf("workspace must be a mapping") }
	manifest.Workspace.ConnectionByEnvironment = connectionByEnvironment
	if connectionByEnvironment {
		setMappingValue(workspace, "connectionByEnvironment", &yaml.Node{Kind:yaml.ScalarNode, Tag:"!!bool", Value:"true"})
	} else { removeMappingEntries(workspace, []string{"connectionByEnvironment"}) }
	sequence := &yaml.Node{Kind:yaml.SequenceNode, Tag:"!!seq"}
	for _, name := range disabled { sequence.Content = append(sequence.Content, &yaml.Node{Kind:yaml.ScalarNode, Tag:"!!str", Value:name}) }
	setMappingValue(workspace, "disabledBindings", sequence)
	if err := validateManifest(manifest); err != nil { return err }
	if beforeCommit != nil { if err := beforeCommit(); err != nil { return err } }
	if err := saveManifestDocumentForOperation(path, document, source, info, manifest, "workspace configuration update"); err != nil {
		if snapshotErr := verifyManifestSnapshot(path, source, info, "workspace configuration update"); snapshotErr != nil { return fmt.Errorf("%w: %v", ErrWorkspaceConfigurationConflict, snapshotErr) }
		return err
	}
	return nil
}
