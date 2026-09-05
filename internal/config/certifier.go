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

func certifyDisabledKafkaConsumerEvidence(request RepositoryCertificationRequest) error {
	for _, evidence := range request.Consumers {
		if evidence.Driver != "kafka" || evidence.Protected {
			continue
		}
		return fmt.Errorf(`%s service %q cannot disable Kafka consumers because its current source has no trusted runtime guard
  - Source: %s
  - Required switch: %s
  => Add this guard before the consumer is created:

%s`, request.Framework, request.Name, evidence.File, KafkaConsumersEnabledEnv, kafkaConsumerGuardExample(request.Framework, request.Runtime))
	}
	return nil
}

func kafkaConsumerGuardExample(framework string, runtimeName string) string {
	switch serviceLanguage(framework, runtimeName) {
	case "go":
		return `  Go example:
      if strings.EqualFold(os.Getenv("SERVICE_KAFKA_CONSUMERS_ENABLED"), "false") {
          return nil, nil
      }`
	case "java":
		return `  Spring Boot example:
      @ConditionalOnProperty(
          prefix = "service.kafka.consumers",
          name = "enabled",
          havingValue = "true",
          matchIfMissing = true
      )`
	case "bun":
		return `  Bun example:
      if (Bun.env.SERVICE_KAFKA_CONSUMERS_ENABLED !== "false") {
        await startKafkaConsumers();
      }`
	case "node":
		return `  Node.js example:
      if (process.env.SERVICE_KAFKA_CONSUMERS_ENABLED !== "false") {
        await startKafkaConsumers();
      }`
	case "python":
		return `  Python example:
      if os.getenv("SERVICE_KAFKA_CONSUMERS_ENABLED", "true").lower() != "false":
          start_kafka_consumers()`
	default:
		return `  Runtime contract:
      Skip consumer construction when SERVICE_KAFKA_CONSUMERS_ENABLED=false.`
	}
}

func ValidateDisabledKafkaConsumerEvidence(name string, framework string, evidence []RepositoryConsumerEvidence) error {
	return certifyDisabledKafkaConsumerEvidence(RepositoryCertificationRequest{Name: name, Framework: framework, Consumers: evidence})
}

func requestKinds(request RepositoryCertificationRequest) []string {
	if len(request.Kinds) > 0 { return request.Kinds }
	if request.Kind != "" && request.Kind != RepositoryKindUnknown { return []string{request.Kind} }
	return nil
}

func certifyRegistrationEvidence(request RepositoryCertificationRequest) error {
	if request.Discovery != "passive" && request.Discovery != "kubernetes-dns" && len(request.Registrations) == 0 {
		return fmt.Errorf(`%s service %q cannot be certified because its registration and deregistration path was not found
  - Discovery: %s
  - Required switch: SERVICE_REGISTRATION_ENABLED
  => Guard the registration lifecycle:

%s

  => If this process does not register, use a passive/Kubernetes DNS policy`, request.Framework, request.Name, request.Discovery, neutralRegistrationGuardExample(request.Framework, request.Runtime))
	}
	for _, evidence := range request.Registrations {
		if evidence.Protected { continue }
		return fmt.Errorf(`custom %s registration cannot be certified because it has no trusted runtime guard
  - Source: %s
  - Required switch: SERVICE_REGISTRATION_ENABLED
  => Guard both registration and deregistration:

%s

The default-enabled branch preserves deployed behavior; Conven injects false for local runs`, evidence.Provider, evidence.File, neutralRegistrationGuardExample(request.Framework, request.Runtime))
	}
	return nil
}

func neutralRegistrationGuardExample(framework string, runtimeName string) string {
	switch serviceLanguage(framework, runtimeName) {
	case "go":
		return `  Go example:
      if !strings.EqualFold(os.Getenv("SERVICE_REGISTRATION_ENABLED"), "false") {
          register()
      }`
	case "bun":
		return `  Bun example:
      if (Bun.env.SERVICE_REGISTRATION_ENABLED !== "false") {
        await registerService();
      }`
	case "node":
		return `  Node.js example:
      if (process.env.SERVICE_REGISTRATION_ENABLED !== "false") {
        await registerService();
      }`
	case "python":
		return `  Python example:
      if os.getenv("SERVICE_REGISTRATION_ENABLED", "true").lower() != "false":
          register_service()`
	default:
		return `  Runtime contract:
      Skip registration and deregistration when SERVICE_REGISTRATION_ENABLED=false.`
	}
}

func serviceLanguage(framework string, runtimeName string) string {
	switch strings.ToLower(strings.TrimSpace(runtimeName)) {
	case "go-zero", "go-generic", "kratos", "hertz", "kitex":
		return "go"
	case "spring-boot", "quarkus", "micronaut":
		return "java"
	case "asgi-uvicorn", "wsgi-gunicorn":
		return "python"
	case "bun-http":
		return "bun"
	case "nestjs", "node-http":
		return "node"
	}
	switch strings.ToLower(strings.TrimSpace(framework)) {
	case "go-zero", "go-generic", "kratos", "hertz", "kitex":
		return "go"
	case "spring-boot", "quarkus", "micronaut":
		return "java"
	case "fastapi", "starlette", "flask", "django":
		return "python"
	case "bun-serve", "elysia", "hono":
		return "bun"
	case "nestjs", "express", "fastify":
		return "node"
	default:
		return ""
	}
}

func certifyGoZeroRegistrationEvidence(request RepositoryCertificationRequest) error {
	for _, evidence := range request.Registrations {
		if !evidence.Protected {
			return fmt.Errorf(`go-zero service %q cannot be certified because custom %s registration bypasses the discovType isolation contract
  - Source: %s
  - Required switch: SERVICE_REGISTRATION_ENABLED
  => Guard registration and deregistration, or remove the custom registration:

%s`, request.Name, evidence.Provider, evidence.File, neutralRegistrationGuardExample("go-zero", "go-zero"))
		}
	}
	return nil
}

func certifySpringRegistration(request RepositoryCertificationRequest) error {
	for _, evidence := range request.Registrations {
		if evidence.Provider != "consul" || evidence.Protected {
			continue
		}
		return fmt.Errorf(`Spring Boot service %q cannot be certified because its custom Consul registration has no trusted runtime guard
  - Source: %s
  - Required property: service.registration.enabled
  => Add this condition to the class that performs registration:

  Spring Boot example:
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
