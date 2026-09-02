package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"github.com/leo1394/homebrew-conven/internal/model"
	"gopkg.in/yaml.v3"
)

type ManifestMigrationResult struct {
	Path       string
	BackupPath string
	Changed    bool
	From       int
	To         int
}

func MigrateWorkspaceManifest(cwd string) (ManifestMigrationResult, error) {
	result := ManifestMigrationResult{To: 3}
	workspace, boundary, err := policyWorkspace(cwd)
	if err != nil {
		return result, err
	}
	path := ManifestPath(workspace)
	result.Path = path
	data, _, err := readManifestForUpdate(path)
	if err != nil {
		return result, err
	}
	manifest, err := decodeManifest(data, path)
	if err != nil {
		return result, err
	}
	result.From = manifest.Version
	if manifest.Version == 3 {
		return result, nil
	}
	if err := rejectActiveMigrationSession(boundary); err != nil {
		return result, err
	}
	if err := migrateManifestV3(workspace, manifest); err != nil {
		return result, fmt.Errorf("migrate Conven manifest v%d to v3: %w", result.From, err)
	}
	var candidate bytes.Buffer
	encoder := yaml.NewEncoder(&candidate)
	encoder.SetIndent(2)
	if err := encoder.Encode(manifest); err != nil {
		return result, fmt.Errorf("encode migrated Conven manifest: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return result, fmt.Errorf("close migrated Conven manifest encoder: %w", err)
	}
	applied, err := applyWorkspacePolicyCandidate(workspace, boundary, candidate.Bytes(), nil, policyCandidateOptions{
		validationName: "migrated Conven manifest v3",
		candidateName:  "migrated Conven manifest",
		operation:      "workspace migration",
		draftPattern:   "conven.yaml-migrate-*.yaml",
		backupPattern:  "conven.yaml-before-v3-migration-*.bak",
		backupLabel:    "migration",
	})
	if err != nil {
		return result, err
	}
	result.BackupPath = applied.BackupPath
	result.Changed = applied.Changed
	return result, nil
}

func migrateManifestV3(workspace string, manifest *model.Manifest) error {
	from := manifest.Version
	manifest.Version = 3
	for name, policy := range manifest.Policies {
		if policy.Drivers.Runtime == "" {
			switch policy.Drivers.Framework {
			case "go-zero":
				policy.Drivers.Runtime = "go-zero"
			case "spring-boot":
				policy.Drivers.Runtime = "spring-boot"
			default:
				policy.Drivers.Runtime = "generic-runner"
			}
		}
		manifest.Policies[name] = policy
	}
	if from == 2 && len(manifest.Services) > 0 && len(manifest.Policies) == 0 {
		manifest.Policies = map[string]model.Policy{
			"generic-runner": {
				Drivers: model.PolicyDrivers{Runtime: "generic-runner", Framework: "generic", ConfigSource: "environment", Discovery: "passive", Materializer: "environment"},
			},
		}
	}
	for environmentName, environment := range manifest.Environments {
		if environment.Registries == nil {
			environment.Registries = make(map[string]model.Registry)
		}
		if environment.Registry != "" {
			address := readinessAddress(environment, environment.Registry)
			if address != "" && !strings.Contains(address, "://") {
				address = "http://" + address
			}
			if address != "" {
				environment.Registries[environment.Registry] = model.Registry{Driver: environment.Registry, Address: address, ObserveFor: "5s"}
			}
			environment.Registry = ""
		}
		if from == 1 {
			if environment.Resolutions == nil {
				environment.Resolutions = make(map[string]map[string]model.DependencyResolution)
			}
			for owner, service := range manifest.Services {
				if len(service.Dependencies) == 0 {
					continue
				}
				if environment.Resolutions[owner] == nil {
					environment.Resolutions[owner] = make(map[string]model.DependencyResolution)
				}
				for alias := range service.Dependencies {
					if _, found := environment.Resolutions[owner][alias]; !found {
						environment.Resolutions[owner][alias] = model.DependencyResolution{Mode: "remote"}
					}
				}
			}
		}
		manifest.Environments[environmentName] = environment
	}
	providerAliases := migratedProviderAliases(manifest)
	for _, name := range ServiceNames(manifest) {
		service := manifest.Services[name]
		if service.Policy == "" && manifest.Workspace.Policy == "" && from == 2 {
			service.Policy = "generic-runner"
		}
		if service.Kind != "" {
			service.Kinds = []string{service.Kind}
			service.Kind = ""
		}
		checks := service.EffectiveHealthChecks()
		if len(service.Kinds) == 1 {
			for index := range checks {
				if checks[index].Server == "" {
					checks[index].Server = service.Kinds[0]
				}
			}
		}
		if len(checks) == 0 {
			for _, kind := range service.Kinds {
				port := service.Ports[kind]
				if port > 0 {
					checks = append(checks, model.ServiceHealthCheck{Server: kind, Type: "tcp", Address: "127.0.0.1:${port." + kind + "}"})
				}
			}
		}
		service.HealthChecks = checks
		service.Health = model.Health{}
		service.Discovery.ConsumerBindings = service.Discovery.EffectiveConsumerBindings()
		service.Discovery.Bindings = nil
		service.Discovery.ProviderAliases = append([]string(nil), providerAliases[name]...)
		if service.Discovery.Certifier == "" && service.Discovery.Analyzer != "" {
			service.Discovery.Certifier = migratedCertifier(service, manifest)
		}
		policyName := service.Policy
		if policyName == "" {
			policyName = manifest.Workspace.Policy
		}
		policy, hasPolicy := manifest.Policies[policyName]
		if from == 2 && hasPolicy && policy.Drivers.Runtime == "generic-runner" {
			service.Kinds = nil
			for index := range service.HealthChecks {
				service.HealthChecks[index].Server = ""
			}
			service.Network = model.ServiceNetwork{}
			service.Discovery.Certifier = "manual"
		}
		if hasPolicy && serviceNeedsRegistry(service, policy) {
			service.Discovery.Registry = policy.Drivers.Discovery
			if service.Discovery.Identity == "" {
				identity, err := inferRegistryIdentity(workspace, service)
				if err != nil {
					return fmt.Errorf("services.%s.discovery.identity: %w", name, err)
				}
				if identity == "" {
					return fmt.Errorf("services.%s discovery identity could not be proven; add discovery.identity with the exact %s service name before retrying", name, policy.Drivers.Discovery)
				}
				service.Discovery.Identity = identity
			}
			for environmentName, environment := range manifest.Environments {
				if _, found := environment.Registries[service.Discovery.Registry]; !found {
					return fmt.Errorf("environment %s has no %s registry address; add environments.%s.registries.%s.address before retrying", environmentName, service.Discovery.Registry, environmentName, service.Discovery.Registry)
				}
			}
		}
		manifest.Services[name] = service
	}
	return validateManifest(manifest)
}

func readinessAddress(environment model.Environment, driver string) string {
	for _, endpoint := range environment.Connection.Readiness {
		if strings.EqualFold(endpoint.Name, driver) || strings.Contains(strings.ToLower(endpoint.Name), strings.ToLower(driver)) {
			return endpoint.Address
		}
	}
	return ""
}

func migratedProviderAliases(manifest *model.Manifest) map[string][]string {
	aliases := make(map[string][]string)
	seen := make(map[string]map[string]bool)
	for _, owner := range ServiceNames(manifest) {
		service := manifest.Services[owner]
		for alias, dependency := range service.Dependencies {
			target := dependency.LocalService
			if target == "" {
				target = alias
			}
			if _, found := manifest.Services[target]; !found || dependency.Binding == "" {
				continue
			}
			if seen[target] == nil {
				seen[target] = make(map[string]bool)
			}
			if !seen[target][dependency.Binding] {
				seen[target][dependency.Binding] = true
				aliases[target] = append(aliases[target], dependency.Binding)
			}
		}
	}
	for name := range aliases {
		sort.Strings(aliases[name])
	}
	return aliases
}

func migratedCertifier(service model.Service, manifest *model.Manifest) string {
	policyName := service.Policy
	if policyName == "" {
		policyName = manifest.Workspace.Policy
	}
	policy := manifest.Policies[policyName]
	if policy.Drivers.Runtime == "generic-runner" || policy.Drivers.Runtime == "" {
		return "manual"
	}
	return policy.Drivers.Runtime
}

func serviceNeedsRegistry(service model.Service, policy model.Policy) bool {
	if policy.Drivers.Discovery == "" || policy.Drivers.Discovery == "passive" || policy.Drivers.Discovery == "kubernetes-dns" {
		return false
	}
	for _, kind := range service.EffectiveKinds() {
		server, found := policy.Routing.Servers[kind]
		if found && server.Isolation.Registration.Mode != "not-applicable" {
			return true
		}
	}
	return false
}

func inferRegistryIdentity(workspace string, service model.Service) (string, error) {
	directory := service.Path
	if !filepath.IsAbs(directory) {
		directory = filepath.Join(workspace, directory)
	}
	files := []string{
		"resources/application.yaml",
		"resources/application.yml",
		"resources/config-local.yaml",
		"resources/config-dev.yaml",
		"resources/config-test.yaml",
		"src/main/resources/application.yml",
		"src/main/resources/application.yaml",
	}
	identities := make(map[string]string)
	for _, relative := range files {
		path := filepath.Join(directory, relative)
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("read %s: %w", path, err)
		}
		var document yaml.Node
		if err := yaml.Unmarshal(data, &document); err != nil {
			return "", fmt.Errorf("decode %s: %w", path, err)
		}
		for _, keys := range [][]string{{"consul", "key"}, {"spring", "application", "name"}} {
			if value := yamlScalarAt(&document, keys...); value != "" {
				identities[value] = path
			}
		}
	}
	if len(identities) > 1 {
		values := make([]string, 0, len(identities))
		for identity := range identities { values = append(values, identity) }
		sort.Strings(values)
		return "", fmt.Errorf("repository config declares conflicting registry identities: %s", strings.Join(values, ", "))
	}
	for identity := range identities { return identity, nil }
	return "", nil
}

