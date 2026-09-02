package config

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/leo1394/homebrew-conven/internal/model"
)

type RepositoryCertificationRequest struct {
	Name           string
	Framework      string
	Runtime        string
	Discovery      string
	Kind           string
	Kinds          []string
	ExplicitPolicy string
	Registrations  []RepositoryRegistrationEvidence
	Consumers      []RepositoryConsumerEvidence
}

type RepositoryCertification struct {
	Certifier string
	Policy    string
}

type RepositoryCertifier interface {
	Name() string
	Matches(RepositoryCertificationRequest) bool
	Certify(*model.Manifest, RepositoryCertificationRequest) (string, error)
}

type driverRepositoryCertifier struct {
	name       string
	runtime    string
	framework  string
	discovery  string
	validation func(RepositoryCertificationRequest) error
	policyValidation func(model.Policy) bool
}

var repositoryCertifierRegistry = struct {
	sync.RWMutex
	certifiers map[string]RepositoryCertifier
}{certifiers: make(map[string]RepositoryCertifier)}

func RegisterRepositoryCertifier(certifier RepositoryCertifier) {
	if certifier == nil || strings.TrimSpace(certifier.Name()) == "" {
		panic("repository certifier must have a name")
	}
	repositoryCertifierRegistry.Lock()
	defer repositoryCertifierRegistry.Unlock()
	if _, found := repositoryCertifierRegistry.certifiers[certifier.Name()]; found {
		panic("duplicate repository certifier " + certifier.Name())
	}
	repositoryCertifierRegistry.certifiers[certifier.Name()] = certifier
}

func registerDriverRepositoryCertifier(runtimeName string, validation func(RepositoryCertificationRequest) error, policyValidation ...func(model.Policy) bool) {
	var validatePolicy func(model.Policy) bool
	if len(policyValidation) > 0 {
		validatePolicy = policyValidation[0]
	}
	RegisterRepositoryCertifier(driverRepositoryCertifier{name: runtimeName, runtime: runtimeName, validation: validation, policyValidation: validatePolicy})
}

func (certifier driverRepositoryCertifier) Name() string {
	return certifier.name
}

func (certifier driverRepositoryCertifier) Matches(request RepositoryCertificationRequest) bool {
	if certifier.runtime != "" {
		return request.Runtime == certifier.runtime
	}
	return request.Framework == certifier.framework && request.Discovery == certifier.discovery
}

func (certifier driverRepositoryCertifier) Certify(manifest *model.Manifest, request RepositoryCertificationRequest) (string, error) {
	if certifier.validation != nil {
		if err := certifier.validation(request); err != nil {
			return "", err
		}
	}
	compatible := func(policy model.Policy) bool {
		runtimeName := policy.Drivers.Runtime
		if runtimeName == "" { runtimeName = policy.Drivers.Framework }
		if runtimeName != request.Runtime || policy.Drivers.Discovery != request.Discovery {
			return false
		}
		if certifier.policyValidation != nil && !certifier.policyValidation(policy) {
			return false
		}
		for _, kind := range requestKinds(request) {
			if _, found := policy.Routing.Servers[kind]; !found { return false }
		}
		return true
	}
	if request.ExplicitPolicy != "" {
		policy, found := manifest.Policies[request.ExplicitPolicy]
		if found && compatible(policy) {
			return request.ExplicitPolicy, nil
		}
		return "", fmt.Errorf("discovered %s service %q has incompatible policy %q; configure runtime=%s discovery=%s with server routes %s", request.Framework, request.Name, request.ExplicitPolicy, request.Runtime, request.Discovery, strings.Join(requestKinds(request), ","))
	}
	candidates := make([]string, 0)
	for _, name := range sortedPolicyNames(manifest) {
		if compatible(manifest.Policies[name]) {
			candidates = append(candidates, name)
		}
	}
	if len(candidates) != 1 {
		detail := "none"
		if len(candidates) > 0 {
			detail = strings.Join(candidates, ", ")
		}
		return "", fmt.Errorf("discovered %s service %q requires exactly one compatible runtime=%s discovery=%s policy with server routes %s; candidates: %s", request.Framework, request.Name, request.Runtime, request.Discovery, strings.Join(requestKinds(request), ","), detail)
	}
	return candidates[0], nil
}

