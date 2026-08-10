package config

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/model"
)

type ExpandContext struct {
	Workspace   string
	Service     string
	ServiceDir  string
	StateDir    string
	RunDir      string
	ConfigDir   string
	Artifact    string
	Environment string
	Manifest    *model.Manifest
}

func Expand(value string, context ExpandContext) (string, error) {
	var result strings.Builder
	remainder := value
	for {
		start := strings.Index(remainder, "${")
		if start < 0 {
			result.WriteString(remainder)
			return result.String(), nil
		}

		result.WriteString(remainder[:start])
		afterStart := remainder[start+2:]
		end := strings.IndexByte(afterStart, '}')
		if end < 0 {
			return "", fmt.Errorf("unterminated template expression in %q", value)
		}

		expression := afterStart[:end]
		expanded, err := expandExpression(expression, context)
		if err != nil {
			return "", fmt.Errorf("expand ${%s}: %w", expression, err)
		}
		result.WriteString(expanded)
		remainder = afterStart[end+1:]
	}
}

func expandExpression(expression string, context ExpandContext) (string, error) {
	switch expression {
	case "workspace":
		return context.Workspace, nil
	case "service":
		return context.Service, nil
	case "serviceDir":
		return context.ServiceDir, nil
	case "stateDir":
		return context.StateDir, nil
	case "runDir":
		return context.RunDir, nil
	case "configDir":
		return context.ConfigDir, nil
	case "artifact":
		return context.Artifact, nil
	case "env":
		return context.Environment, nil
	}

	if strings.HasPrefix(expression, "port.") {
		portName := strings.TrimPrefix(expression, "port.")
		if portName == "" {
			return "", fmt.Errorf("port name is required")
		}
		if context.Manifest == nil {
			return "", fmt.Errorf("manifest is required for port references")
		}
		service, ok := context.Manifest.Services[context.Service]
		if !ok {
			return "", fmt.Errorf("service %q is not declared", context.Service)
		}
		port, ok := service.Ports[portName]
		if !ok {
			return "", fmt.Errorf("service %q has no port %q", context.Service, portName)
		}
		return strconv.Itoa(port), nil
	}

	if strings.HasPrefix(expression, "services.") {
		reference := strings.TrimPrefix(expression, "services.")
		separator := strings.LastIndex(reference, ".ports.")
		if separator < 1 || separator+len(".ports.") == len(reference) {
			return "", fmt.Errorf("service port reference must be services.NAME.ports.PORT")
		}
		serviceName := reference[:separator]
		portName := reference[separator+len(".ports."):]
		if context.Manifest == nil {
			return "", fmt.Errorf("manifest is required for service port references")
		}
		service, ok := context.Manifest.Services[serviceName]
		if !ok {
			return "", fmt.Errorf("service %q is not declared", serviceName)
		}
		port, ok := service.Ports[portName]
		if !ok {
			return "", fmt.Errorf("service %q has no port %q", serviceName, portName)
		}
		return strconv.Itoa(port), nil
	}

	if expression == "" {
		return "", fmt.Errorf("template expression is empty")
	}
	return "", fmt.Errorf("unknown template variable")
}