func yamlScalarAt(document *yaml.Node, keys ...string) string {
	if document == nil || len(keys) == 0 {
		return ""
	}
	node := document
	if node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
		node = node.Content[0]
	}
	for _, key := range keys {
		if node.Kind != yaml.MappingNode {
			return ""
		}
		var next *yaml.Node
		for index := 0; index+1 < len(node.Content); index += 2 {
			if node.Content[index].Value == key {
				next = node.Content[index+1]
				break
			}
		}
		if next == nil {
			return ""
		}
		node = next
	}
	if node.Kind != yaml.ScalarNode {
		return ""
	}
	return strings.TrimSpace(node.Value)
}

func springNacosImportIdentity(directory string) string {
	entries, err := os.ReadDir(directory)
	if err != nil { return "" }
	pattern := regexp.MustCompile(`(?:optional:)?nacos:([A-Za-z0-9._-]+)`)
	identities := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yml" && filepath.Ext(entry.Name()) != ".yaml" { continue }
		data, err := os.ReadFile(filepath.Join(directory, entry.Name())); if err != nil { continue }
		for _, match := range pattern.FindAllStringSubmatch(string(data), -1) { identities[match[1]] = true }
	}
	if len(identities) != 1 { return "" }
	for identity := range identities { return identity }
	return ""
}

func rejectActiveMigrationSession(boundary string) error {
	path := filepath.Join(boundary, "runtime", "session.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect runtime session before migration: %w", err)
	}
	var session struct {
		Services []struct {
			Name string `json:"name"`
			PID  int    `json:"pid"`
		} `json:"services"`
	}
	if err := json.Unmarshal(data, &session); err != nil {
		return fmt.Errorf("decode runtime session before migration: %w", err)
	}
	active := make([]string, 0)
	for _, service := range session.Services {
		if service.PID < 1 {
			continue
		}
		err := syscall.Kill(service.PID, 0)
		if err == nil || errors.Is(err, syscall.EPERM) {
			active = append(active, service.Name)
		}
	}
	if len(active) > 0 {
		sort.Strings(active)
		return fmt.Errorf("workspace migration requires stopped services (%s); run conven services --stop-all first", strings.Join(active, ", "))
	}
	return nil
}
