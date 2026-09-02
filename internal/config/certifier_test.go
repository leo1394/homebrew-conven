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
	for _, expected := range []string{"orders-service", "internal/registry/client.go:42", "SERVICE_REGISTRATION_ENABLED", "Go:", "Node/Bun:", "Python:"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("certification error is missing %q: %v", expected, err)
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

func TestCertifierRejectsUnguardedKafkaConsumerWithFix(t *testing.T) {
	err := certifyKafkaConsumerEvidence(RepositoryCertificationRequest{
		Name:      "visit-plan-mgr-service",
		Framework: "go-zero",
		Consumers: []RepositoryConsumerEvidence{{
			Driver: "kafka",
			File:   "handler/kq_routes.go:14",
		}},
	})
	if err == nil {
		t.Fatal("unguarded Kafka consumer was certified")
	}
	for _, expected := range []string{"visit-plan-mgr-service", "handler/kq_routes.go:14", "SERVICE_KAFKA_CONSUMERS_ENABLED", "strings.EqualFold"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("Kafka consumer certification error is missing %q: %v", expected, err)
		}
	}
}
