package runtime

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/model"
	"gopkg.in/yaml.v3"
)

// Observe the registry the application would register with, not a stale manifest
// endpoint. Only the trusted contract defines this configuration shape.
func effectiveObservationRegistry(service PlannedService) (model.Registry, error) {
	registry := *service.Registry
	if registry.Driver != "consul" || service.Config == nil || !(goZeroConsulRuntimeContract{}).MatchesPlanned(service.Config) { return registry, nil }
	data, err := os.ReadFile(filepath.Join(service.Config.Plan.TargetDir, service.Config.Plan.Application))
	if err != nil { return registry, fmt.Errorf("read effective registration configuration: %w", err) }
	var document struct { Consul struct { Host string `yaml:"host"`; Port int `yaml:"port"`; Key string `yaml:"key"` } `yaml:"consul"` }
	if yaml.Unmarshal(data, &document) != nil { return registry, fmt.Errorf("invalid effective registration configuration") }
	c := document.Consul
	if c.Host == "" || strings.ContainsAny(c.Host, "/?#@ \t\r\n") || c.Port < 1 || c.Port > 65535 || c.Key != service.RegistryIdentity {
		return registry, fmt.Errorf("effective Consul registration endpoint or identity is missing or inconsistent")
	}
	address := "http://" + net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	if address != registryBaseAddress(registry.Address) && (!strings.HasPrefix(registryBaseAddress(registry.Address), "http://") || registry.TokenEnv != "" || registry.UsernameEnv != "" || registry.PasswordEnv != "" || registry.TLS != (model.RegistryTLS{})) {
		return registry, fmt.Errorf("effective Consul endpoint differs from authenticated registry declaration; update the environment registry explicitly")
	}
	registry.Address = address
	return registry, nil
}
