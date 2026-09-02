package runtime

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/model"
)

type pythonServerRuntimeContract struct {
	environmentRuntimeContract
}

func init() {
	RegisterRuntimeContractAdapter(pythonServerRuntimeContract{})
}

func (pythonServerRuntimeContract) Name() string {
	return "python-server-environment"
}

func (pythonServerRuntimeContract) Matches(policy model.Policy, kind string) bool {
	runtimeName := policyRuntime(policy)
	if runtimeName != "asgi-uvicorn" && runtimeName != "wsgi-gunicorn" {
		return false
	}
	return policy.Drivers.ConfigSource == "environment" && policy.Drivers.Materializer == "environment" && environmentDiscoverySupported(policy.Drivers.Discovery) && kind == "http"
}

func (pythonServerRuntimeContract) MatchesPlanned(planned *PlannedConfig) bool {
	return planned != nil && planned.Contract == "python-server-environment"
}

func (pythonServerRuntimeContract) ProtectedServerArguments(arguments []string, _ string, planned *PlannedConfig) []string {
	host, _ := planned.Isolation.ListenerGuard.Value.(string)
	port := strconv.Itoa(planned.Isolation.ListenerPort)
	result := append([]string(nil), arguments...)
	if planned.Runtime == "asgi-uvicorn" {
		return append(result, "--host", host, "--port", port)
	}
	return append(result, "--bind", net.JoinHostPort(host, planned.Isolation.ListenerPortString()))
}

func (pythonServerRuntimeContract) ValidateRuntimeConfig(name string, planned *PlannedConfig, run []string, environment map[string]string) error {
	base := environmentRuntimeContract{}
	if err := base.ValidateRuntimeConfig(name, planned, run, environment); err != nil {
		return err
	}
	host, _ := planned.Isolation.ListenerGuard.Value.(string)
	if planned.Runtime == "asgi-uvicorn" {
		if err := requireProtectedArgument(run, "--host", host); err != nil {
			return fmt.Errorf("service %s Uvicorn listener: %w", name, err)
		}
		if err := requireProtectedArgument(run, "--port", strconv.Itoa(planned.Isolation.ListenerPort)); err != nil {
			return fmt.Errorf("service %s Uvicorn listener: %w", name, err)
		}
		return nil
	}
	if err := requireProtectedArgument(run, "--bind", net.JoinHostPort(host, strconv.Itoa(planned.Isolation.ListenerPort))); err != nil {
		return fmt.Errorf("service %s Gunicorn listener: %w", name, err)
	}
	return nil
}

func requireProtectedArgument(arguments []string, flag string, want string) error {
	values := make([]string, 0, 1)
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == flag {
			if index+1 >= len(arguments) {
				return fmt.Errorf("%s has no value", flag)
			}
			values = append(values, arguments[index+1])
			index++
			continue
		}
		if strings.HasPrefix(argument, flag+"=") {
			values = append(values, strings.TrimPrefix(argument, flag+"="))
		}
	}
	if len(values) != 1 {
		return fmt.Errorf("requires exactly one %s value, found %d", flag, len(values))
	}
	if values[0] != want {
		return fmt.Errorf("%s=%q conflicts with required value %q", flag, values[0], want)
	}
	return nil
}

func (isolation PlannedIsolation) ListenerPortString() string {
	return strconv.Itoa(isolation.ListenerPort)
}
