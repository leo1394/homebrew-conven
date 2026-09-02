package config

import (
	"fmt"
	"sort"

	"github.com/leo1394/homebrew-conven/internal/model"
	"gopkg.in/yaml.v3"
)

type ServiceListenerUpdateResult struct {
	Changed   []string
	Unchanged []string
}

func SetServiceListenerScope(path string, services []string, listen string) (ServiceListenerUpdateResult, error) {
	result := ServiceListenerUpdateResult{}
	if listen != model.NetworkListenLoopback && listen != model.NetworkListenAllInterfaces {
		return result, fmt.Errorf("service listener scope must be loopback or all-interfaces, got %q", listen)
	}
	if len(services) == 0 {
		return result, fmt.Errorf("service listener update requires at least one service")
	}
	source, sourceInfo, err := readManifestForUpdate(path)
	if err != nil {
		return result, err
	}
	manifest, err := decodeManifest(source, path)
	if err != nil {
		return result, err
	}
	if err := ValidateSelection(manifest, services); err != nil {
		return result, err
	}

	names := append([]string(nil), services...)
	sort.Strings(names)
	candidate := *manifest
	candidate.Services = make(map[string]model.Service, len(manifest.Services))
	for name, service := range manifest.Services {
		candidate.Services[name] = service
	}
	for _, name := range names {
		service := candidate.Services[name]
		if listen == model.NetworkListenAllInterfaces {
			kinds := service.EffectiveKinds()
			if len(kinds) == 0 {
				return ServiceListenerUpdateResult{}, fmt.Errorf("service %s must declare at least one http or rpc kind before enabling all-interfaces listening", name)
			}
			policyName := service.Policy
			if policyName == "" {
				policyName = candidate.Workspace.Policy
			}
			policy, found := candidate.Policies[policyName]
			if policyName == "" || !found {
				return ServiceListenerUpdateResult{}, fmt.Errorf("service %s has no policy-backed listener adapter", name)
			}
			for _, kind := range kinds {
				if kind != "http" && kind != "rpc" {
					return ServiceListenerUpdateResult{}, fmt.Errorf("service %s kind %s cannot use all-interfaces listening", name, kind)
				}
				if _, found := policy.Routing.Servers[kind]; !found {
					return ServiceListenerUpdateResult{}, fmt.Errorf("service %s policy %s has no %s listener adapter", name, policyName, kind)
				}
			}
			if service.Network.Listen == model.NetworkListenAllInterfaces {
				result.Unchanged = append(result.Unchanged, name)
				continue
			}
			service.Network.Listen = model.NetworkListenAllInterfaces
		} else {
			if service.Network.Listen == "" {
				result.Unchanged = append(result.Unchanged, name)
				continue
			}
			service.Network = model.ServiceNetwork{}
		}
		candidate.Services[name] = service
		result.Changed = append(result.Changed, name)
	}
	if err := validateManifest(&candidate); err != nil {
		return ServiceListenerUpdateResult{}, fmt.Errorf("validate service listener update: %w", err)
	}
	if len(result.Changed) == 0 {
		if err := verifyManifestSnapshot(path, source, sourceInfo, "service listener update"); err != nil {
			return ServiceListenerUpdateResult{}, err
		}
		return result, nil
	}

	document, serviceMapping, err := loadManifestDocument(source, path)
	if err != nil {
		return ServiceListenerUpdateResult{}, err
	}
	for _, name := range result.Changed {
		service := mappingValue(serviceMapping, name)
		if service == nil || service.Kind != yaml.MappingNode {
			return ServiceListenerUpdateResult{}, fmt.Errorf("Conven manifest %q service %q must be a mapping", path, name)
		}
		if listen == model.NetworkListenLoopback {
			removeMappingEntries(service, []string{"network"})
			continue
		}
		setServiceListenerNode(service, listen)
	}
	if err := saveManifestDocumentForOperation(path, document, source, sourceInfo, &candidate, "service listener update"); err != nil {
		return ServiceListenerUpdateResult{}, err
	}
	return result, nil
}

func setServiceListenerNode(service *yaml.Node, listen string) {
	network := mappingValue(service, "network")
	if network == nil {
		network = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		service.Content = append(service.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "network"},
			network,
		)
	} else if network.Kind != yaml.MappingNode {
		network.Kind = yaml.MappingNode
		network.Tag = "!!map"
		network.Value = ""
		network.Content = nil
	}
	value := mappingValue(network, "listen")
	if value == nil {
		network.Content = append(network.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "listen"},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: listen},
		)
		return
	}
	value.Kind = yaml.ScalarNode
	value.Tag = "!!str"
	value.Value = listen
	value.Content = nil
}
