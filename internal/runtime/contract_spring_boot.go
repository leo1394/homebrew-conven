package runtime

import (
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/config"
	"github.com/leo1394/homebrew-conven/internal/materialize"
	"github.com/leo1394/homebrew-conven/internal/model"
)

type springBootConsulRuntimeContract struct{}

func init() {
	RegisterRuntimeContractAdapter(springBootConsulRuntimeContract{})
}

func (springBootConsulRuntimeContract) Name() string {
	return "spring-boot-repository-overlay"
}

func (springBootConsulRuntimeContract) Matches(policy model.Policy, kind string) bool {
	runtimeName := policy.Drivers.Runtime
	if runtimeName == "" {
		runtimeName = policy.Drivers.Framework
	}
	if runtimeName != "spring-boot" || materialize.SourceDriver(policy.Drivers.ConfigSource) != materialize.SourceRepository || !springDiscoverySupported(policy.Drivers.Discovery) || !springMaterializerSupported(policy.Drivers.Materializer) {
		return false
	}
	server, found := policy.Routing.Servers[kind]
	if !found {
		return false
	}
	if policy.Drivers.Discovery == "passive" || policy.Drivers.Discovery == "kubernetes-dns" {
		if server.Isolation.Registration.Mode != "not-applicable" { return false }
	} else if server.Isolation.Registration.Mode != "config" || !springRegistrationPathSupported(policy.Drivers.Discovery, server.Isolation.Registration.Path) {
		return false
	}
	wantListener := "server.address"
	if kind == "rpc" {
		wantListener = "grpc.server.address"
	}
	return (kind == "http" || kind == "rpc") && server.Isolation.Listener.Path == wantListener
}

func (springBootConsulRuntimeContract) MatchesPlanned(planned *PlannedConfig) bool {
	runtimeName := planned.Runtime
	if runtimeName == "" { runtimeName = planned.Framework }
	return runtimeName == "spring-boot" && planned.Plan.SourceDriver == materialize.SourceRepository && springDiscoverySupported(planned.Discovery) && (planned.Plan.Driver == materialize.DriverYAMLOverlay || planned.Plan.Driver == materialize.DriverPropertiesOverlay)
}

func (springBootConsulRuntimeContract) AllowGuardCreation() bool {
	return true
}

func (springBootConsulRuntimeContract) RuntimeConfigGuards(_ materialize.Plan, _ config.ExpandContext) ([]materialize.Guard, error) {
	return nil, nil
}

func (springBootConsulRuntimeContract) ServerArguments(arguments []string, kind string, isolation PlannedIsolation) []string {
	if isolation.ListenerMode != "all-interfaces" {
		return append([]string(nil), arguments...)
	}
	key := "--server.address="
	if kind == "rpc" {
		key = "--grpc.server.address="
	}
	result := append([]string(nil), arguments...)
	for index, argument := range result {
		if strings.HasPrefix(argument, key) {
			result[index] = key + "0.0.0.0"
		}
	}
	return result
}

func (springBootConsulRuntimeContract) ProtectedServerArguments(arguments []string, kind string, planned *PlannedConfig) []string {
	listenerKey := "server.address"
	portKey := "server.port"
	if kind == "rpc" {
		listenerKey = "grpc.server.address"
		portKey = "grpc.server.port"
	}
	listener, _ := planned.Isolation.ListenerGuard.Value.(string)
	required := []string{
		"--spring.config.location=file:" + filepath.Clean(planned.Plan.TargetDir) + string(filepath.Separator),
	}
	if planned.Isolation.RegistrationMode == "config" {
		required = append(required, "--"+springRegistrationPath(planned.Discovery)+"=false")
		registrationArgument := springRegistrationArgument(planned.Discovery)
		if registrationArgument != springRegistrationPath(planned.Discovery) {
			required = append(required, "--"+registrationArgument+"=false")
		}
	}
	required = append(required,
		"--"+listenerKey+"="+listener,
		"--"+portKey+"="+strconv.Itoa(planned.Isolation.ListenerPort),
	)
	result := append([]string(nil), arguments...)
	for _, argument := range required {
		key := strings.SplitN(argument, "=", 2)[0] + "="
		found := false
		for _, existing := range result {
			if strings.HasPrefix(existing, key) {
				found = true
				break
			}
		}
		if !found {
			result = append(result, argument)
		}
	}
	return result
}

func (springBootConsulRuntimeContract) ValidateIsolation(name string, kind string, config *PlannedConfig) error {
	if config.Plan.SourceDriver != materialize.SourceRepository || !springApplicationSupported(config.Plan.Application) {
		return fmt.Errorf("service %s Spring Boot isolation requires repository config source application.yml, application.yaml, or application.properties", name)
	}
	if kind != "rpc" && kind != "http" {
		return fmt.Errorf("service %s kind %s has no trusted Spring Boot Consul local isolation contract", name, kind)
	}
	isolation := config.Isolation
	if config.Discovery == "passive" || config.Discovery == "kubernetes-dns" {
		if isolation.RegistrationMode != "not-applicable" || isolation.RegistrationGuard != nil {
			return fmt.Errorf("service %s Spring Boot %s registration must be not-applicable", name, config.Discovery)
		}
	} else {
		if isolation.RegistrationMode != "config" || isolation.RegistrationGuard == nil {
			return fmt.Errorf("service %s kind %s must verify disabled registration through its final Spring Boot runtime config", name, kind)
		}
		registration := *isolation.RegistrationGuard
		disabled, ok := registration.Value.(bool)
		wantRegistration := springRegistrationPath(config.Discovery)
		if registration.File != config.Plan.Application || registration.Path != wantRegistration || !ok || disabled {
			return fmt.Errorf("service %s Spring Boot %s registration isolation must enforce %s:%s to false", name, config.Discovery, config.Plan.Application, wantRegistration)
		}
	}
	wantListenerPath := "server.address"
	if kind == "rpc" {
		wantListenerPath = "grpc.server.address"
	}
	listener, ok := isolation.ListenerGuard.Value.(string)
	if !ok || isolation.ListenerGuard.File != config.Plan.Application || isolation.ListenerGuard.Path != wantListenerPath {
		return fmt.Errorf("service %s kind %s listener isolation must enforce %s:%s", name, kind, config.Plan.Application, wantListenerPath)
	}
	if err := validateServiceListener(listener, 0, isolationListenerMode(isolation)); err != nil {
		return fmt.Errorf("service %s listener isolation: %w", name, err)
	}
	if net.ParseIP(listener) == nil {
		return fmt.Errorf("service %s kind %s listener isolation must be an IP without a port", name, kind)
	}
	if isolation.ListenerPort < 1 {
		return fmt.Errorf("service %s kind %s listener isolation has no declared local port", name, kind)
	}
	return nil
}

