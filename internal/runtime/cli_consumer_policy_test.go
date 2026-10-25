package runtime

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestCLIConsumerPolicyDoesNotRequireOrInjectKafkaSwitch(t *testing.T) {
	for _, value := range []string{"", "false", "true", "invalid"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("SERVICE_KAFKA_CONSUMERS_ENABLED", value)
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "worker"), 0700); err != nil { t.Fatal(err) }
			if err := os.WriteFile(filepath.Join(root, "worker", "main.go"), []byte("package main\nfunc main() { kafka.NewReader(config) }\n"), 0600); err != nil { t.Fatal(err) }
			store, err := NewStore(root); if err != nil { t.Fatal(err) }
			command := []string{"worker", "--application-option"}
			manifest := &model.Manifest{Version:2, Services:map[string]model.Service{
				"worker": {Path:"worker", Runner:model.Runner{Run:command}, Discovery:model.ServiceDiscovery{Consumers:[]string{"kafka"}}, Isolation:model.ServiceIsolation{Consumers:map[string]model.ConsumerIsolation{"kafka":{Mode:"guarded", Env:"APP_CONSUMERS_ENABLED"}}}},
			}}
			plan, err := BuildPlan(&WorkspaceData{Root:root, Manifest:manifest, Store:store}, CommonOptions{}, []string{"worker"})
			if err != nil { t.Fatal(err) }
			service := plan.Services["worker"]
			if !reflect.DeepEqual(service.Run, command) { t.Fatalf("consumer args injected: %v", service.Run) }
			if _, found := plannedEnvironment(service.Environment)["APP_CONSUMERS_ENABLED"]; found { t.Fatal("application consumer switch injected") }
			if service.ConsumerIsolation["kafka"].Status != "unrestricted" { t.Fatalf("incorrect isolation evidence: %v", service.ConsumerIsolation) }
		})
	}
}
