package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestCertifyRepositoryRejectsUnprotectedSpringRegistrationWithExample(t *testing.T) {
	manifest := &model.Manifest{Policies: map[string]model.Policy{
		"spring-boot-consul": springBootCertifierPolicy("rpc"),
	}}
	_, certified, err := CertifyRepository(manifest, RepositoryCertificationRequest{
		Name:      "data-mart-service",
		Framework: "spring-boot",
		Discovery: "consul",
		Kind:      "rpc",
		Registrations: []RepositoryRegistrationEvidence{{
			Provider: "consul",
			File:     "src/main/java/ConsulConfig.java",
		}},
	})
	if err == nil || certified {
		t.Fatalf("unprotected registration certification = %t, error = %v", certified, err)
	}
	for _, expected := range []string{
		"custom Consul registration",
		"src/main/java/ConsulConfig.java",
		"@ConditionalOnProperty(",
		`prefix = "service.registration"`,
		"matchIfMissing = true",
		"--service.registration.enabled=false",
	} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("certification error is missing %q: %v", expected, err)
		}
	}
}

func TestCertifyRepositorySelectsUniqueCompatiblePolicy(t *testing.T) {
	manifest := &model.Manifest{Policies: map[string]model.Policy{
		"go-workspace":       {},
		"spring-boot-consul": springBootCertifierPolicy("rpc"),
	}}
	certification, certified, err := CertifyRepository(manifest, RepositoryCertificationRequest{
		Name:      "data-mart-service",
		Framework: "spring-boot",
		Discovery: "consul",
		Kind:      "rpc",
		Registrations: []RepositoryRegistrationEvidence{{
			Provider:  "consul",
			File:      "src/main/java/ConsulConfig.java",
			Protected: true,
		}},
	})
	if err != nil || !certified {
		t.Fatalf("certification = %#v, certified = %t, error = %v", certification, certified, err)
	}
	if certification.Certifier != "spring-boot" || certification.Policy != "spring-boot-consul" {
		t.Fatalf("certification = %#v", certification)
	}
}

func TestCertifyRepositoryUsesSuppliedImplementationWithoutCoreChanges(t *testing.T) {
	implementation := testRepositoryCertifier{name: "python-http", framework: "python", policy: "python-local"}
	certification, certified, err := CertifyRepository(&model.Manifest{}, RepositoryCertificationRequest{
		Name:      "customer-service",
		Framework: "python",
		Discovery: "none",
		Kind:      "http",
	}, implementation)
	if err != nil || !certified {
		t.Fatalf("certification = %#v, certified = %t, error = %v", certification, certified, err)
	}
	if certification.Certifier != implementation.name || certification.Policy != implementation.policy {
		t.Fatalf("certification = %#v", certification)
	}
}

func TestCertifyRepositoryAllowsUnguardedKafkaConsumerByDefault(t *testing.T) {
	implementation := testRepositoryCertifier{name: "go-local", framework: "go-zero", policy: "go-local"}
	certification, certified, err := CertifyRepository(&model.Manifest{}, RepositoryCertificationRequest{
		Name:      "events",
		Framework: "go-zero",
		Discovery: "consul",
		Kind:      "rpc",
		Consumers: []RepositoryConsumerEvidence{{Driver: "kafka", File: "handler/kq_routes.go:14"}},
	}, implementation)
	if err != nil || !certified {
		t.Fatalf("certification = %#v, certified = %t, error = %v", certification, certified, err)
	}
}

func TestCertifyRepositoryRejectsMultipleImplementations(t *testing.T) {
	request := RepositoryCertificationRequest{Name: "customer-service", Framework: "python", Discovery: "none", Kind: "http"}
	first := testRepositoryCertifier{name: "python-a", framework: "python", policy: "python-local"}
	second := testRepositoryCertifier{name: "python-b", framework: "python", policy: "python-local"}
	_, certified, err := CertifyRepository(&model.Manifest{}, request, first, second)
	if err == nil || certified || !strings.Contains(err.Error(), "matched multiple repository certifiers: python-a, python-b") {
		t.Fatalf("multiple certification = %t, error = %v", certified, err)
	}
}

func TestEnvironmentCertifierRejectsUnprovenRegistryLifecycle(t *testing.T) {
	err := certifyRegistrationEvidence(RepositoryCertificationRequest{
		Name: "orders",
		Framework: "kratos",
		Discovery: "consul",
	})
	if err == nil {
		t.Fatal("non-passive service without registration evidence was certified")
	}
	for _, expected := range []string{"orders", "registration and deregistration", "SERVICE_REGISTRATION_ENABLED", "passive/Kubernetes DNS"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("certification error is missing %q: %v", expected, err)
		}
	}
}

func TestGoZeroCertifierReportsRepositoryLocationAndFix(t *testing.T) {
	err := certifyGoZeroRegistrationEvidence(RepositoryCertificationRequest{
		Name: "orders-service",
		Registrations: []RepositoryRegistrationEvidence{{
			Provider: "consul",
			File: "internal/registry/client.go:42",
		}},
	})
	if err == nil {
		t.Fatal("unguarded go-zero custom registration was certified")
	}
	for _, expected := range []string{"orders-service", "internal/registry/client.go:42", "SERVICE_REGISTRATION_ENABLED", "Go example:"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("certification error is missing %q: %v", expected, err)
		}
	}
	for _, unexpected := range []string{"Node.js example:", "Bun example:", "Python example:"} {
		if strings.Contains(err.Error(), unexpected) {
			t.Fatalf("go-zero registration error includes unrelated %q guidance: %v", unexpected, err)
		}
	}
}

