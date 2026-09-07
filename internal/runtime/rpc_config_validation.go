package runtime

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode"

	"github.com/leo1394/homebrew-conven/internal/config"
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

type rpcRouteIncompleteError struct {
	message string
}

func (err rpcRouteIncompleteError) Error() string {
	return err.message
}

func incompleteRPCRoute(message string) error {
	return rpcRouteIncompleteError{message: message}
}

func validateActiveRPCClientConfig(node *yaml.Node, directory, workdir string) (bool, error) {
	if node == nil || node.Tag == "!!null" {
		return false, nil
	}
	if node.Kind != yaml.MappingNode {
		return false, fmt.Errorf("expected an RPC configuration mapping")
	}
	if err := validateRPCClientTypedShape(node); err != nil {
		return false, err
	}

	discovType, err := optionalRPCString(externalMappingValue(node, "discovType"), "discovType")
	if err != nil {
		return false, err
	}
	if discovType != "" && discovType != "consul" && discovType != "etcd" {
		return false, fmt.Errorf("discovType uses unsupported discovery mode")
	}
	if discovType == "consul" {
		if err := validateRPCClientConsul(externalMappingValue(node, "consul")); err != nil {
			return false, err
		}
		return true, nil
	}

	expectedEndpointKey, endpointKeyErr := config.InspectGoRPCClientEndpointYAMLKey(directory, workdir)
	if endpointKeyErr != nil {
		for _, key := range []string{"endpoints", "endpints"} {
			values, err := rpcStringSequence(externalMappingValue(node, key), key)
			if err != nil {
				return false, err
			}
			if len(values) > 0 {
				return false, fmt.Errorf("cannot verify direct endpoint spelling before target/Etcd fallback: %w", endpointKeyErr)
			}
		}
	}
	if endpointKeyErr == nil {
		endpoints, err := rpcStringSequence(externalMappingValue(node, expectedEndpointKey), expectedEndpointKey)
		if err != nil {
			return false, err
		}
		if len(endpoints) > 0 {
			for _, endpoint := range endpoints {
				if err := validateRPCAddress(endpoint); err != nil {
					return false, fmt.Errorf("%s contains an invalid endpoint: %w", expectedEndpointKey, err)
				}
			}
			return true, nil
		}
	}

	target, err := optionalRPCString(externalMappingValue(node, "target"), "target")
	if err != nil {
		return false, err
	}
	if target != "" {
		if err := validateRPCAddress(target); err != nil {
			return false, fmt.Errorf("target is not a valid gRPC target: %w", err)
		}
		return true, nil
	}
	etcdActive, err := validateRPCClientEtcd(externalMappingValue(node, "etcd"))
	if err != nil {
		return false, err
	}
	if etcdActive {
		return true, nil
	}
	for _, unsupported := range []string{"endpoints", "endpints"} {
		if unsupported == expectedEndpointKey || externalMappingValue(node, unsupported) == nil {
			continue
		}
		values, err := rpcStringSequence(externalMappingValue(node, unsupported), unsupported)
		if err != nil {
			return false, err
		}
		if len(values) > 0 {
			if endpointKeyErr != nil {
				return false, fmt.Errorf("cannot verify direct endpoint spelling: %w", endpointKeyErr)
			}
			return false, fmt.Errorf("%s is not consumed by RpcClientConf; use the source-declared %s key", unsupported, expectedEndpointKey)
		}
	}
	if discovType == "etcd" {
		return false, incompleteRPCRoute("discovType etcd requires direct endpoints, target, or etcd hosts/key")
	}
	return false, nil
}

func validateRPCClientConsul(node *yaml.Node) error {
	if node == nil {
		return incompleteRPCRoute("consul discovery requires a consul mapping")
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("consul must be a mapping")
	}
	host, err := requiredRPCString(externalMappingValue(node, "host"), "consul.host")
	if err != nil {
		return err
	}
	if containsRPCWhitespace(host) {
		return fmt.Errorf("consul.host must not contain whitespace")
	}
	if _, err := externalPortValue(externalMappingValue(node, "port")); err != nil {
		return fmt.Errorf("consul.port is invalid: %w", err)
	}
	key, err := requiredRPCString(externalMappingValue(node, "key"), "consul.key")
	if err != nil {
		return err
	}
	if containsRPCWhitespace(key) {
		return fmt.Errorf("consul.key must not contain whitespace")
	}
	return nil
}

