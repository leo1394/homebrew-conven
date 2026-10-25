package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/materialize"
	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestRegistryObservationUsesMaterializedProvider(t *testing.T) {
	dir := t.TempDir()
	service := PlannedService{RegistryIdentity: "example.rpc", Registry: &model.Registry{Driver: "consul", Address: "http://stale.example:8500"}, Config: &PlannedConfig{Runtime: "go-zero", Discovery: "consul", Plan: materialize.Plan{Driver: materialize.DriverYAMLOverlay, TargetDir: dir, Application: "application.yaml"}}}
	if err := os.WriteFile(filepath.Join(dir, "application.yaml"), []byte("discovType: ''\nconsul: {host: current.example, port: 8500, key: example.rpc}\n"), 0600); err != nil { t.Fatal(err) }
	got, err := effectiveObservationRegistry(service)
	if err != nil || got.Address != "http://current.example:8500" { t.Fatalf("effective registry: %+v %v", got, err) }
	if service.Registry.Address != "http://stale.example:8500" { t.Fatal("mutated manifest declaration") }
	service.RegistryIdentity = "other.rpc"
	if _, err := effectiveObservationRegistry(service); err == nil { t.Fatal("accepted wrong identity") }
	service.RegistryIdentity = "example.rpc"
	service.Registry.TokenEnv = "CONSUL_TOKEN"
	if _, err := effectiveObservationRegistry(service); err == nil { t.Fatal("redirected credentials") }
	service.Registry.TokenEnv = ""
	service.Registry.Address = "https://current.example:8500"
	if _, err := effectiveObservationRegistry(service); err == nil { t.Fatal("downgraded TLS declaration") }
}