func environmentPolicyCompatible(policy model.Policy) bool {
	return policy.Drivers.ConfigSource == "environment" && policy.Drivers.Materializer == "environment"
}

func springPolicyCompatible(policy model.Policy) bool {
	return policy.Drivers.ConfigSource == "repository" && (policy.Drivers.Materializer == "yaml-overlay" || policy.Drivers.Materializer == "properties-overlay")
}

func goZeroPolicyCompatible(policy model.Policy) bool {
	return (policy.Drivers.ConfigSource == "repository" || policy.Drivers.ConfigSource == "apollo") && policy.Drivers.Materializer == "yaml-overlay"
}

func BuiltinRepositoryCertifiers() []RepositoryCertifier {
	repositoryCertifierRegistry.RLock()
	defer repositoryCertifierRegistry.RUnlock()
	names := make([]string, 0, len(repositoryCertifierRegistry.certifiers))
	for name := range repositoryCertifierRegistry.certifiers {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]RepositoryCertifier, 0, len(names))
	for _, name := range names {
		result = append(result, repositoryCertifierRegistry.certifiers[name])
	}
	return result
}

func CertifyRepository(manifest *model.Manifest, request RepositoryCertificationRequest, certifiers ...RepositoryCertifier) (RepositoryCertification, bool, error) {
	if request.Runtime == "" {
		request.Runtime = request.Framework
	}
	if err := certifyKafkaConsumerEvidence(request); err != nil {
		return RepositoryCertification{}, false, err
	}
	if request.Framework == "" || request.Runtime == "" || request.Discovery == "" || len(requestKinds(request)) == 0 {
		return RepositoryCertification{Policy: request.ExplicitPolicy}, false, nil
	}
	if len(certifiers) == 0 {
		certifiers = BuiltinRepositoryCertifiers()
	}
	matches := make([]RepositoryCertifier, 0, 1)
	for _, certifier := range certifiers {
		if certifier == nil {
			return RepositoryCertification{}, false, fmt.Errorf("repository certifier is nil")
		}
		if strings.TrimSpace(certifier.Name()) == "" {
			return RepositoryCertification{}, false, fmt.Errorf("repository certifier name is empty")
		}
		if certifier.Matches(request) {
			matches = append(matches, certifier)
		}
	}
	if len(matches) == 0 {
		return RepositoryCertification{}, false, fmt.Errorf("discovered %s service %q has no repository certifier for runtime %q, discovery %q, and kinds %s", request.Framework, request.Name, request.Runtime, request.Discovery, strings.Join(requestKinds(request), ","))
	}
	if len(matches) > 1 {
		names := make([]string, 0, len(matches))
		for _, certifier := range matches {
			names = append(names, certifier.Name())
		}
		return RepositoryCertification{}, false, fmt.Errorf("discovered service %q matched multiple repository certifiers: %s", request.Name, strings.Join(names, ", "))
	}
	policy, err := matches[0].Certify(manifest, request)
	if err != nil {
		return RepositoryCertification{}, false, err
	}
	return RepositoryCertification{Certifier: matches[0].Name(), Policy: policy}, true, nil
}

