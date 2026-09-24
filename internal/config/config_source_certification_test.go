package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestConfigSourceSelectionUsesPolicyNotBootstrapConvention(t *testing.T) {
	for _, flag := range []string{"true", "false", "custom-value"} {
		t.Run(flag, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.WriteFile(filepath.Join(directory, "config-test.yaml"), []byte("localConfigEnable: "+flag+"\n"), 0600); err != nil { t.Fatal(err) }
			remote := model.Policy{Drivers: model.PolicyDrivers{Runtime:"go-zero",Framework:"go-zero",Discovery:"consul",ConfigSource:"apollo",Materializer:"yaml-overlay"},Config:model.PolicyConfig{SourceDir:directory,Bootstrap:"config-${env}.yaml"},Routing:model.PolicyRouting{Servers:map[string]model.ServerRoute{"http":{}}}}
			local := remote
			local.Drivers.ConfigSource = "repository"
			manifest := &model.Manifest{Version:3,Environments:map[string]model.Environment{"test":{}},Policies:map[string]model.Policy{"remote":remote,"local":local}}
			service := DiscoveredService{Name:"api",Framework:"go-zero",Runtime:"go-zero",DiscoveryDriver:"consul",Kind:"http"}
			for _, explicit := range []string{"remote", "local"} {
				got, _, err := certifyDiscoveredService(manifest, service, explicit)
				if err != nil || got != explicit { t.Fatalf("policy=%q error=%v",got,err) }
			}
			if _, _, err := certifyDiscoveredService(manifest, service, ""); err == nil || !strings.Contains(err.Error(),"exactly one") { t.Fatalf("ambiguous policies must not be guessed: %v",err) }
			delete(manifest.Policies,"local")
			if got, _, err := certifyDiscoveredService(manifest, service, ""); err != nil || got != "remote" { t.Fatalf("unique Apollo policy rejected: %q %v",got,err) }
		})
	}
}
