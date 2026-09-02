package config

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/model"
	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

type DiscoveredService struct {
	Name            string
	Path            string
	Analyzer        string
	Framework       string
	Runtime         string
	DiscoveryDriver string
	Policy          string
	Certifier       string
	Kind            string
	Kinds           []string
	Bindings        []string
	Runner          model.Runner
	Ports           map[string]int
	Health          model.Health
	HealthChecks    []model.ServiceHealthCheck
	Registry        string
	Identity        string
	Registrations   []RepositoryRegistrationEvidence
	Consumers       []RepositoryConsumerEvidence
}

const firstDiscoveredLocalPort = 18080

type DiscoveryResult struct {
	Discovered []string
	Added      []string
	Existing   []string
	Updated    []string
	Missing    []string
	Pruned     []string
	Skipped    []string
	SkippedDetails []string
	Assigned   []string
}

type discoveredManifest struct {
	Version   int                          `yaml:"version"`
	Workspace discoveredManifestWorkspace `yaml:"workspace"`
	Policies  map[string]model.Policy      `yaml:"policies,omitempty"`
	Services  map[string]discoveredEntry   `yaml:"services"`
}

type discoveredManifestWorkspace struct {
	Name             string   `yaml:"name"`
	DisabledBindings []string `yaml:"disabledBindings"`
}

type discoveredEntry struct {
	Path         string                     `yaml:"path"`
	Policy       string                     `yaml:"policy,omitempty"`
	Kind         string                     `yaml:"kind,omitempty"`
	Kinds        []string                   `yaml:"kinds,omitempty"`
	Discovery    discoveredDiscovery        `yaml:"discovery"`
	Runner       discoveredRunner           `yaml:"runner"`
	Ports        map[string]int             `yaml:"ports,omitempty"`
	Health       discoveredHealth           `yaml:"health,omitempty"`
	HealthChecks []model.ServiceHealthCheck `yaml:"healthChecks,omitempty"`
	Isolation    *model.ServiceIsolation    `yaml:"isolation,omitempty"`
}

type discoveredDiscovery struct {
	Analyzer         string   `yaml:"analyzer"`
	Certifier        string   `yaml:"certifier,omitempty"`
	Registry         string   `yaml:"registry,omitempty"`
	Identity         string   `yaml:"identity,omitempty"`
	ConsumerBindings []string `yaml:"consumerBindings,omitempty"`
	Consumers        []string `yaml:"consumers,omitempty"`
	Bindings         []string `yaml:"bindings,omitempty"`
}

type discoveredRunner struct {
	Workdir  string   `yaml:"workdir"`
	Artifact string   `yaml:"artifact,omitempty"`
	Prepare  []string `yaml:"prepare,omitempty"`
	Build    []string `yaml:"build"`
	Run      []string `yaml:"run"`
}

type discoveredHealth struct {
	Type    string   `yaml:"type,omitempty"`
	Address string   `yaml:"address,omitempty"`
	URL     string   `yaml:"url,omitempty"`
	Command []string `yaml:"command,omitempty"`
	Timeout string   `yaml:"timeout,omitempty"`
}

func ScanServices(workspace string) ([]DiscoveredService, []string, error) {
	workspace, err := resolveCwd(workspace)
	if err != nil {
		return nil, nil, err
	}
	entries, err := os.ReadDir(workspace)
	if err != nil {
		return nil, nil, fmt.Errorf("scan workspace %q: %w", workspace, err)
	}

	discovered := make([]DiscoveredService, 0)
	skipped := make([]string, 0)
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || !entry.IsDir() {
			continue
		}
		repository := filepath.Join(workspace, name)
		isRepository, err := pathExists(filepath.Join(repository, ".git"))
		if err != nil {
			return nil, nil, fmt.Errorf("inspect repository marker for %q: %w", name, err)
		}
		if !isRepository {
			continue
		}
		analysis, supported, err := AnalyzeRepository(repository)
		if err != nil {
			return nil, nil, err
		}
		if !supported {
			skipped = append(skipped, name)
			continue
		}
		kind := analysis.Kind
		if kind == RepositoryKindUnknown {
			kind = ""
		}
		bindings := make([]string, 0, len(analysis.RPCClientBindings))
		seenBindings := make(map[string]bool, len(analysis.RPCClientBindings))
		for _, binding := range analysis.RPCClientBindings {
			if seenBindings[binding.YAMLKey] {
				continue
			}
			seenBindings[binding.YAMLKey] = true
			bindings = append(bindings, binding.YAMLKey)
		}
		sort.Strings(bindings)
		identity := ""
		if analysis.Discovery != "" && analysis.Discovery != "passive" && analysis.Discovery != "kubernetes-dns" {
			identity, err = inferRegistryIdentity(workspace, model.Service{Path: name})
			if err != nil {
				return nil, nil, fmt.Errorf("discover %s registry identity: %w", name, err)
			}
			if identity == "" && analysis.Discovery == "nacos" {
				identity = springNacosImportIdentity(filepath.Join(repository, "src", "main", "resources"))
			}
			if identity == "" && (containsDiscoveredKind(analysis.EffectiveKinds(), RepositoryKindRPC) || len(analysis.Registrations) > 0) {
				return nil, nil, fmt.Errorf("discover %s registry identity: %s provider identity could not be proven; add the framework-native service name to repository configuration, or declare services.%s.discovery.identity and registry in the manifest", name, analysis.Discovery, analysis.ServiceName)
			}
		}
		registry := ""
		if identity != "" {
			registry = analysis.Discovery
		}
		discovered = append(discovered, DiscoveredService{
			Name:            analysis.ServiceName,
			Path:            name,
			Analyzer:        analysis.Analyzer,
			Framework:       analysis.Framework,
			Runtime:         analysis.Runtime,
			DiscoveryDriver: analysis.Discovery,
			Kind:            kind,
			Kinds:           analysis.EffectiveKinds(),
			Bindings:        bindings,
			Runner:          analysis.Runner,
			Health:          analysis.Health,
			HealthChecks:    append([]model.ServiceHealthCheck(nil), analysis.HealthChecks...),
			Registry:        registry,
			Identity:        identity,
			Registrations:   append([]RepositoryRegistrationEvidence(nil), analysis.Registrations...),
			Consumers:       append([]RepositoryConsumerEvidence(nil), analysis.Consumers...),
		})
	}
	sort.Slice(discovered, func(left int, right int) bool {
		return discovered[left].Name < discovered[right].Name
	})
	sort.Strings(skipped)
	return discovered, skipped, nil
}

