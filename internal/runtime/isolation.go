package runtime

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/materialize"
	"github.com/leo1394/homebrew-conven/internal/model"
	"github.com/leo1394/homebrew-conven/internal/terminal"
)

func validatePlannedIsolation(name string, kind string, config *PlannedConfig) error {
	if config == nil {
		if kind != "" {
			return fmt.Errorf("service %s kind %s has no policy-backed local isolation contract", name, kind)
		}
		return nil
	}
	if config.Isolation.RegistrationMode == "" {
		if kind != "" {
			return fmt.Errorf("service %s kind %s has no local isolation contract", name, kind)
		}
		return nil
	}
	adapter, err := requireRuntimeContract(name, config)
	if err != nil {
		return err
	}
	return adapter.ValidateIsolation(name, kind, config)
}

func validateRuntimeConfigConsumption(name string, config *PlannedConfig, run []string, environment map[string]string) error {
	if config == nil || config.Isolation.RegistrationMode == "" {
		return nil
	}
	adapter, err := requireRuntimeContract(name, config)
	if err != nil {
		return err
	}
	return adapter.ValidateRuntimeConfig(name, config, run, environment)
}

func validateLoopbackListener(value string, expectedPort int) error {
	return validateServiceListener(value, expectedPort, model.NetworkListenLoopback)
}

func validateServiceListener(value string, expectedPort int, mode string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("listener is empty")
	}
	if value != trimmed {
		return fmt.Errorf("listener %q must not contain surrounding whitespace", value)
	}
	if address := net.ParseIP(value); address != nil {
		if err := validateListenerAddress(address, value, mode); err != nil {
			return err
		}
		return nil
	}
	host, portValue, err := net.SplitHostPort(value)
	if err != nil {
		return fmt.Errorf("listener %q must be a loopback IP or IP:port", value)
	}
	address := net.ParseIP(host)
	if address == nil {
		return fmt.Errorf("listener host %q is not an IP", host)
	}
	if err := validateListenerAddress(address, host, mode); err != nil {
		return err
	}
	port, err := strconv.Atoi(portValue)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("listener port %q is invalid", portValue)
	}
	if expectedPort > 0 && port != expectedPort {
		return fmt.Errorf("listener port %d does not match the declared local port %d", port, expectedPort)
	}
	return nil
}

func validateListenerAddress(address net.IP, value string, mode string) error {
	switch mode {
	case "", model.NetworkListenLoopback:
		if !address.IsLoopback() {
			return fmt.Errorf("listener %q is not a loopback IP", value)
		}
	case model.NetworkListenAllInterfaces:
		if address.To4() == nil || !address.Equal(net.IPv4zero) {
			return fmt.Errorf("listener %q is not the IPv4 all-interfaces address 0.0.0.0", value)
		}
	default:
		return fmt.Errorf("listener mode %q is unsupported", mode)
	}
	return nil
}

func isolationListenerMode(isolation PlannedIsolation) string {
	if isolation.ListenerMode == "" {
		return model.NetworkListenLoopback
	}
	return isolation.ListenerMode
}

func validateInboundRouting(connection ConnectionConfig) error {
	switch connection.Driver {
	case "", "none", "ktctl":
		return nil
	case "command":
		return fmt.Errorf("connection.driver command cannot prove that remote inbound routing is disabled; use the built-in ktctl connect driver or no connection")
	default:
		return fmt.Errorf("connection driver %q cannot prove that remote inbound routing is disabled", connection.Driver)
	}
}

func verifyServiceIsolation(service PlannedService) error {
	if service.Config == nil || service.Config.Isolation.RegistrationMode == "" {
		return nil
	}
	if err := materialize.VerifyPlanGuards(service.Config.Plan.TargetDir, service.Config.Plan.Driver, service.Config.Plan.Guards); err != nil {
		return fmt.Errorf("verify %s local isolation: %w", service.Name, err)
	}
	return nil
}

