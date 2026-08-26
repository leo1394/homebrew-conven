package dependency

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/model"
)

type Resolution struct {
	Owner     string
	Alias     string
	Mode      string
	Target    string
	Host      string
	Ports     map[string]int
	Address   string
	Addresses string
	Env       map[string]string
}

func Resolve(manifest *model.Manifest, environmentName string, selected []string, environment map[string]string) (map[string]map[string]Resolution, error) {
	environmentDeclaration := manifest.Environments[environmentName]
	selectedSet := make(map[string]bool, len(selected))
	for _, name := range selected {
		selectedSet[name] = true
	}
	resolved := make(map[string]map[string]Resolution, len(selected))
	for _, owner := range selected {
		service := manifest.Services[owner]
		aliases := make([]string, 0, len(service.Dependencies))
		for alias := range service.Dependencies {
			aliases = append(aliases, alias)
		}
		sort.Strings(aliases)
		resolved[owner] = make(map[string]Resolution, len(aliases))
		for _, alias := range aliases {
			declaration := service.Dependencies[alias]
			localService := declaration.LocalService
			if localService == "" {
				if _, found := manifest.Services[alias]; found {
					localService = alias
				}
			}
			resolution := Resolution{Owner: owner, Alias: alias, Env: copyValues(declaration.Env)}
			if localService != "" && selectedSet[localService] {
				resolution.Mode = "local"
				resolution.Target = localService
				resolution.Host = "127.0.0.1"
				resolution.Ports = copyPorts(manifest.Services[localService].Ports)
				mergeValues(resolution.Env, declaration.LocalEnv)
			} else {
				configured, found := environmentDeclaration.Resolutions[owner][alias]
				if !found && environmentName == "local" {
					if _, exists := environmentDeclaration.Endpoints[alias]; exists {
						configured = model.DependencyResolution{Mode: "endpoint", Target: alias}
						found = true
					}
				}
				if !found {
					return nil, fmt.Errorf("service %s dependency %s has no resolution in environment %s", owner, alias, environmentName)
				}
				resolution.Mode = configured.Mode
				resolution.Target = configured.Target
				switch configured.Mode {
				case "endpoint":
					endpoint := environmentDeclaration.Endpoints[configured.Target]
					host, port, err := splitAddress(endpoint.Address)
					if err != nil {
						return nil, fmt.Errorf("resolve endpoint %s: %w", configured.Target, err)
					}
					resolution.Host = host
					resolution.Ports = map[string]int{"default": port}
				case "remote":
					mergeValues(resolution.Env, declaration.RemoteEnv)
				case "disabled":
					required := declaration.Required == nil || *declaration.Required
					if required {
						return nil, fmt.Errorf("service %s dependency %s is required but disabled", owner, alias)
					}
				case "error":
					return nil, fmt.Errorf("service %s dependency %s is blocked in environment %s", owner, alias, environmentName)
				default:
					return nil, fmt.Errorf("service %s dependency %s has unsupported resolution mode %q", owner, alias, configured.Mode)
				}
				mergeValues(resolution.Env, configured.Env)
			}
			populateAddresses(&resolution)
			for key, value := range resolution.Env {
				expanded, err := expandValue(value, resolution, environment)
				if err != nil {
					return nil, fmt.Errorf("expand service %s dependency %s env %s: %w", owner, alias, key, err)
				}
				resolution.Env[key] = expanded
			}
			resolved[owner][alias] = resolution
		}
	}
	return resolved, nil
}

func splitAddress(address string) (string, int, error) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return "", 0, fmt.Errorf("address %q must use host:port", address)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("address %q has an invalid port", address)
	}
	return host, port, nil
}

func populateAddresses(resolution *Resolution) {
	if resolution == nil || resolution.Host == "" || len(resolution.Ports) == 0 {
		return
	}
	names := make([]string, 0, len(resolution.Ports))
	for name := range resolution.Ports {
		names = append(names, name)
	}
	sort.Strings(names)
	addresses := make([]string, 0, len(names))
	for _, name := range names {
		addresses = append(addresses, net.JoinHostPort(resolution.Host, strconv.Itoa(resolution.Ports[name])))
	}
	resolution.Address = addresses[0]
	resolution.Addresses = strings.Join(addresses, ",")
}

func expandValue(value string, resolution Resolution, environment map[string]string) (string, error) {
	replacements := map[string]string{
		"dependency.name":      resolution.Target,
		"dependency.host":      resolution.Host,
		"dependency.address":   resolution.Address,
		"dependency.addresses": resolution.Addresses,
		"dependency.mode":      resolution.Mode,
	}
	if len(resolution.Ports) == 1 {
		for _, port := range resolution.Ports {
			replacements["dependency.port"] = strconv.Itoa(port)
		}
	}
	for name, port := range resolution.Ports {
		replacements["dependency.ports."+name] = strconv.Itoa(port)
	}
	var result strings.Builder
	remainder := value
	for {
		start := strings.Index(remainder, "${")
		if start < 0 {
			result.WriteString(remainder)
			return result.String(), nil
		}
		result.WriteString(remainder[:start])
		after := remainder[start+2:]
		end := strings.IndexByte(after, '}')
		if end < 0 {
			return "", fmt.Errorf("unterminated template expression")
		}
		expression := after[:end]
		if replacement, found := replacements[expression]; found {
			if replacement == "" && strings.HasPrefix(expression, "dependency.") {
				return "", fmt.Errorf("%s is unavailable for %s resolution", expression, resolution.Mode)
			}
			result.WriteString(replacement)
		} else {
			if strings.HasPrefix(expression, "dependency.") {
				return "", fmt.Errorf("%s is unavailable for %s resolution", expression, resolution.Mode)
			}
			name := expression
			message := "environment variable " + expression + " is required"
			if separator := strings.Index(expression, ":?"); separator >= 0 {
				name = expression[:separator]
				message = expression[separator+2:]
			}
			value, found := environment[name]
			if !found || value == "" {
				return "", fmt.Errorf("%s", message)
			}
			result.WriteString(value)
		}
		remainder = after[end+1:]
	}
}

func copyPorts(source map[string]int) map[string]int {
	result := make(map[string]int, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func copyValues(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func mergeValues(target map[string]string, source map[string]string) {
	for key, value := range source {
		target[key] = value
	}
}