func containsDiscoveredKind(kinds []string, want string) bool {
	for _, kind := range kinds {
		if kind == want {
			return true
		}
	}
	return false
}

func discoveredConsumerDrivers(evidence []RepositoryConsumerEvidence) []string {
	seen := make(map[string]bool)
	for _, item := range evidence {
		if item.Driver != "" {
			seen[item.Driver] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}
	result := make([]string, 0, len(seen))
	for driver := range seen {
		result = append(result, driver)
	}
	sort.Strings(result)
	return result
}

func discoveredConsumerIsolation(evidence []RepositoryConsumerEvidence) *model.ServiceIsolation {
	drivers := discoveredConsumerDrivers(evidence)
	if len(drivers) == 0 {
		return nil
	}
	isolation := &model.ServiceIsolation{Consumers: make(map[string]model.ConsumerIsolation, len(drivers))}
	for _, driver := range drivers {
		if driver == "kafka" {
			isolation.Consumers[driver] = model.ConsumerIsolation{Mode: KafkaConsumerGuardMode, Env: KafkaConsumersEnabledEnv}
		}
	}
	return isolation
}

func RenderDiscoveredManifest(workspace string, services []DiscoveredService) ([]byte, error) {
	return renderDiscoveredManifest(workspace, services, 3)
}

func renderDiscoveredManifest(workspace string, services []DiscoveredService, version int) ([]byte, error) {
	workspace, err := resolveCwd(workspace)
	if err != nil {
		return nil, err
	}
	services = copyDiscoveredServices(services)
	sort.Slice(services, func(left int, right int) bool {
		return services[left].Name < services[right].Name
	})
	usedPorts := make(map[int]bool)
	policies := make(map[string]model.Policy)
	for index := range services {
		if version >= 3 && services[index].Policy == "" && services[index].Runtime != "" {
			if services[index].DiscoveryDriver != "passive" {
				return nil, fmt.Errorf("discovered service %q uses %s discovery; initialize a manifest with an environment registry and a matching runtime policy, then run services --registry", services[index].Name, services[index].DiscoveryDriver)
			}
			policyName := services[index].Runtime + "-passive"
			policy, found := policies[policyName]
			if !found {
				policy = discoveredEnvironmentPolicy(services[index])
			}
			for _, kind := range services[index].Kinds {
				policy.Routing.Servers[kind] = discoveredEnvironmentServerRoute(services[index].Runtime, kind, len(services[index].Kinds))
			}
			policies[policyName] = policy
			services[index].Policy = policyName
			services[index].Certifier = services[index].Runtime
		}
		ports, assigned, err := assignDiscoveredPorts(services[index].Kinds, services[index].Kind, services[index].Ports, usedPorts)
		if err != nil {
			return nil, err
		}
		if len(assigned) > 0 {
			services[index].Ports = ports
		}
	}
	entries := make(map[string]discoveredEntry, len(services))
	for _, service := range services {
		entry := discoveredEntry{
			Path:   service.Path,
			Policy: service.Policy,
			Discovery: discoveredDiscovery{
				Analyzer:  service.Analyzer,
				Certifier: service.Certifier,
				Registry:  service.Registry,
				Identity:  service.Identity,
			},
			Runner: discoveredRunner{
				Workdir:  service.Runner.Workdir,
				Artifact: service.Runner.Artifact,
				Prepare:  append([]string(nil), service.Runner.Prepare...),
				Build:    append([]string(nil), service.Runner.Build...),
				Run:      append([]string(nil), service.Runner.Run...),
			},
			Ports:        copyDiscoveredPorts(service.Ports),
			HealthChecks: append([]model.ServiceHealthCheck(nil), service.HealthChecks...),
		}
		if version >= 3 {
			entry.Kinds = append([]string(nil), service.Kinds...)
			entry.Discovery.ConsumerBindings = append([]string(nil), service.Bindings...)
			entry.Discovery.Consumers = discoveredConsumerDrivers(service.Consumers)
			entry.Isolation = discoveredConsumerIsolation(service.Consumers)
		} else {
			entry.Kind = service.Kind
			entry.Discovery.Bindings = append([]string(nil), service.Bindings...)
			entry.Health = discoveredHealthFor(service.Health)
			entry.HealthChecks = nil
		}
		entries[service.Name] = entry
	}
	var data bytes.Buffer
	encoder := yaml.NewEncoder(&data)
	encoder.SetIndent(2)
	err = encoder.Encode(discoveredManifest{
		Version: version,
		Workspace: discoveredManifestWorkspace{
			Name:             filepath.Base(workspace),
			DisabledBindings: []string{},
		},
		Policies: policies,
		Services: entries,
	})
	if err != nil {
		return nil, fmt.Errorf("encode discovered manifest: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("close discovered manifest encoder: %w", err)
	}
	return data.Bytes(), nil
}

func discoveredEnvironmentPolicy(service DiscoveredService) model.Policy {
	policy := model.Policy{
		Drivers: model.PolicyDrivers{
			Runtime: service.Runtime,
			Framework: service.Framework,
			ConfigSource: "environment",
			Discovery: "passive",
			Materializer: "environment",
		},
		Routing: model.PolicyRouting{Servers: make(map[string]model.ServerRoute)},
	}
	policy.Routing.Servers[RepositoryKindHTTP] = discoveredEnvironmentServerRoute(service.Runtime, RepositoryKindHTTP, 1)
	policy.Routing.Servers[RepositoryKindRPC] = discoveredEnvironmentServerRoute(service.Runtime, RepositoryKindRPC, 1)
	return policy
}

func discoveredEnvironmentServerRoute(runtimeName string, kind string, listenerCount int) model.ServerRoute {
	listenerEnv := "HOST"
	portEnv := ""
	if runtimeName == "quarkus" {
		if kind == RepositoryKindRPC { listenerEnv, portEnv = "QUARKUS_GRPC_SERVER_HOST", "QUARKUS_GRPC_SERVER_PORT" } else { listenerEnv, portEnv = "QUARKUS_HTTP_HOST", "QUARKUS_HTTP_PORT" }
	}
	if runtimeName == "micronaut" {
		if kind == RepositoryKindRPC { listenerEnv, portEnv = "GRPC_SERVER_HOST", "GRPC_SERVER_PORT" } else { listenerEnv, portEnv = "MICRONAUT_SERVER_HOST", "MICRONAUT_SERVER_PORT" }
	}
	server := model.ServerRoute{
		Port: kind,
		Isolation: model.ServerIsolation{
			Registration: model.RegistrationGuard{Mode: "not-applicable"},
			Listener: model.ListenerGuard{Path: listenerEnv, Value: "127.0.0.1"},
		},
	}
	if portEnv != "" {
		server.Env = map[string]string{portEnv: "${port." + kind + "}"}
	}
	return server
}

func DiscoverWorkspace(manifestPath string, workspace string, prune bool) (DiscoveryResult, error) {
	result := DiscoveryResult{}
	source, sourceInfo, err := readManifestForUpdate(manifestPath)
	if err != nil {
		return result, err
	}
	manifest, err := decodeManifest(source, manifestPath)
	if err != nil {
		return result, err
	}
	discovered, skipped, err := ScanServices(workspace)
	if err != nil {
		return result, err
	}
	result.Skipped = skipped
	for _, name := range skipped {
		result.SkippedDetails = append(result.SkippedDetails, name+": no registered Analyzer recognized a supported framework and deterministic build contract")
	}
	for _, service := range discovered {
		result.Discovered = append(result.Discovered, service.Name)
	}

	existingByName := manifest.Services
	usedPorts := make(map[int]bool)
	for _, existing := range existingByName {
		claimDiscoveredPorts(usedPorts, existing.Ports)
	}
	matched := make(map[string]bool)
	additions := make([]DiscoveredService, 0)
	updates := make(map[string]model.Service)
	for _, service := range discovered {
		repository := filepath.Join(workspace, service.Path)
		matches := make([]string, 0)
		for _, name := range ServiceNames(manifest) {
			if servicePathMatchesRepository(workspace, existingByName[name].Path, repository) {
				matches = append(matches, name)
			}
		}
		if len(matches) > 1 {
			return result, fmt.Errorf("discovered repository %q is already declared by multiple services: %s", service.Path, strings.Join(matches, ", "))
		}
		if existing, found := existingByName[service.Name]; found && !servicePathMatchesRepository(workspace, existing.Path, repository) {
			return result, fmt.Errorf("discovered service %q at %q conflicts with existing path %q", service.Name, service.Path, existing.Path)
		}
		if len(matches) == 1 {
			existingName := matches[0]
			matched[existingName] = true
			result.Existing = append(result.Existing, existingName)
			existing := existingByName[existingName]
			service.Policy, service.Certifier, err = certifyDiscoveredService(manifest, service, existing.Policy)
			if err != nil {
				return result, err
			}
			updated, changed := backfillDiscoveredDescription(existing, service, manifest.Version)
			if updated.Discovery.Analyzer == service.Analyzer {
				ports, assignedKinds, err := assignDiscoveredPorts(updated.EffectiveKinds(), updated.Kind, updated.Ports, usedPorts)
				if err != nil {
					return result, err
				}
				if len(assignedKinds) > 0 {
					updated.Ports = ports
					changed = true
					for _, kind := range assignedKinds { result.Assigned = append(result.Assigned, discoveredPortAssignment(existingName, kind, ports)) }
				}
			}
			if changed {
				updates[existingName] = updated
				result.Updated = append(result.Updated, existingName)
			}
			continue
		}
		service.Policy, service.Certifier, err = certifyDiscoveredService(manifest, service, "")
		if err != nil {
			return result, err
		}
		ports, assignedKinds, err := assignDiscoveredPorts(service.Kinds, service.Kind, service.Ports, usedPorts)
		if err != nil {
			return result, err
		}
		service.Ports = ports
		for _, kind := range assignedKinds { result.Assigned = append(result.Assigned, discoveredPortAssignment(service.Name, kind, ports)) }
		additions = append(additions, service)
		result.Added = append(result.Added, service.Name)
	}

	services := make(map[string]model.Service, len(manifest.Services)+len(additions))
	for name, service := range manifest.Services {
		services[name] = service
	}
	for name, service := range updates {
		services[name] = service
	}
	for _, service := range additions {
		services[service.Name] = discoveredModelService(service, manifest.Version)
	}
	for _, name := range ServiceNames(manifest) {
		if matched[name] || !isMissingDirectChildServicePath(workspace, manifest.Services[name].Path) {
			continue
		}
		if prune {
			delete(services, name)
			result.Pruned = append(result.Pruned, name)
		} else {
			result.Missing = append(result.Missing, name)
		}
	}
	sort.Strings(result.Added)
	sort.Strings(result.Existing)
	sort.Strings(result.Updated)
	sort.Strings(result.Missing)
	sort.Strings(result.Pruned)
	sort.Strings(result.Assigned)

	candidate := *manifest
	candidate.Services = services
	if err := validateManifest(&candidate); err != nil {
		return result, fmt.Errorf("validate discovered manifest: %w", err)
	}
	if len(result.Added) == 0 && len(result.Updated) == 0 && len(result.Pruned) == 0 {
		if err := verifyManifestSnapshot(manifestPath, source, sourceInfo, "discovery"); err != nil {
			return result, err
		}
		return result, nil
	}

	document, serviceMapping, err := loadManifestDocument(source, manifestPath)
	if err != nil {
		return result, err
	}
	if len(result.Pruned) > 0 {
		removeMappingEntries(serviceMapping, result.Pruned)
	}
	for _, name := range result.Updated {
		value := mappingValue(serviceMapping, name)
		if value == nil || value.Kind != yaml.MappingNode {
			return result, fmt.Errorf("Conven manifest %q service %q must be a mapping", manifestPath, name)
		}
		appendDiscoveredDescription(value, updates[name])
	}
	for _, service := range additions {
		value, err := discoveredServiceNode(service, manifest.Version)
		if err != nil {
			return result, err
		}
		serviceMapping.Content = append(serviceMapping.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: service.Name},
			value,
		)
	}
	if err := saveManifestDocument(manifestPath, document, source, sourceInfo, &candidate); err != nil {
		return result, err
	}
	return result, nil
}

func discoveredModelService(service DiscoveredService, version int) model.Service {
	result := model.Service{
		Path:   service.Path,
		Policy: service.Policy,
		Discovery: model.ServiceDiscovery{
			Analyzer:         service.Analyzer,
		},
		Runner: service.Runner,
		Ports:  copyDiscoveredPorts(service.Ports),
	}
	if version >= 3 {
		result.Kinds = append([]string(nil), service.Kinds...)
		result.Discovery.Certifier = service.Certifier
		result.Discovery.Registry = service.Registry
		result.Discovery.Identity = service.Identity
		result.Discovery.ConsumerBindings = append([]string(nil), service.Bindings...)
		result.Discovery.Consumers = discoveredConsumerDrivers(service.Consumers)
		if isolation := discoveredConsumerIsolation(service.Consumers); isolation != nil {
			result.Isolation = *isolation
		}
		result.HealthChecks = normalizedDiscoveredHealthChecks(service.HealthChecks)
	} else {
		result.Kind = service.Kind
		result.Discovery.Bindings = append([]string(nil), service.Bindings...)
		result.Health = service.Health
	}
	return result
}

func certifyDiscoveredService(manifest *model.Manifest, service DiscoveredService, explicit string) (string, string, error) {
	if manifest.Version < 3 && service.Runtime != "spring-boot" {
		return explicit, "", nil
	}
	certification, _, err := CertifyRepository(manifest, RepositoryCertificationRequest{
		Name:           service.Name,
		Framework:      service.Framework,
		Runtime:        service.Runtime,
		Discovery:      service.DiscoveryDriver,
		Kind:           service.Kind,
		Kinds:          append([]string(nil), service.Kinds...),
		ExplicitPolicy: explicit,
		Registrations:  append([]RepositoryRegistrationEvidence(nil), service.Registrations...),
		Consumers:      append([]RepositoryConsumerEvidence(nil), service.Consumers...),
	})
	if err != nil {
		return "", "", err
	}
	return certification.Policy, certification.Certifier, nil
}

func discoveredPortAssignment(name string, kind string, ports map[string]int) string {
	return name + "." + kind + "=" + strconv.Itoa(ports[kind])
}

func discoveredHealthFor(health model.Health) discoveredHealth {
	return discoveredHealth{
		Type:    health.Type,
		Address: health.Address,
		URL:     health.URL,
		Command: append([]string(nil), health.Command...),
		Timeout: health.Timeout,
	}
}

func copyDiscoveredServices(services []DiscoveredService) []DiscoveredService {
	copied := make([]DiscoveredService, len(services))
	for index, service := range services {
		copied[index] = service
		copied[index].Ports = copyDiscoveredPorts(service.Ports)
		copied[index].Registrations = append([]RepositoryRegistrationEvidence(nil), service.Registrations...)
		copied[index].Consumers = append([]RepositoryConsumerEvidence(nil), service.Consumers...)
	}
	return copied
}

func copyDiscoveredPorts(ports map[string]int) map[string]int {
	if len(ports) == 0 {
		return nil
	}
	copied := make(map[string]int, len(ports))
	for name, port := range ports {
		copied[name] = port
	}
	return copied
}

func normalizedDiscoveredHealthChecks(checks []model.ServiceHealthCheck) []model.ServiceHealthCheck {
	if len(checks) == 0 {
		return nil
	}
	result := append([]model.ServiceHealthCheck(nil), checks...)
	for index := range result {
		if result[index].Command == nil {
			result[index].Command = []string{}
		}
	}
	return result
}

func claimDiscoveredPorts(used map[int]bool, ports map[string]int) {
	for _, port := range ports {
		used[port] = true
	}
}

func assignDiscoveredPort(kind string, ports map[string]int, used map[int]bool) (map[string]int, bool, error) {
	assigned, kinds, err := assignDiscoveredPorts(nil, kind, ports, used)
	return assigned, len(kinds) > 0, err
}

func assignDiscoveredPorts(kinds []string, legacyKind string, ports map[string]int, used map[int]bool) (map[string]int, []string, error) {
	claimDiscoveredPorts(used, ports)
	if len(kinds) == 0 && legacyKind != "" {
		kinds = []string{legacyKind}
	}
	kinds = append([]string(nil), kinds...)
	sort.Strings(kinds)
	assigned := copyDiscoveredPorts(ports)
	assignedKinds := make([]string, 0)
	for _, kind := range kinds {
		if kind != RepositoryKindHTTP && kind != RepositoryKindRPC {
			continue
		}
		if _, found := assigned[kind]; found {
			continue
		}
		port := firstDiscoveredLocalPort
		for ; port <= 65535 && used[port]; port++ {
		}
		if port > 65535 {
			return nil, nil, errors.New("no local port is available for a discovered HTTP/RPC listener")
		}
		if assigned == nil {
			assigned = make(map[string]int, len(kinds))
		}
		assigned[kind] = port
		used[port] = true
		assignedKinds = append(assignedKinds, kind)
	}
	return assigned, assignedKinds, nil
}

func backfillDiscoveredDescription(existing model.Service, discovered DiscoveredService, version int) (model.Service, bool) {
	changed := false
	if existing.Policy == "" && discovered.Policy != "" {
		existing.Policy = discovered.Policy
		changed = true
	}
	if version < 3 && existing.Kind == "" && discovered.Kind != "" {
		existing.Kind = discovered.Kind
		changed = true
	}
	if version >= 3 && len(existing.Kinds) == 0 && len(discovered.Kinds) > 0 {
		existing.Kinds = append([]string(nil), discovered.Kinds...)
		changed = true
	}
	if existing.Discovery.Analyzer == "" {
		existing.Discovery.Analyzer = discovered.Analyzer
		if version >= 3 {
			existing.Discovery.Certifier = discovered.Certifier
			existing.Discovery.Registry = discovered.Registry
			existing.Discovery.Identity = discovered.Identity
			existing.Discovery.ConsumerBindings = append([]string(nil), discovered.Bindings...)
		} else {
			existing.Discovery.Bindings = append([]string(nil), discovered.Bindings...)
		}
		changed = true
	}
	if version >= 3 {
		consumerDrivers := discoveredConsumerDrivers(discovered.Consumers)
		if !reflect.DeepEqual(existing.Discovery.Consumers, consumerDrivers) {
			existing.Discovery.Consumers = consumerDrivers
			isolation := discoveredConsumerIsolation(discovered.Consumers)
			if isolation == nil {
				existing.Isolation = model.ServiceIsolation{}
			} else {
				existing.Isolation = *isolation
			}
			changed = true
		}
	}
	if len(existing.Runner.Prepare) == 0 && len(discovered.Runner.Prepare) > 0 {
		existing.Runner.Prepare = append([]string(nil), discovered.Runner.Prepare...)
		changed = true
	}
	if version >= 3 && len(existing.HealthChecks) == 0 && len(discovered.HealthChecks) > 0 {
		existing.HealthChecks = normalizedDiscoveredHealthChecks(discovered.HealthChecks)
		changed = true
	}
	if version < 3 && existing.Health.Type == "" && discovered.Health.Type != "" {
		existing.Health = discovered.Health
		changed = true
	}
	return existing, changed
}

func appendDiscoveredDescription(mapping *yaml.Node, service model.Service) {
	if mappingValue(mapping, "policy") == nil && service.Policy != "" {
		mapping.Content = append(mapping.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "policy"},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: service.Policy},
		)
	}
	if mappingValue(mapping, "kind") == nil && service.Kind != "" {
		mapping.Content = append(mapping.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "kind"},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: service.Kind},
		)
	}
	if mappingValue(mapping, "kinds") == nil && len(service.Kinds) > 0 {
		value := &yaml.Node{}
		value.Encode(service.Kinds)
		mapping.Content = append(mapping.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "kinds"},
			value,
		)
	}
	if mappingValue(mapping, "discovery") == nil && service.Discovery.Analyzer != "" {
		value := &yaml.Node{}
		value.Encode(discoveredDiscovery{
			Analyzer:         service.Discovery.Analyzer,
			Certifier:        service.Discovery.Certifier,
			Registry:         service.Discovery.Registry,
			Identity:         service.Discovery.Identity,
			ConsumerBindings: append([]string(nil), service.Discovery.ConsumerBindings...),
			Consumers:        append([]string(nil), service.Discovery.Consumers...),
			Bindings:         append([]string(nil), service.Discovery.Bindings...),
		})
		mapping.Content = append(mapping.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "discovery"},
			value,
		)
	}
	discovery := mappingValue(mapping, "discovery")
	if discovery != nil && discovery.Kind == yaml.MappingNode {
		if len(service.Discovery.Consumers) > 0 {
			value := &yaml.Node{}
			value.Encode(service.Discovery.Consumers)
			setMappingValue(discovery, "consumers", value)
		} else {
			removeMappingEntries(discovery, []string{"consumers"})
		}
	}
	if len(service.Isolation.Consumers) > 0 {
		value := &yaml.Node{}
		value.Encode(service.Isolation)
		setMappingValue(mapping, "isolation", value)
	} else {
		removeMappingEntries(mapping, []string{"isolation"})
	}
	if len(service.Runner.Prepare) > 0 {
		runner := mappingValue(mapping, "runner")
		if runner != nil && runner.Kind == yaml.MappingNode && mappingValue(runner, "prepare") == nil {
			value := &yaml.Node{}
			value.Encode(service.Runner.Prepare)
			runner.Content = append(runner.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "prepare"},
				value,
			)
		}
	}
	if len(service.Ports) > 0 {
		value := mappingValue(mapping, "ports")
		if value == nil {
			value = &yaml.Node{}
			value.Encode(copyDiscoveredPorts(service.Ports))
			mapping.Content = append(mapping.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "ports"},
				value,
			)
		} else if value.Kind == yaml.MappingNode {
			names := make([]string, 0, len(service.Ports))
			for name := range service.Ports {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				if mappingValue(value, name) != nil {
					continue
				}
				value.Content = append(value.Content,
					&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name},
					&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprintf("%d", service.Ports[name])},
				)
			}
		}
	}
	if mappingValue(mapping, "health") == nil && service.Health.Type != "" {
		value := &yaml.Node{}
		value.Encode(service.Health)
		mapping.Content = append(mapping.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "health"},
			value,
		)
	}
	if mappingValue(mapping, "healthChecks") == nil && len(service.HealthChecks) > 0 {
		value := &yaml.Node{}
		value.Encode(service.HealthChecks)
		mapping.Content = append(mapping.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "healthChecks"},
			value,
		)
	}
}