func printVerifiedIsolation(output io.Writer, service PlannedService) {
	style := terminal.New(output)
	if isolation := consumerIsolationDescription(service.ConsumerIsolation); isolation != "" {
		fmt.Fprintf(output, "%s %s: %s\n", style.Warning("Warning: consumer guard"), style.Identifier(service.Name), isolation)
	}
	if service.Config == nil || service.Config.Isolation.RegistrationMode == "" {
		return
	}
	registration := "not applicable"
	if service.Config.Isolation.RegistrationMode == "config" {
		registration = "disabled via " + guardLocation(*service.Config.Isolation.RegistrationGuard)
	}
	listener := effectiveListener(service.Config)
	fmt.Fprintf(output, "%s %s: registration %s; listener %s %s; runtime config %s\n", style.Success("✓ Local isolation"), style.Identifier(service.Name), registration, isolationListenerMode(service.Config.Isolation), style.Identifier(listener), runtimeConfigDescription(service.Config))
}

func consumerIsolationDescription(consumers map[string]ConsumerIsolationEvidence) string {
	names := make([]string, 0, len(consumers))
	for name := range consumers {
		names = append(names, name)
	}
	sort.Strings(names)
	details := make([]string, 0, len(names))
	for _, name := range names {
		consumer := consumers[name]
		details = append(details, fmt.Sprintf("%s=%s via %s (mode=%s)", name, consumer.Status, consumer.Env, consumer.Mode))
	}
	return strings.Join(details, ", ")
}

func copyConsumerIsolation(source map[string]ConsumerIsolationEvidence) map[string]ConsumerIsolationEvidence {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]ConsumerIsolationEvidence, len(source))
	for name, evidence := range source {
		result[name] = evidence
	}
	return result
}

func plannedIsolationDescription(config *PlannedConfig) string {
	if config == nil || config.Isolation.RegistrationMode == "" {
		return ""
	}
	registration := "not-applicable"
	if config.Isolation.RegistrationMode == "config" {
		registration = "disabled via " + guardLocation(*config.Isolation.RegistrationGuard)
	}
	listener := effectiveListener(config)
	return "registration=" + registration + "; listener=" + isolationListenerMode(config.Isolation) + "(" + listener + "); runtime-config=" + runtimeConfigDescription(config)
}

func effectiveListener(config *PlannedConfig) string {
	adapter, found, err := runtimeContractForConfig(config)
	if err == nil && found {
		return adapter.EffectiveListener(config)
	}
	listener, _ := config.Isolation.ListenerGuard.Value.(string)
	return listener
}

func appendIsolationEvidence(service PlannedService, connection ConnectionConfig) error {
	if service.Config == nil || service.Config.Isolation.RegistrationMode == "" {
		return nil
	}
	file, err := openLog(service.LogPath)
	if err != nil {
		return err
	}
	registration := "not-applicable"
	if service.Config.Isolation.RegistrationMode == "config" {
		registration = "disabled(" + guardLocation(*service.Config.Isolation.RegistrationGuard) + ")"
	}
	listener := effectiveListener(service.Config)
	_, writeErr := fmt.Fprintf(file, "[conven] local isolation verified: registration=%s, listener=%s(%s), runtime-config=%s, remote-inbound=%s\n", registration, isolationListenerMode(service.Config.Isolation), listener, runtimeConfigDescription(service.Config), inboundRoutingDescription(connection))
	closeErr := file.Close()
	return errors.Join(writeErr, closeErr)
}

func runtimeConfigDescription(config *PlannedConfig) string {
	if config.Isolation.RuntimeConfigRef != "" {
		return config.Isolation.RuntimeConfigRef
	}
	if config.Isolation.RuntimeConfigDir {
		return "guarded-bootstrap(" + config.Plan.RuntimeBootstrap + "->" + config.Plan.Application + ")"
	}
	return "unverified"
}

func inboundRoutingDescription(connection ConnectionConfig) string {
	if connection.Driver == "ktctl" {
		return "disabled(connection=ktctl-connect-only)"
	}
	return "not-configured-by-conven(connection=none)"
}

func guardLocation(guard materialize.Guard) string {
	return guard.File + ":" + guard.Path
}
