package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/model"
	"gopkg.in/yaml.v3"
)

// Only a successfully read application mapping can replace dependency facts.
func synchronizeApplicationDependencies(manifest *model.Manifest, workspace string, scanned []DiscoveredService) ([]string, []string, error) {
	changed, notes := []string{}, []string{}
	environments := make(map[string]model.Environment, len(manifest.Environments))
	if manifest.Environments == nil { environments = nil }
	for name, environment := range manifest.Environments {
		resolutions := make(map[string]map[string]model.DependencyResolution)
		if environment.Resolutions == nil { resolutions = nil }
		for owner, entries := range environment.Resolutions {
			resolutions[owner] = make(map[string]model.DependencyResolution)
			for alias, entry := range entries { resolutions[owner][alias] = entry }
		}
		environment.Resolutions = resolutions
		environments[name] = environment
	}
	manifest.Environments = environments
	for _, facts := range scanned {
		name := ""
		for _, candidate := range ServiceNames(manifest) {
			if servicePathMatchesRepository(workspace, manifest.Services[candidate].Path, filepath.Join(workspace, facts.Path)) { name = candidate; break }
		}
		if name == "" { continue }
		service := manifest.Services[name]
		policyName := service.Policy
		if policyName == "" { policyName = manifest.Workspace.Policy }
		policy := manifest.Policies[policyName]
		if policy.Drivers.Materializer != "yaml-overlay" || policy.Config.Application == "" {
			notes = append(notes, name+": dependency configuration preserved; no YAML application source configured")
			continue
		}
		path := filepath.Join(workspace, service.Path, policy.Config.SourceDir, policy.Config.Application)
		file, err := os.Open(path)
		if os.IsNotExist(err) {
			notes = append(notes, name+": dependency configuration preserved; application file missing: "+path)
			continue
		}
		if err != nil { return nil, nil, err }
		var application map[string]interface{}
		decoder := yaml.NewDecoder(file)
		err = decoder.Decode(&application)
		if err == nil {
			var extra interface{}
			if tail := decoder.Decode(&extra); tail != io.EOF { err = fmt.Errorf("expected one YAML document") }
		}
		file.Close()
		if err != nil { return nil, nil, fmt.Errorf("service %s dependency update: invalid application YAML %s", name, path) }
		if application == nil { return nil, nil, fmt.Errorf("service %s dependency update: %s must contain an application mapping", name, path) }
		known := make(map[string]bool)
		for _, binding := range append(facts.Bindings, service.Discovery.EffectiveConsumerBindings()...) { known[binding] = true }
		for _, dependency := range service.Dependencies { known[dependency.Binding] = true }
		active := make(map[string]map[string]interface{})
		for key, value := range application {
			entry, mapping := value.(map[string]interface{})
			if known[key] || (mapping && strings.HasSuffix(key, "Rpc")) {
				if value == nil { continue }
				if !mapping { return nil, nil, fmt.Errorf("service %s binding %s in %s must be a mapping", name, key, path) }
				active[key] = entry
			}
		}
		updated := service
		updated.Discovery.ConsumerBindings = []string{}
		updated.Discovery.Bindings = nil
		for binding := range active { updated.Discovery.ConsumerBindings = append(updated.Discovery.ConsumerBindings, binding) }
		sort.Strings(updated.Discovery.ConsumerBindings)
		updated.Dependencies = make(map[string]model.Dependency)
		bound := make(map[string]bool)
		removed := make(map[string]bool)
		for alias, dependency := range service.Dependencies {
			if dependency.Binding != "" {
				if _, exists := active[dependency.Binding]; !exists { removed[alias] = true; continue }
				identity := ""
				if consul, ok := active[dependency.Binding]["consul"].(map[string]interface{}); ok { identity, _ = consul["key"].(string) }
				provider := dependency.LocalService
				if provider == "" { provider = alias }
				if identity != "" && manifest.Services[provider].Discovery.Identity != identity { removed[alias] = true; continue }
				bound[dependency.Binding] = true
			}
			updated.Dependencies[alias] = dependency
		}
		for _, binding := range updated.Discovery.ConsumerBindings {
			if bound[binding] { continue }
			identity := ""
			if consul, ok := active[binding]["consul"].(map[string]interface{}); ok { identity, _ = consul["key"].(string) }
			providers := []string{}
			for _, provider := range ServiceNames(manifest) {
				entry := manifest.Services[provider]
				matches := identity != "" && entry.Discovery.Identity == identity
				if identity == "" { for _, alias := range entry.Discovery.ProviderAliases { if alias == binding { matches = true } } }
				if matches { providers = append(providers, provider) }
			}
			if len(providers) > 1 { return nil, nil, fmt.Errorf("service %s binding %s matches multiple providers: %s", name, binding, strings.Join(providers, ", ")) }
			if len(providers) == 0 {
				notes = append(notes, fmt.Sprintf("service %s binding %s in %s: no workspace provider matched consul.key=%q; remote binding preserved, local dependency not created. Ensure the provider repository is in this workspace and its discovery.identity matches the service registration key", name, binding, path, identity))
				continue
			}
			provider := providers[0]
			port := "rpc"
			if manifest.Services[provider].Ports[port] == 0 { return nil, nil, fmt.Errorf("service %s binding %s provider %s has no rpc port", name, binding, provider) }
			alias := provider
			if _, exists := updated.Dependencies[alias]; exists { alias = binding }
			if _, exists := updated.Dependencies[alias]; exists { return nil, nil, fmt.Errorf("service %s binding %s dependency alias conflicts", name, binding) }
			updated.Dependencies[alias] = model.Dependency{LocalService: provider, Binding: binding, Port: port}
		}
		routesChanged := false
		for envName, environment := range manifest.Environments {
			entries := environment.Resolutions[name]
			for alias := range removed { if _, exists := entries[alias]; exists { delete(entries, alias); routesChanged = true } }
			if envName != "local" {
				for alias, dependency := range updated.Dependencies {
					if dependency.Binding == "" { continue }
					if _, exists := entries[alias]; !exists {
						if entries == nil { entries = make(map[string]model.DependencyResolution) }
						entries[alias] = model.DependencyResolution{Mode: "remote"}
						routesChanged = true
					}
				}
			}
			if entries != nil {
				if environment.Resolutions == nil { environment.Resolutions = make(map[string]map[string]model.DependencyResolution) }
				environment.Resolutions[name] = entries
				manifest.Environments[envName] = environment
			}
		}
		if !reflect.DeepEqual(service, updated) || routesChanged {
			manifest.Services[name] = updated
			changed = append(changed, name)
		}
	}
	return changed, notes, nil
}