func moduleName(path string) (string, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", false, fmt.Errorf("open Go module %q: %w", path, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "module" {
			return strings.Trim(fields[1], "\"'"), true, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", false, fmt.Errorf("read Go module %q: %w", path, err)
	}
	return "", false, nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func regularFileExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err == nil {
		return info.Mode().IsRegular(), nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func servicePathMatchesRepository(workspace string, servicePath string, repository string) bool {
	resolved := servicePath
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(workspace, resolved)
	}
	resolved = filepath.Clean(resolved)
	if resolved == filepath.Clean(repository) {
		return true
	}
	return sameDirectory(resolved, repository)
}

func isMissingDirectChildServicePath(workspace string, servicePath string) bool {
	resolved := servicePath
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(workspace, resolved)
	}
	relative, err := filepath.Rel(workspace, filepath.Clean(resolved))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	if strings.ContainsRune(relative, filepath.Separator) {
		return false
	}
	info, err := os.Stat(resolved)
	return os.IsNotExist(err) || err == nil && !info.IsDir()
}

func readManifestForUpdate(path string) ([]byte, os.FileInfo, error) {
	observed, err := os.Lstat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect Conven manifest %q: %w", path, err)
	}
	if observed.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("Conven manifest %q is a symbolic link; Conven refuses to replace symbolic links", path)
	}
	if !observed.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("Conven manifest %q is not a regular file", path)
	}
	file, err := openManifestNoFollow(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("inspect opened Conven manifest %q: %w", path, err)
	}
	if !info.Mode().IsRegular() || !os.SameFile(observed, info) {
		return nil, nil, fmt.Errorf("Conven manifest %q changed while it was opened; retry the command", path)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, nil, fmt.Errorf("read Conven manifest %q: %w", path, err)
	}
	return data, info, nil
}

