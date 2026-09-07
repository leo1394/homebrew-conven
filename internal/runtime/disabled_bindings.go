package runtime

import (
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/leo1394/homebrew-conven/internal/config"
	"github.com/leo1394/homebrew-conven/internal/terminal"
	"gopkg.in/yaml.v3"
)

type disabledBindingValidator interface {
	ValidateDisabledBindings(PlannedService, []string, []string) (map[string]string, error)
}

func preflightDisabledBindings(plan *Plan, output io.Writer, announce bool) error {
	if plan.Workspace == nil || plan.Workspace.Manifest == nil || len(plan.Workspace.Manifest.Workspace.DisabledBindings) == 0 {
		return nil
	}
	bindings := append([]string(nil), plan.Workspace.Manifest.Workspace.DisabledBindings...)
	sort.Strings(bindings)
	known := make(map[string]bool)
	for _, service := range plan.Workspace.Manifest.Services {
		for _, binding := range service.Discovery.EffectiveConsumerBindings() {
			known[binding] = true
		}
		for _, dependency := range service.Dependencies {
			known[dependency.Binding] = true
		}
	}
	style := terminal.New(output)
	if announce {
		fmt.Fprintln(output, style.Stage("Disabled binding capability preflight"))
	}
	for _, name := range plan.Order {
		service := plan.Services[name]
		manifestService := plan.Workspace.Manifest.Services[name]
		declared := append([]string(nil), manifestService.Discovery.EffectiveConsumerBindings()...)
		for _, dependency := range manifestService.Dependencies {
			declared = append(declared, dependency.Binding)
		}
		adapter, found, err := runtimeContractForConfig(service.Config)
		if err != nil {
			return err
		}
		validator, supported := adapter.(disabledBindingValidator)
		if !found || !supported {
			for _, binding := range bindings {
				if bindingListContains(declared, binding) {
					return fmt.Errorf("service %s binding %s is requested disabled, but its runtime adapter cannot verify application disable capability; use a supported optional-client guard or remove this binding from workspace.disabledBindings", name, binding)
				}
			}
			continue
		}
		evidence, err := validator.ValidateDisabledBindings(service, bindings, declared)
		if err != nil {
			return err
		}
		for _, binding := range bindings {
			if status := evidence[binding]; status != "" {
				known[binding] = true
				if announce {
					fmt.Fprintln(output, style.Success("✓ "+name+"."+binding+": disabled ("+status+")"))
				}
			}
		}
	}
	for _, binding := range bindings {
		if !known[binding] {
			return fmt.Errorf("unknown disabled binding %s: not declared by the workspace or found in selected service source; correct workspace.disabledBindings or refresh service discovery", binding)
		}
	}
	return nil
}

func bindingListContains(bindings []string, wanted string) bool {
	for _, binding := range bindings {
		if binding == wanted {
			return true
		}
	}
	return false
}

func (goZeroConsulRuntimeContract) ValidateDisabledBindings(service PlannedService, bindings []string, declared []string) (map[string]string, error) {
	application, err := externalDependencyApplicationPath(service.Config.Plan)
	if err != nil {
		return nil, fmt.Errorf("service %s disabled binding config: %w", service.Name, err)
	}
	data, err := os.ReadFile(application)
	if err != nil {
		return nil, err
	}
	document, err := decodeExternalDependencyYAML(data)
	if err != nil {
		return nil, fmt.Errorf("service %s disabled binding config %s: %w", service.Name, application, err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("service %s disabled binding config %s must be a mapping", service.Name, application)
	}
	root := document.Content[0]
	evidence, err := config.InspectGoRPCDisableCapabilities(service.Directory, service.Workdir, bindings)
	if err != nil {
		return nil, fmt.Errorf("service %s disabled binding capability failed (config %s): %w; protect optional RPC initialization with a configuration guard before disabling it", service.Name, application, err)
	}
	result := make(map[string]string)
	for _, binding := range bindings {
		node := externalMappingValue(root, binding)
		status, proven := evidence[binding]
		if node == nil && !proven && !bindingListContains(declared, binding) {
			continue
		}
		if !proven {
			return nil, fmt.Errorf("service %s binding %s is requested disabled but no verifiable RpcClientConf source declaration was found (config %s); verify the binding YAML key and optional-client guard", service.Name, binding, application)
		}
		if err := validateDisabledRPCConfig(node); err != nil {
			return nil, fmt.Errorf("service %s binding %s cannot be disabled (config %s): %w", service.Name, binding, application, err)
		}
		result[binding] = status
	}
	return result, nil
}

// Clearing discovery alone must not leave a direct or Etcd route active.
func validateDisabledRPCConfig(node *yaml.Node) error {
	if node == nil || (node.Kind == yaml.ScalarNode && node.Tag == "!!null") {
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("expected an RPC configuration mapping")
	}
	for _, key := range []string{"discovType", "target"} {
		value := externalMappingValue(node, key)
		if value != nil && (value.Kind != yaml.ScalarNode || (value.Tag != "!!null" && (value.Tag != "!!str" || value.Value != ""))) {
			return fmt.Errorf("residual %s can keep the client active; remove the route or remove the disable request", key)
		}
	}
	for _, key := range []string{"endpoints", "endpints"} {
		if err := requireEmptyDisabledRPCSequence(externalMappingValue(node, key), key); err != nil {
			return err
		}
	}
	etcd := externalMappingValue(node, "etcd")
	if etcd != nil && etcd.Tag != "!!null" {
		if etcd.Kind != yaml.MappingNode {
			return fmt.Errorf("etcd must be a mapping")
		}
		if err := requireEmptyDisabledRPCSequence(externalMappingValue(etcd, "hosts"), "etcd.hosts"); err != nil {
			return err
		}
		key := externalMappingValue(etcd, "key")
		if key != nil && key.Tag != "!!null" && (key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value != "") {
			return fmt.Errorf("residual etcd.key can keep the client active; remove the route or remove the disable request")
		}
	}
	return nil
}

func requireEmptyDisabledRPCSequence(node *yaml.Node, path string) error {
	if node == nil || node.Tag == "!!null" || (node.Kind == yaml.SequenceNode && len(node.Content) == 0) {
		return nil
	}
	return fmt.Errorf("residual %s can keep the client active; remove the route or remove the disable request", path)
}
