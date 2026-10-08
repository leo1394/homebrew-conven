package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/materialize"
	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestSpringProdConfigUsesBaseAndOptionalProfile(t *testing.T) {
	for _, extension := range []string{"yaml", "yml"} {
		for _, profile := range []bool{false, true} {
			name := extension + "/base-only"
			if profile { name = extension + "/with-prod" }
			t.Run(name, func(t *testing.T) {
				root := t.TempDir()
				source := filepath.Join(root, "api", "resources")
				if err := os.MkdirAll(source, 0700); err != nil { t.Fatal(err) }
				application := "application." + extension
				profileFile := "application-prod." + extension
				base := "service:\n  registration:\n    enabled: true\nspring:\n  profiles:\n    active: dev\nmarker: base\n"
				prod := "marker: prod\nserver:\n  port: 8098\n"
				if err := os.WriteFile(filepath.Join(source, application), []byte(base), 0600); err != nil { t.Fatal(err) }
				if profile {
					if err := os.WriteFile(filepath.Join(source, profileFile), []byte(prod), 0600); err != nil { t.Fatal(err) }
				}
				store, err := NewStore(root); if err != nil { t.Fatal(err) }
				manifest := &model.Manifest{
					Version: 3,
					Environments: map[string]model.Environment{"prod": {}},
					Policies: map[string]model.Policy{"spring": {
						Drivers: model.PolicyDrivers{Runtime: "spring-boot", ConfigSource: "repository", Discovery: "consul", Materializer: "yaml-overlay"},
						Config: model.PolicyConfig{SourceDir: "resources", Application: application},
						Process: model.PolicyProcess{Args: []string{"--spring.profiles.active=${env}"}},
						Routing: model.PolicyRouting{Servers: map[string]model.ServerRoute{"http": {
							Port: "http",
							Isolation: model.ServerIsolation{
								Registration: model.RegistrationGuard{Mode: "config", Path: "service.registration.enabled", DisabledValue: false},
								Listener: model.ListenerGuard{Path: "server.address", Value: "127.0.0.1"},
							},
						}}},
					}},
					Services: map[string]model.Service{"api": {Path: "api", Policy: "spring", Kind: "http", Runner: model.Runner{Run: []string{"java", "-jar", "api.jar"}}, Ports: map[string]int{"http": 18098}}},
				}
				plan, err := BuildPlan(&WorkspaceData{Root: root, Manifest: manifest, Store: store}, CommonOptions{Environment: "prod"}, []string{"api"})
				if err != nil { t.Fatal(err) }
				service := plan.Services["api"]
				if !strings.Contains(strings.Join(service.Run, "\n"), "--spring.profiles.active=prod\n") { t.Fatalf("prod profile not activated: %v", service.Run) }
				if err := os.MkdirAll(service.Config.Plan.ConfigRoot, 0700); err != nil { t.Fatal(err) }
				if err := materialize.Materialize(context.Background(), service.Config.Plan); err != nil { t.Fatal(err) }
				if _, err := os.Stat(filepath.Join(service.Config.Plan.TargetDir, application)); err != nil { t.Fatal(err) }
				if profile {
					data, err := os.ReadFile(filepath.Join(service.Config.Plan.TargetDir, profileFile)); if err != nil { t.Fatal(err) }
					if string(data) != prod { t.Fatalf("prod config changed: %s", data) }
				}
				data, err := os.ReadFile(filepath.Join(source, application)); if err != nil { t.Fatal(err) }
				if string(data) != base { t.Fatal("repository base config was modified") }
			})
		}
	}
}