func loadManifestDocument(data []byte, path string) (*yaml.Node, *yaml.Node, error) {
	if len(data) == 0 {
		return nil, nil, fmt.Errorf("Conven manifest %q is empty", path)
	}
	document := &yaml.Node{}
	if err := yaml.Unmarshal(data, document); err != nil {
		return nil, nil, fmt.Errorf("decode Conven manifest %q for discovery: %w", path, err)
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("Conven manifest %q must contain one mapping document", path)
	}
	services := mappingValue(document.Content[0], "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("Conven manifest %q services must be a mapping", path)
	}
	return document, services, nil
}

func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return mapping.Content[index+1]
		}
	}
	return nil
}

func setMappingValue(mapping *yaml.Node, key string, value *yaml.Node) {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			mapping.Content[index+1] = value
			return
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		value,
	)
}

func removeMappingEntries(mapping *yaml.Node, names []string) {
	remove := make(map[string]bool, len(names))
	for _, name := range names {
		remove[name] = true
	}
	content := make([]*yaml.Node, 0, len(mapping.Content))
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if remove[mapping.Content[index].Value] {
			continue
		}
		content = append(content, mapping.Content[index], mapping.Content[index+1])
	}
	mapping.Content = content
}

func discoveredServiceNode(service DiscoveredService, version int) (*yaml.Node, error) {
	value := &yaml.Node{}
	entry := discoveredEntry{
		Path:   service.Path,
		Policy: service.Policy,
		Discovery: discoveredDiscovery{
			Analyzer: service.Analyzer,
		},
		Runner: discoveredRunner{
			Workdir:  service.Runner.Workdir,
			Artifact: service.Runner.Artifact,
			Prepare:  append([]string(nil), service.Runner.Prepare...),
			Build:    append([]string(nil), service.Runner.Build...),
			Run:      append([]string(nil), service.Runner.Run...),
		},
		Ports:        copyDiscoveredPorts(service.Ports),
	}
	if version >= 3 {
		entry.Kinds = append([]string(nil), service.Kinds...)
		entry.Discovery.Certifier = service.Certifier
		entry.Discovery.Registry = service.Registry
		entry.Discovery.Identity = service.Identity
		entry.Discovery.ConsumerBindings = append([]string(nil), service.Bindings...)
		entry.Discovery.Consumers = discoveredConsumerDrivers(service.Consumers)
		entry.Isolation = discoveredConsumerIsolation(service.Consumers)
		entry.HealthChecks = append([]model.ServiceHealthCheck(nil), service.HealthChecks...)
	} else {
		entry.Kind = service.Kind
		entry.Discovery.Bindings = append([]string(nil), service.Bindings...)
		entry.Health = discoveredHealthFor(service.Health)
	}
	err := value.Encode(entry)
	if err != nil {
		return nil, fmt.Errorf("encode discovered service %q: %w", service.Name, err)
	}
	return value, nil
}

