package runtime

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/leo1394/homebrew-conven/internal/config"
	"github.com/leo1394/homebrew-conven/internal/model"
)

type WorkspaceConfigurationError struct { Code string; Err error }
func (e *WorkspaceConfigurationError) Error() string { return e.Err.Error() }

type ConfiguredConnection struct {
	Kubeconfig string `json:"kubeconfig"`
	Context string `json:"context"`
	Namespace string `json:"namespace"`
}
type EnvironmentConfigurationView struct {
	Name string `json:"name"`
	Configured ConfiguredConnection `json:"configured"`
	Effective map[string]string `json:"effective"`
	Error string `json:"error,omitempty"`
}
type ConfigurationDependency struct { Binding string `json:"binding"`; Provider string `json:"provider"` }
type ServiceConfigurationView struct {
	Name string `json:"name"`
	Kinds []string `json:"kinds"`
	Listen string `json:"listen"`
	ListenEditable bool `json:"listenEditable"`
	ListenReason string `json:"listenReason,omitempty"`
	Dependencies []ConfigurationDependency `json:"dependencies"`
}
type BindingConfigurationView struct {
	Name string `json:"name"`
	Consumers []string `json:"consumers"`
	Providers []string `json:"providers"`
	Disabled bool `json:"disabled"`
	Capability string `json:"capability"`
	Reason string `json:"reason,omitempty"`
}
type ConfigurationSession struct {
	Environment string `json:"environment"`
	Services []string `json:"services"`
	Connection map[string]string `json:"connection,omitempty"`
}
type WorkspaceConfiguration struct {
	ConnectionByEnvironment bool `json:"connectionByEnvironment"`
	Revision string `json:"revision"`
	Workspace string `json:"workspace"`
	ManifestPath string `json:"manifestPath"`
	Environments []EnvironmentConfigurationView `json:"environments"`
	Services []ServiceConfigurationView `json:"services"`
	Bindings []BindingConfigurationView `json:"bindings"`
	DisabledBindings []string `json:"disabledBindings"`
	Session *ConfigurationSession `json:"session"`
}
type WorkspaceConfigurationSave struct {
	ConnectionByEnvironment *bool `json:"connectionByEnvironment"`
	Revision string `json:"revision"`
	Environments []config.EnvironmentConfiguration `json:"environments"`
	Services []config.ServiceConfiguration `json:"services"`
	DisabledBindings []string `json:"disabledBindings"`
}

func configurationFailure(code string, err error) error { return &WorkspaceConfigurationError{Code:code, Err:err} }

func ReadWorkspaceConfiguration(root string) (*WorkspaceConfiguration, error) {
	value, _, err := readWorkspaceConfiguration(root)
	return value, err
}