func TestDynamicRegistrationEvidenceTracksEveryCallAndLocation(t *testing.T) {
	javascript := `if (process.env.SERVICE_REGISTRATION_ENABLED !== "false") {
  registerService();
}
deregisterService();
`
	evidence := registrationEvidenceFromText("src/main.ts", javascript, "consul")
	if len(evidence) != 2 || !evidence[0].Protected || evidence[1].Protected || evidence[0].File != "src/main.ts:2" || evidence[1].File != "src/main.ts:4" {
		t.Fatalf("JavaScript registration evidence = %#v", evidence)
	}
	python := `if os.getenv("SERVICE_REGISTRATION_ENABLED", "true").lower() != "false":
    register_service()
    deregister_service()
`
	evidence = registrationEvidenceFromText("app.py", python, "consul")
	if len(evidence) != 2 || !evidence[0].Protected || !evidence[1].Protected {
		t.Fatalf("Python registration evidence = %#v", evidence)
	}
}

func springBootCertifierPolicy(kind string) model.Policy {
	return model.Policy{
		Drivers: model.PolicyDrivers{
			Framework:    "spring-boot",
			ConfigSource: "repository",
			Discovery:    "consul",
			Materializer: "yaml-overlay",
		},
		Routing: model.PolicyRouting{Servers: map[string]model.ServerRoute{
			kind: {Port: kind},
		}},
	}
}

type testRepositoryCertifier struct {
	name      string
	framework string
	policy    string
	err       error
}

func (certifier testRepositoryCertifier) Name() string {
	return certifier.name
}

func (certifier testRepositoryCertifier) Matches(request RepositoryCertificationRequest) bool {
	return request.Framework == certifier.framework
}

func (certifier testRepositoryCertifier) Certify(_ *model.Manifest, _ RepositoryCertificationRequest) (string, error) {
	if certifier.err != nil {
		return "", certifier.err
	}
	if certifier.policy == "" {
		return "", errors.New("test certifier has no policy")
	}
	return certifier.policy, nil
}

func TestDisabledKafkaConsumerValidationReportsLanguageSpecificFix(t *testing.T) {
	err := certifyDisabledKafkaConsumerEvidence(RepositoryCertificationRequest{
		Name:      "visit-plan-mgr-service",
		Framework: "go-zero",
		Consumers: []RepositoryConsumerEvidence{{
			Driver: "kafka",
			File:   "handler/kq_routes.go:14",
		}},
	})
	if err == nil {
		t.Fatal("unguarded Kafka consumer was accepted while disabling consumers")
	}
	for _, expected := range []string{"visit-plan-mgr-service", "handler/kq_routes.go:14", "SERVICE_KAFKA_CONSUMERS_ENABLED", "strings.EqualFold"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("Kafka consumer certification error is missing %q: %v", expected, err)
		}
	}
	for _, unexpected := range []string{"@ConditionalOnProperty(", "process.env.", "Bun.env.", "os.getenv("} {
		if strings.Contains(err.Error(), unexpected) {
			t.Fatalf("Go Kafka consumer certification error includes unrelated %q guidance: %v", unexpected, err)
		}
	}
}

func TestKafkaConsumerGuardFixMatchesServiceLanguage(t *testing.T) {
	for _, test := range []struct {
		framework  string
		runtime    string
		want       string
		unexpected string
	}{
		{framework: "spring-boot", want: "@ConditionalOnProperty(", unexpected: "strings.EqualFold"},
		{framework: "nestjs", want: "process.env.SERVICE_KAFKA_CONSUMERS_ENABLED", unexpected: "@ConditionalOnProperty("},
		{framework: "bun-serve", want: "Bun.env.SERVICE_KAFKA_CONSUMERS_ENABLED", unexpected: "process.env."},
		{framework: "fastapi", want: "os.getenv(\"SERVICE_KAFKA_CONSUMERS_ENABLED\"", unexpected: "strings.EqualFold"},
		{framework: "hono", runtime: "node-http", want: "process.env.SERVICE_KAFKA_CONSUMERS_ENABLED", unexpected: "Bun.env."},
		{framework: "hono", runtime: "bun-http", want: "Bun.env.SERVICE_KAFKA_CONSUMERS_ENABLED", unexpected: "process.env."},
	} {
		name := test.framework
		if test.runtime != "" {
			name += "-" + test.runtime
		}
		t.Run(name, func(t *testing.T) {
			err := certifyDisabledKafkaConsumerEvidence(RepositoryCertificationRequest{
				Name:      "events",
				Framework: test.framework,
				Runtime:   test.runtime,
				Consumers: []RepositoryConsumerEvidence{{Driver: "kafka", File: "consumer:1"}},
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Kafka consumer certification error = %v, want %q", err, test.want)
			}
			if strings.Contains(err.Error(), test.unexpected) {
				t.Fatalf("Kafka consumer certification error includes unrelated %q guidance: %v", test.unexpected, err)
			}
		})
	}
}
