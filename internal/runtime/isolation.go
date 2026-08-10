package runtime

import (
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/materialize"
	"github.com/leo1394/homebrew-conven/internal/terminal"
)

func validatePlannedIsolation(name string, kind string, config *PlannedConfig) error {
	if config == nil {
		if kind != "" {
			return fmt.Errorf("service %s kind %s has no policy-backed local isolation contract", name, kind)
		}
		return nil
	}
	isolation := config.Isolation
	if isolation.RegistrationMode == "" {
		if kind != "" {
			return fmt.Errorf("service %s kind %s has no local isolation contract", name, kind)
		}
		return nil
	}
	if kind == "rpc" && isolation.RegistrationMode != "config" {
		return fmt.Errorf("service %s kind rpc must verify disabled registration through its final runtime config", name)
	}
	if kind == "http" && isolation.RegistrationMode != "not-applicable" {
		return fmt.Errorf("service %s kind http registration isolation must be not-applicable for the trusted go-zero Consul adapter", name)
	}
	if config.Framework != "go-zero" || config.Discovery != "consul" || config.Plan.Driver != materialize.DriverYAMLOverlay {
		return fmt.Errorf("service %s has no trusted local isolation adapter for framework %q, discovery %q, and materializer %q", name, config.Framework, config.Discovery, config.Plan.Driver)
	}
	if kind != "rpc" && kind != "http" {
		return fmt.Errorf("service %s kind %s has no trusted go-zero Consul local isolation contract", name, kind)
	}
	if isolation.RegistrationMode == "config" && isolation.RegistrationGuard == nil {
		return fmt.Errorf("service %s registration isolation guard is missing", name)
	}
	if isolation.RegistrationMode == "config" {
		guard := *isolation.RegistrationGuard
		disabledValue, ok := guard.Value.(string)
		if guard.File != config.Plan.Application || guard.Path != "discovType" || !ok || disabledValue != "" {
			return fmt.Errorf("service %s go-zero Consul registration isolation must enforce %s:discovType to the empty string", name, config.Plan.Application)
		}
	}
	listener, ok := isolation.ListenerGuard.Value.(string)
	if !ok {
		return fmt.Errorf("service %s listener isolation value must be a string", name)
	}
	wantListenerPath := "host"
	if kind == "rpc" {
		wantListenerPath = "listenOn"
	}
	if isolation.ListenerGuard.File != config.Plan.Application || isolation.ListenerGuard.Path != wantListenerPath {
		return fmt.Errorf("service %s kind %s listener isolation must enforce %s:%s", name, kind, config.Plan.Application, wantListenerPath)
	}
	if err := validateLoopbackListener(listener, isolation.ListenerPort); err != nil {
		return fmt.Errorf("service %s listener isolation: %w", name, err)
	}
	if kind == "rpc" {
		if _, _, err := net.SplitHostPort(listener); err != nil {
			return fmt.Errorf("service %s kind rpc listener isolation must include its declared port", name)
		}
	} else if net.ParseIP(listener) == nil {
		return fmt.Errorf("service %s kind http listener isolation must be a loopback IP without a port", name)
	}
	return nil
}

func validateRuntimeConfigConsumption(name string, config *PlannedConfig, run []string, environment map[string]string) error {
	if config == nil || config.Isolation.RegistrationMode == "" {
		return nil
	}
	target := filepath.Clean(config.Plan.TargetDir)
	accepted := false
	if config.Isolation.RuntimeConfigDir && environment["PROFILE_ACTIVE"] == "local" {
		accepted = true
	}
	config.Isolation.RuntimeConfigRef = ""
	found := 0
	for index := 1; index < len(run); index++ {
		argument := run[index]
		candidate := ""
		if argument == "-f" {
			if index+1 >= len(run) {
				return fmt.Errorf("service %s run command has a go-zero -f flag without a value", name)
			}
			candidate = run[index+1]
			index++
		} else if strings.HasPrefix(argument, "-f=") {
			candidate = strings.TrimPrefix(argument, "-f=")
		} else {
			return fmt.Errorf("service %s trusted go-zero adapter run command supports only its executable and one -f runtime config flag, got %q", name, argument)
		}
		found++
		if found > 1 {
			return fmt.Errorf("service %s trusted go-zero adapter run command must contain exactly one -f runtime config flag", name)
		}
		if accepted && filepath.IsAbs(candidate) && filepath.Clean(candidate) == target {
			config.Isolation.RuntimeConfigRef = "guarded-bootstrap(" + config.Plan.RuntimeBootstrap + "->" + config.Plan.Application + ")"
			continue
		}
		return fmt.Errorf("service %s run command has an unverified go-zero -f path %q", name, candidate)
	}
	if found == 1 {
		return nil
	}
	return fmt.Errorf("service %s run command does not pass its verified runtime config directory through the go-zero -f flag", name)
}

func validateLoopbackListener(value string, expectedPort int) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("listener is empty")
	}
	if value != trimmed {
		return fmt.Errorf("listener %q must not contain surrounding whitespace", value)
	}
	if address := net.ParseIP(value); address != nil {
		if !address.IsLoopback() {
			return fmt.Errorf("listener %q is not a loopback IP", value)
		}
		return nil
	}
	host, portValue, err := net.SplitHostPort(value)
	if err != nil {
		return fmt.Errorf("listener %q must be a loopback IP or IP:port", value)
	}
	address := net.ParseIP(host)
	if address == nil || !address.IsLoopback() {
		return fmt.Errorf("listener host %q is not a loopback IP", host)
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
	if err := materialize.VerifyGuards(service.Config.Plan.TargetDir, service.Config.Plan.Guards); err != nil {
		return fmt.Errorf("verify %s local isolation: %w", service.Name, err)
	}
	return nil
}

func printVerifiedIsolation(output io.Writer, service PlannedService) {
	if service.Config == nil || service.Config.Isolation.RegistrationMode == "" {
		return
	}
	style := terminal.New(output)
	registration := "not applicable"
	if service.Config.Isolation.RegistrationMode == "config" {
		registration = "disabled via " + guardLocation(*service.Config.Isolation.RegistrationGuard)
	}
	listener := effectiveListener(service.Config)
	fmt.Fprintf(output, "%s %s: registration %s; listener loopback %s; runtime config %s\n", style.Success("✓ Local isolation"), style.Identifier(service.Name), registration, style.Identifier(listener), runtimeConfigDescription(service.Config))
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
	return "registration=" + registration + "; listener=loopback(" + listener + "); runtime-config=" + runtimeConfigDescription(config)
}

func effectiveListener(config *PlannedConfig) string {
	listener := config.Isolation.ListenerGuard.Value.(string)
	if config.Isolation.ListenerGuard.Path == "host" && config.Isolation.ListenerPort > 0 {
		return net.JoinHostPort(listener, strconv.Itoa(config.Isolation.ListenerPort))
	}
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
	_, writeErr := fmt.Fprintf(file, "[conven] local isolation verified: registration=%s, listener=loopback(%s), runtime-config=%s, remote-inbound=%s\n", registration, listener, runtimeConfigDescription(service.Config), inboundRoutingDescription(connection))
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