func (springBootConsulRuntimeContract) ValidateRuntimeConfig(name string, config *PlannedConfig, run []string, _ map[string]string) error {
	if len(run) < 3 || filepath.Base(run[0]) != "java" || run[1] != "-jar" {
		return fmt.Errorf("service %s trusted Spring Boot adapter requires a java -jar run command", name)
	}
	kind := "http"
	listenerKey := "server.address"
	portKey := "server.port"
	if config.Isolation.ListenerGuard.Path == "grpc.server.address" {
		kind = "rpc"
		listenerKey = "grpc.server.address"
		portKey = "grpc.server.port"
	}
	listener, ok := config.Isolation.ListenerGuard.Value.(string)
	if !ok {
		return fmt.Errorf("service %s protected Spring Boot listener value must be a string", name)
	}
	expected := map[string]string{
		"spring.config.location": "file:" + filepath.Clean(config.Plan.TargetDir) + string(filepath.Separator),
		listenerKey:               listener,
		portKey:                   strconv.Itoa(config.Isolation.ListenerPort),
	}
	registrationKeys := []string(nil)
	if config.Isolation.RegistrationMode == "config" {
		registrationKeys = append(registrationKeys, springRegistrationPath(config.Discovery))
		registrationArgument := springRegistrationArgument(config.Discovery)
		if registrationArgument != registrationKeys[0] {
			registrationKeys = append(registrationKeys, registrationArgument)
		}
	}
	for _, key := range registrationKeys {
		expected[key] = "false"
	}
	seen := make(map[string]bool, len(expected))
	for index := 3; index < len(run); index++ {
		argument := run[index]
		if !strings.HasPrefix(argument, "--") {
			continue
		}
		keyValue := strings.SplitN(strings.TrimPrefix(argument, "--"), "=", 2)
		want, protected := expected[keyValue[0]]
		if !protected {
			continue
		}
		if len(keyValue) != 2 {
			return fmt.Errorf("service %s protected Spring Boot argument --%s must use --key=value syntax", name, keyValue[0])
		}
		if seen[keyValue[0]] {
			return fmt.Errorf("service %s protected Spring Boot argument --%s is duplicated", name, keyValue[0])
		}
		seen[keyValue[0]] = true
		if keyValue[1] != want {
			return fmt.Errorf("service %s protected Spring Boot argument --%s=%s conflicts with required value %q", name, keyValue[0], keyValue[1], want)
		}
	}
	requiredKeys := []string{"spring.config.location"}
	requiredKeys = append(requiredKeys, registrationKeys...)
	requiredKeys = append(requiredKeys, listenerKey, portKey)
	for _, key := range requiredKeys {
		if !seen[key] {
			return fmt.Errorf("service %s run command is missing protected Spring Boot argument --%s=%s for %s isolation", name, key, expected[key], kind)
		}
	}
	config.Isolation.RuntimeConfigRef = "spring-config-location(" + config.Plan.Application + ")"
	return nil
}

func (springBootConsulRuntimeContract) EffectiveListener(config *PlannedConfig) string {
	return net.JoinHostPort(config.Isolation.ListenerGuard.Value.(string), strconv.Itoa(config.Isolation.ListenerPort))
}

func (springBootConsulRuntimeContract) ExternalDependencies(_ PlannedService, _ string) ([]ExternalConsulDependency, bool, error) {
	return nil, false, nil
}

func springDiscoverySupported(driver string) bool {
	switch driver {
	case "passive", "kubernetes-dns", "consul", "nacos", "eureka":
		return true
	}
	return false
}

func springMaterializerSupported(driver string) bool {
	return driver == string(materialize.DriverYAMLOverlay) || driver == string(materialize.DriverPropertiesOverlay)
}

func springApplicationSupported(application string) bool {
	return application == "application.yml" || application == "application.yaml" || application == "application.properties"
}

func springRegistrationPathSupported(driver string, path string) bool {
	return path == springRegistrationPath(driver)
}

func springRegistrationPath(driver string) string {
	switch driver {
	case "nacos":
		return "spring.cloud.nacos.discovery.register-enabled"
	case "eureka":
		return "eureka.client.register-with-eureka"
	case "passive", "kubernetes-dns":
		return "service.registration.enabled"
	default:
		return "service.registration.enabled"
	}
}

func springRegistrationArgument(driver string) string {
	switch driver {
	case "nacos":
		return "spring.cloud.nacos.discovery.register-enabled"
	case "eureka":
		return "eureka.client.register-with-eureka"
	case "consul":
		return "spring.cloud.consul.discovery.register"
	default:
		return "service.registration.enabled"
	}
}