func saveManifestDocument(path string, document *yaml.Node, source []byte, sourceInfo os.FileInfo, expected *model.Manifest) error {
	return saveManifestDocumentForOperation(path, document, source, sourceInfo, expected, "discovery")
}

func saveManifestDocumentForOperation(path string, document *yaml.Node, source []byte, sourceInfo os.FileInfo, expected *model.Manifest, operation string) error {
	var data bytes.Buffer
	encoder := yaml.NewEncoder(&data)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		encoder.Close()
		return fmt.Errorf("encode Conven manifest %q: %w", path, err)
	}
	if err := encoder.Close(); err != nil {
		return fmt.Errorf("close Conven manifest encoder: %w", err)
	}
	updated, err := decodeManifest(data.Bytes(), path)
	if err != nil {
		return fmt.Errorf("validate updated Conven manifest: %w", err)
	}
	if !reflect.DeepEqual(updated, expected) {
		return fmt.Errorf("validate updated Conven manifest: encoded manifest does not match the validated discovery result")
	}
	return publishManifestUpdate(path, data.Bytes(), source, sourceInfo, operation)
}

func publishManifestUpdate(path string, data []byte, source []byte, sourceInfo os.FileInfo, operation string) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".conven-manifest-*")
	if err != nil {
		return fmt.Errorf("create temporary Conven manifest: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(sourceInfo.Mode().Perm()); err != nil {
		temporary.Close()
		return fmt.Errorf("protect temporary Conven manifest: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary Conven manifest: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary Conven manifest: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary Conven manifest: %w", err)
	}
	locked, err := lockManifestSnapshot(path, source, sourceInfo, operation)
	if err != nil {
		return err
	}
	defer unlockManifest(locked)
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("publish Conven manifest %q: %w", path, err)
	}
	_ = syncDirectory(directory)
	return nil
}