func certifyKafkaConsumerEvidence(request RepositoryCertificationRequest) error {
	for _, evidence := range request.Consumers {
		if evidence.Driver != "kafka" || evidence.Protected {
			continue
		}
		return fmt.Errorf(`%s service %q creates a Kafka consumer at %s without a trusted runtime guard; services --registry made no changes

Guard consumer construction with the neutral switch below. Conven currently defaults %s=true; the guard is required so a future certified local-routing policy can disable remote membership without another source change.

Go:
if strings.EqualFold(os.Getenv("%s"), "false") {
    return []service.Service{}, nil
}

Spring Boot:
@ConditionalOnProperty(
    prefix = "service.kafka.consumers",
    name = "enabled",
    havingValue = "true",
    matchIfMissing = true
)

Node/Bun:
if (process.env.%s !== "false") {
  await startKafkaConsumers();
}

Python:
if os.getenv("%s", "true").lower() != "false":
    start_kafka_consumers()`, request.Framework, request.Name, evidence.File, KafkaConsumersEnabledEnv, KafkaConsumersEnabledEnv, KafkaConsumersEnabledEnv, KafkaConsumersEnabledEnv)
	}
	return nil
}

func ValidateKafkaConsumerEvidence(name string, framework string, evidence []RepositoryConsumerEvidence) error {
	return certifyKafkaConsumerEvidence(RepositoryCertificationRequest{Name: name, Framework: framework, Consumers: evidence})
}

func requestKinds(request RepositoryCertificationRequest) []string {
	if len(request.Kinds) > 0 { return request.Kinds }
	if request.Kind != "" && request.Kind != RepositoryKindUnknown { return []string{request.Kind} }
	return nil
}

func certifyRegistrationEvidence(request RepositoryCertificationRequest) error {
	if request.Discovery != "passive" && request.Discovery != "kubernetes-dns" && len(request.Registrations) == 0 {
		return fmt.Errorf("%s service %q uses %s discovery but Analyzer could not locate the provider registration and deregistration path; wrap those calls in the neutral SERVICE_REGISTRATION_ENABLED guard below, or use a passive/Kubernetes DNS policy if this process does not register\n\n%s", request.Framework, request.Name, request.Discovery, neutralRegistrationGuardExamples())
	}
	for _, evidence := range request.Registrations {
		if evidence.Protected { continue }
		return fmt.Errorf("custom %s registration in %s cannot be trusted; the registration and deregistration calls must be inside a neutral SERVICE_REGISTRATION_ENABLED guard\n\n%s\n\nThe default-enabled branch preserves deployed behavior; Conven injects false for local runs", evidence.Provider, evidence.File, neutralRegistrationGuardExamples())
	}
	return nil
}

func neutralRegistrationGuardExamples() string {
	return `Go:
if !strings.EqualFold(os.Getenv("SERVICE_REGISTRATION_ENABLED"), "false") {
    register()
}

Node/Bun:
if (process.env.SERVICE_REGISTRATION_ENABLED !== "false") {
  await registerService();
}

Python:
if os.getenv("SERVICE_REGISTRATION_ENABLED", "true").lower() != "false":
    register_service()`
}

func certifyGoZeroRegistrationEvidence(request RepositoryCertificationRequest) error {
	for _, evidence := range request.Registrations {
		if !evidence.Protected {
			return fmt.Errorf("go-zero service %q has custom %s registration in %s that bypasses the discovType isolation contract; guard the registration and deregistration path with SERVICE_REGISTRATION_ENABLED or remove the custom registration\n\n%s", request.Name, evidence.Provider, evidence.File, neutralRegistrationGuardExamples())
		}
	}
	return nil
}

func certifySpringRegistration(request RepositoryCertificationRequest) error {
	for _, evidence := range request.Registrations {
		if evidence.Provider != "consul" || evidence.Protected {
			continue
		}
		return fmt.Errorf(`Spring Boot service %q has custom Consul registration in %s that cannot be trusted because Conven could not verify a service.registration.enabled condition on the class that performs registration

Add this import and condition to that class:

import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;

@ConditionalOnProperty(
    prefix = "service.registration",
    name = "enabled",
    havingValue = "true",
    matchIfMissing = true
)
public class ConsulConfig {
    // existing registration and deregistration code
}

Conven passes --service.registration.enabled=false for local runs; matchIfMissing=true preserves existing deployments`, request.Name, evidence.File)
	}
	return nil
}
