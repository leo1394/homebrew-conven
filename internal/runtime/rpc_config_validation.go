package runtime

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/config"
	"github.com/leo1394/homebrew-conven/internal/rpcconfig"
	"gopkg.in/yaml.v3"
)

func preflightRPCClientConfigs(plan *Plan) error {
	if plan == nil {
		return nil
	}
	for _, name := range plan.Order {
		service, found := plan.Services[name]
		if !found || service.Config == nil || !isGoZeroPlannedConfig(service.Config) || strings.TrimSpace(service.Directory) == "" {
			continue
		}
		application, err := externalDependencyApplicationPath(service.Config.Plan)
		if err != nil {
			return fmt.Errorf("service %s RPC client config path: %w", name, err)
		}
		data, err := os.ReadFile(application)
		if err != nil {
			return fmt.Errorf("service %s RPC client config %s: %w", name, application, err)
		}
		document, err := decodeExternalDependencyYAML(data)
		if err != nil {
			return fmt.Errorf("service %s RPC client config %s: %w", name, application, err)
		}
		if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
			return fmt.Errorf("service %s RPC client config %s must contain one mapping document", name, application)
		}
		bindings, err := config.InspectGoRPCClientBindings(service.Directory, service.Workdir)
		if err != nil {
			return fmt.Errorf("service %s RPC client config %s source inspection failed: %w", name, application, err)
		}
		seen := make(map[string]bool)
		for _, declaration := range bindings {
			binding := declaration.YAMLKey
			if seen[binding] {
				continue
			}
			seen[binding] = true
			node := externalMappingValue(document.Content[0], binding)
			active, err := validateActiveRPCClientConfig(node, service.Directory, service.Workdir)
			if err != nil {
				var incomplete rpcRouteIncompleteError
				if errors.As(err, &incomplete) {
					evidence, inspectErr := config.InspectGoRPCDisableCapabilities(service.Directory, service.Workdir, []string{binding})
					if inspectErr == nil && evidence[binding] == "unused" {
						continue
					}
				}
				return fmt.Errorf("service %s binding %s config %s: %w", name, binding, application, err)
			}
			if active {
				for _, route := range service.Config.Routes {
					if !route.Local || route.Binding != binding { continue }
					target := externalMappingValue(node, "target")
					discovery := externalMappingValue(node, "discovType")
					if target == nil || strings.TrimSpace(target.Value) == "" || (discovery != nil && discovery.Value != "") { continue }
					if err := config.InspectGoRPCTargetInitialization(service.Directory, service.Workdir, binding); err != nil {
						return fmt.Errorf("service %s binding %s config %s: %w", name, binding, application, err)
					}
				}
				continue
			}
			evidence, err := config.InspectGoRPCDisableCapabilities(service.Directory, service.Workdir, []string{binding})
			if err != nil {
				return fmt.Errorf("service %s binding %s config %s has no active RPC route and source disable capability could not be proven: %w", name, binding, application, err)
			}
			if evidence[binding] == "" {
				return fmt.Errorf("service %s binding %s config %s requires an active Consul, direct, target, or Etcd route", name, binding, application)
			}
		}
	}
	return nil
}

func isGoZeroPlannedConfig(planned *PlannedConfig) bool {
	runtimeName := planned.Runtime
	if runtimeName == "" {
		runtimeName = planned.Framework
	}
	return runtimeName == "go-zero"
}

type rpcRouteIncompleteError = rpcconfig.IncompleteError

func validateActiveRPCClientConfig(node *yaml.Node, directory, workdir string) (bool, error) {
	key, err := config.InspectGoRPCClientEndpointYAMLKey(directory, workdir)
	return rpcconfig.Validate(node, key, err)
}