func verifyManifestSnapshot(path string, source []byte, sourceInfo os.FileInfo, operation string) error {
	locked, err := lockManifestSnapshot(path, source, sourceInfo, operation)
	if err != nil {
		return err
	}
	return unlockManifest(locked)
}

func lockManifestSnapshot(path string, source []byte, sourceInfo os.FileInfo, operation string) (*os.File, error) {
	file, err := openManifestNoFollow(path)
	if err != nil {
		return nil, fmt.Errorf("reopen Conven manifest before %s: %w", operation, err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("Conven manifest %q is being updated by another Conven process; retry the command", path)
		}
		return nil, fmt.Errorf("lock Conven manifest %q before %s: %w", path, operation, err)
	}
	info, err := file.Stat()
	if err != nil {
		unlockManifest(file)
		return nil, fmt.Errorf("inspect locked Conven manifest %q: %w", path, err)
	}
	currentInfo, err := os.Lstat(path)
	if err != nil {
		unlockManifest(file)
		return nil, fmt.Errorf("reinspect Conven manifest %q before %s: %w", path, operation, err)
	}
	if currentInfo.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !os.SameFile(sourceInfo, info) || !os.SameFile(info, currentInfo) {
		unlockManifest(file)
		return nil, fmt.Errorf("Conven manifest %q changed during %s; retry the command", path, operation)
	}
	current, err := io.ReadAll(file)
	if err != nil {
		unlockManifest(file)
		return nil, fmt.Errorf("reread locked Conven manifest %q before %s: %w", path, operation, err)
	}
	if !bytes.Equal(current, source) {
		unlockManifest(file)
		return nil, fmt.Errorf("Conven manifest %q was edited during %s; retry the command", path, operation)
	}
	return file, nil
}

func openManifestNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open Conven manifest %q without following symbolic links: %w", path, err)
	}
	return os.NewFile(uintptr(fd), path), nil
}

func unlockManifest(file *os.File) error {
	unlockErr := unix.Flock(int(file.Fd()), unix.LOCK_UN)
	closeErr := file.Close()
	if unlockErr != nil {
		return fmt.Errorf("unlock Conven manifest %q: %w", file.Name(), unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close locked Conven manifest %q: %w", file.Name(), closeErr)
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open directory %q for sync: %w", path, err)
	}
	if err := directory.Sync(); err != nil {
		directory.Close()
		return fmt.Errorf("sync directory %q: %w", path, err)
	}
	if err := directory.Close(); err != nil {
		return fmt.Errorf("close directory %q after sync: %w", path, err)
	}
	return nil
}
