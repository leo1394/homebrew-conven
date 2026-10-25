package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/config"
	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestKafkaExistingEnvironmentGuardDoesNotModifySource(t *testing.T) {
	t.Setenv(config.KafkaConsumersEnabledEnv, "")
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	source := "package main\nimport (\"os\"; \"strings\")\nfunc start() { if strings.EqualFold(os.Getenv(\"APP_CONSUMERS_ENABLED\"), \"false\") { return }; kafka.NewReader(config) }\n"
	if err := os.WriteFile(path, []byte(source), 0600); err != nil { t.Fatal(err) }
	service := model.Service{Discovery: model.ServiceDiscovery{Consumers: []string{"kafka"}}, Isolation: model.ServiceIsolation{Consumers: map[string]model.ConsumerIsolation{
		"kafka": {Mode: "guarded", Env: "APP_CONSUMERS_ENABLED"},
	}}}
	environment := map[string]string{"APP_CONSUMERS_ENABLED": "true"}
	if err := validatePlannedConsumerIsolation("api", "go-zero", root, ".", service, environment); err != nil { t.Fatal(err) }
	evidence, err := applyProtectedConsumerIsolation("api", service, environment)
	if err != nil || environment["APP_CONSUMERS_ENABLED"] != "false" || evidence["kafka"].Status != "disabled" { t.Fatalf("guard: %v %v %v", environment, evidence, err) }
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != source { t.Fatal("source was changed") }
}

func TestKafkaSpringPropertyIsProtected(t *testing.T) {
	t.Setenv(config.KafkaConsumersEnabledEnv, "")
	service := model.Service{Isolation: model.ServiceIsolation{Consumers: map[string]model.ConsumerIsolation{
		"kafka": {Mode: "guarded", Property: "app.kafka.enabled"},
	}}}
	argv := []string{"java", "-jar", "app.jar"}
	result, err := protectedKafkaConsumerArguments("api", "spring-boot", service, argv, nil)
	if err != nil || result[len(result)-1] != "--app.kafka.enabled=false" { t.Fatalf("args: %v %v", result, err) }
	if len(argv) != 3 { t.Fatal("original args mutated") }
	for _, argument := range []string{"--app.kafka.enabled", "--app.kafka.enabled=true", "-Dapp.kafka.enabled=false"} {
		if _, err := protectedKafkaConsumerArguments("api", "spring-boot", service, append(argv, argument), nil); err == nil { t.Fatalf("accepted duplicate %s", argument) }
	}
	if _, err := protectedKafkaConsumerArguments("api", "go-zero", service, argv, nil); err == nil || !strings.Contains(err.Error(), "no consumer property guard adapter") { t.Fatalf("wrong adapter: %v", err) }
	t.Setenv(config.KafkaConsumersEnabledEnv, "true")
	result, err = protectedKafkaConsumerArguments("api", "spring-boot", service, argv, nil)
	if err != nil || result[len(result)-1] != "--app.kafka.enabled=true" { t.Fatalf("explicit shared: %v %v", result, err) }
}

func TestKafkaSpringDefaultEnvironmentGuardProtectsProperty(t *testing.T) {
	t.Setenv(config.KafkaConsumersEnabledEnv, "")
	service := model.Service{Isolation: model.ServiceIsolation{Consumers: map[string]model.ConsumerIsolation{
		"kafka": {Mode: "guarded", Env: config.KafkaConsumersEnabledEnv},
	}}}
	argv := []string{"java", "-jar", "app.jar"}
	result, err := protectedKafkaConsumerArguments("api", "spring-boot", service, argv, nil)
	if err != nil || result[len(result)-1] != "--service.kafka.consumers.enabled=false" { t.Fatalf("args: %v %v", result, err) }
	for _, command := range [][]string{
		{"java", "-jar", "app.jar", "--service.kafka.consumers.enabled=true"},
		{"java", "-Dservice.kafka.consumers.enabled=true", "-jar", "app.jar"},
		{"sh", "-c", "exec java -jar app.jar"},
	} {
		if _, err := protectedKafkaConsumerArguments("api", "spring-boot", service, command, nil); err == nil { t.Fatalf("accepted unsafe args %v", command) }
	}
}

func TestKafkaPropertyProofRequiresDeliveryAdapter(t *testing.T) {
	t.Setenv(config.KafkaConsumersEnabledEnv, "")
	root := t.TempDir()
	source := `@ConditionalOnProperty(prefix="service.kafka.consumers", name="enabled", havingValue="true", matchIfMissing=true)
class Events { @KafkaListener(topics="events") public void receive(String value) {} }`
	if err := os.WriteFile(filepath.Join(root, "Events.java"), []byte(source), 0600); err != nil { t.Fatal(err) }
	service := model.Service{Discovery: model.ServiceDiscovery{Consumers: []string{"kafka"}}, Isolation: model.ServiceIsolation{Consumers: map[string]model.ConsumerIsolation{
		"kafka": {Mode: "guarded", Env: config.KafkaConsumersEnabledEnv},
	}}}
	for _, runtimeName := range []string{"", "generic-runner"} {
		if err := validatePlannedConsumerIsolation("api", runtimeName, root, ".", service, nil); err == nil || !strings.Contains(err.Error(), "property-capable") { t.Fatalf("runtime %q: %v", runtimeName, err) }
	}
	if err := validatePlannedConsumerIsolation("api", "spring-boot", root, ".", service, nil); err != nil { t.Fatal(err) }
}

func TestKafkaUndeclaredWrapperFailsBeforeStartup(t *testing.T) {
	t.Setenv(config.KafkaConsumersEnabledEnv, "")
	root := t.TempDir()
	source := []byte("package events\nfunc start() { reader := &mq.KafkaReader{}; reader.Init(\"shared\", \"events\") }\n")
	path := filepath.Join(root, "events.go")
	if err := os.WriteFile(path, source, 0600); err != nil { t.Fatal(err) }
	err := validatePlannedConsumerIsolation("events", "go-zero", root, ".", model.Service{}, nil)
	if err == nil || !strings.Contains(err.Error(), "no discovery.consumers entry") { t.Fatalf("undeclared wrapper error: %v", err) }
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != string(source) { t.Fatal("source was changed") }
	if err := validateConsumerWithBoundary(false, "events", "go-zero", root, ".", model.Service{}, nil); err == nil { t.Fatal("ordinary startup lost its source guard") }
	if err := validateConsumerWithBoundary(true, "events", "go-zero", root, ".", model.Service{}, nil); err != nil { t.Fatalf("deny-all boundary should not require source changes: %v", err) }
}