func validateRPCClientTypedShape(node *yaml.Node) error {
	for _, path := range []string{"discovType", "target", "app", "token"} {
		if _, err := optionalRPCString(externalMappingValue(node, path), path); err != nil {
			return err
		}
	}
	timeout := externalMappingValue(node, "timeout")
	if timeout != nil {
		if timeout.Kind != yaml.ScalarNode {
			return fmt.Errorf("timeout must be an integer within int64 range")
		}
		var value int64
		if err := timeout.Decode(&value); err != nil {
			return fmt.Errorf("timeout must be an integer within int64 range")
		}
	}
	for _, path := range []string{"endpoints", "endpints"} {
		if _, err := rpcStringSequence(externalMappingValue(node, path), path); err != nil {
			return err
		}
	}
	consul := externalMappingValue(node, "consul")
	if consul != nil {
		if consul.Kind != yaml.MappingNode {
			return fmt.Errorf("consul must be a mapping")
		}
		for _, path := range []string{"host", "key"} {
			if _, err := optionalRPCString(externalMappingValue(consul, path), "consul."+path); err != nil {
				return err
			}
		}
		port := externalMappingValue(consul, "port")
		if port != nil {
			if port.Kind != yaml.ScalarNode {
				return fmt.Errorf("consul.port must be an integer")
			}
			var value int
			if err := port.Decode(&value); err != nil {
				return fmt.Errorf("consul.port must be an integer")
			}
		}
	}
	etcd := externalMappingValue(node, "etcd")
	if etcd != nil {
		if etcd.Kind != yaml.MappingNode {
			return fmt.Errorf("etcd must be a mapping")
		}
		if _, err := rpcStringSequence(externalMappingValue(etcd, "hosts"), "etcd.hosts"); err != nil {
			return err
		}
		if _, err := optionalRPCString(externalMappingValue(etcd, "key"), "etcd.key"); err != nil {
			return err
		}
	}
	return nil
}

func validateRPCClientEtcd(node *yaml.Node) (bool, error) {
	if node == nil {
		return false, nil
	}
	if node.Kind != yaml.MappingNode {
		return false, fmt.Errorf("etcd must be a mapping")
	}
	hosts, err := rpcStringSequence(externalMappingValue(node, "hosts"), "etcd.hosts")
	if err != nil {
		return false, err
	}
	key, err := optionalRPCString(externalMappingValue(node, "key"), "etcd.key")
	if err != nil {
		return false, err
	}
	if len(hosts) == 0 && key == "" {
		return false, nil
	}
	if len(hosts) == 0 {
		return false, incompleteRPCRoute("Etcd discovery requires etcd.hosts")
	}
	if key == "" {
		return false, incompleteRPCRoute("Etcd discovery requires etcd.key")
	}
	for _, host := range hosts {
		if err := validateRPCAddress(host); err != nil {
			return false, fmt.Errorf("etcd.hosts contains an invalid endpoint: %w", err)
		}
	}
	if containsRPCWhitespace(key) {
		return false, fmt.Errorf("etcd.key must not contain whitespace")
	}
	return true, nil
}

func optionalRPCString(node *yaml.Node, path string) (string, error) {
	if node == nil {
		return "", nil
	}
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return "", fmt.Errorf("%s must be a string", path)
	}
	if node.Value != "" && strings.TrimSpace(node.Value) == "" {
		return "", fmt.Errorf("%s must not be whitespace-only", path)
	}
	return node.Value, nil
}

func requiredRPCString(node *yaml.Node, path string) (string, error) {
	value, err := optionalRPCString(node, path)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", incompleteRPCRoute(path + " is required")
	}
	return value, nil
}

func rpcStringSequence(node *yaml.Node, path string) ([]string, error) {
	if node == nil {
		return nil, nil
	}
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s must be a sequence", path)
	}
	values := make([]string, 0, len(node.Content))
	for _, item := range node.Content {
		value, err := optionalRPCString(item, path+" entry")
		if err != nil {
			return nil, err
		}
		if value == "" {
			return nil, fmt.Errorf("%s entries must not be empty", path)
		}
		if containsRPCWhitespace(value) {
			return nil, fmt.Errorf("%s entries must not contain whitespace", path)
		}
		values = append(values, value)
	}
	return values, nil
}

func containsRPCWhitespace(value string) bool {
	return strings.IndexFunc(value, unicode.IsSpace) >= 0
}

func validateRPCAddress(value string) error {
	if containsRPCWhitespace(value) {
		return fmt.Errorf("value must not contain whitespace")
	}
	host, portText, err := net.SplitHostPort(value)
	if err == nil && strings.TrimSpace(host) != "" {
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("plain address port must be between 1 and 65535")
		}
		return nil
	}
	parsed, parseErr := url.Parse(value)
	if parseErr != nil || parsed.Scheme == "" {
		return fmt.Errorf("value must be host:port or a valid URI target")
	}
	return nil
}