func readWorkspaceConfiguration(root string) (*WorkspaceConfiguration, string, error) {
	workspace, err := OpenWorkspace(CommonOptions{Cwd:root})
	if err != nil { return nil, "", configurationFailure("workspace", err) }
	manifest, manifestRevision, err := config.ReadConfigurationManifest(workspace.ConfigPath)
	if err != nil { return nil, "", configurationFailure("workspace", err) }
	workspace.Manifest = manifest
	value := &WorkspaceConfiguration{Workspace:workspace.Root, ManifestPath:workspace.ConfigPath, Environments:[]EnvironmentConfigurationView{}, Services:[]ServiceConfigurationView{}, Bindings:[]BindingConfigurationView{}, DisabledBindings:append([]string{}, manifest.Workspace.DisabledBindings...)}
	value.ConnectionByEnvironment = manifest.Workspace.ConnectionByEnvironment
	names := []string{}
	for name := range manifest.Environments { names = append(names, name) }; sort.Strings(names)
	for _, name := range names {
		connection := manifest.Environments[name].Connection
		effective, previewErr := ConnectionPreview(workspace, name)
		entry := EnvironmentConfigurationView{Name:name, Configured:ConfiguredConnection{Kubeconfig:connection.Kubeconfig, Context:connection.Context, Namespace:connection.Namespace}, Effective:map[string]string{"kubeconfig":"", "context":"", "namespace":"", "source":"", "driver":connection.Driver}}
		if previewErr != nil { entry.Error = RedactDiagnosticText(previewErr.Error()) } else { for _, key := range []string{"kubeconfig", "context", "namespace", "source", "driver"} { entry.Effective[key] = effective[key] } }
		value.Environments = append(value.Environments, entry)
	}
	bindings := map[string]*BindingConfigurationView{}
	getBinding := func(name string) *BindingConfigurationView {
		if bindings[name] == nil { bindings[name] = &BindingConfigurationView{Name:name, Consumers:[]string{}, Providers:[]string{}, Capability:"not-verified", Reason:"consumer disable support has not been verified"} }
		return bindings[name]
	}
	for _, name := range value.DisabledBindings { getBinding(name).Disabled = true }
	for _, name := range config.ServiceNames(manifest) {
		service := manifest.Services[name]
		listen := service.Network.Listen; if listen == "" { listen = model.NetworkListenLoopback }
		reason := config.ListenerConfigurationReason(manifest, service)
		if listen != model.NetworkListenLoopback && listen != model.NetworkListenAllInterfaces { reason = "existing listener scope is not editable" }
		kinds := append([]string{}, service.EffectiveKinds()...)
		entry := ServiceConfigurationView{Name:name, Kinds:kinds, Listen:listen, ListenEditable:reason == "", ListenReason:reason, Dependencies:[]ConfigurationDependency{}}
		consumed := map[string]bool{}
		for _, binding := range service.Discovery.EffectiveConsumerBindings() { consumed[binding] = true }
		aliases := []string{}; for alias := range service.Dependencies { aliases = append(aliases, alias) }; sort.Strings(aliases)
		for _, alias := range aliases {
			dependency := service.Dependencies[alias]; binding := dependency.Binding; if binding == "" { binding = alias }
			provider := dependency.LocalService; if provider == "" { provider = model.ProviderService(manifest, alias, binding) }
			entry.Dependencies = append(entry.Dependencies, ConfigurationDependency{Binding:binding, Provider:provider})
			consumed[binding] = true
			if provider != "" { b := getBinding(binding); b.Providers = appendUniqueConfiguration(b.Providers, provider) }
		}
		for binding := range consumed {
			b := getBinding(binding); b.Consumers = appendUniqueConfiguration(b.Consumers, name)
			if provider := model.ProviderService(manifest, "", binding); provider != "" { b.Providers = appendUniqueConfiguration(b.Providers, provider) }
		}
		value.Services = append(value.Services, entry)
	}
	names = []string{}; for name := range bindings { names = append(names, name) }; sort.Strings(names)
	for _, name := range names { binding := bindings[name]; sort.Strings(binding.Consumers); sort.Strings(binding.Providers); value.Bindings = append(value.Bindings, *binding) }
	// Revision includes bytes from both settings scopes and all inherited expansion
	// inputs, even when a higher-priority override masks one of those settings.
	hash := sha256.New()
	json.NewEncoder(hash).Encode(manifestRevision)
	settingsPaths := []string{config.LocalSettingsPath(workspace.Root)}
	global, err := config.GlobalSettingsPath(""); if err != nil { return nil, "", err }; settingsPaths = append(settingsPaths, global)
	for _, path := range settingsPaths {
		data, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) { return nil, "", configurationFailure("workspace", err) }
		json.NewEncoder(hash).Encode([]string{path, string(data), fmt.Sprint(os.IsNotExist(err))})
	}
	inherited := os.Environ(); sort.Strings(inherited)
	json.NewEncoder(hash).Encode(inherited)
	json.NewEncoder(hash).Encode(workspace.Settings)
	json.NewEncoder(hash).Encode(value.Environments)
	value.Revision = fmt.Sprintf("%x", hash.Sum(nil))
	session, err := workspace.Store.Load()
	if err != nil { return nil, "", configurationFailure("session", fmt.Errorf("cannot inspect workspace session; resolve it before editing configuration")) }
	if session != nil && len(session.Services) > 0 {
		selected := []string{}; for _, service := range session.Services { selected = append(selected, service.Name) }; sort.Strings(selected)
		value.Session = &ConfigurationSession{Environment:session.Environment, Services:selected}
		if c := session.Connection; c != nil { value.Session.Connection = map[string]string{"kubeconfig":c.SourceKubeconfig, "context":c.Context, "namespace":c.Namespace, "source":c.KubeconfigSource, "driver":c.Driver} }
	}
	return value, manifestRevision, nil
}

func appendUniqueConfiguration(values []string, value string) []string { for _, existing := range values { if value == existing { return values } }; return append(values, value) }

func SaveWorkspaceConfiguration(root string, request WorkspaceConfigurationSave) (*WorkspaceConfiguration, error) {
	if request.ConnectionByEnvironment == nil { return nil, configurationFailure("validation", fmt.Errorf("connectionByEnvironment must be explicitly true or false")) }
	current, manifestRevision, err := readWorkspaceConfiguration(root)
	if err != nil { return nil, err }
	if request.Revision == "" || request.Revision != current.Revision { return nil, configurationFailure("conflict", fmt.Errorf("configuration changed; reload before saving")) }
	if request.Environments == nil || request.Services == nil || request.DisabledBindings == nil { return nil, configurationFailure("validation", fmt.Errorf("complete environment, service and disabled binding lists are required")) }
	err = config.SaveWorkspaceConfiguration(current.ManifestPath, manifestRevision, *request.ConnectionByEnvironment, request.Environments, request.Services, request.DisabledBindings, func() error {
		latest, _, err := readWorkspaceConfiguration(root)
		if err != nil { return err }
		if latest.Revision != request.Revision { return configurationFailure("conflict", fmt.Errorf("configuration changed; reload before saving")) }
		return nil
	})
	if err != nil { if _, ok := err.(*WorkspaceConfigurationError); ok { return nil, err }; if errors.Is(err, config.ErrWorkspaceConfigurationConflict) { return nil, configurationFailure("conflict", err) }; return nil, configurationFailure("validation", err) }
	return ReadWorkspaceConfiguration(root)
}
