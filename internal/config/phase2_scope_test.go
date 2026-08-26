package config

import (
	"strings"
	"testing"
)

func TestManifestV2RejectsManagedInfrastructureSchema(t *testing.T) {
	_, err := decodeManifest([]byte(`version: 2
workspace:
  name: demo
dependencies:
  postgres:
    preset: postgres
services: {}
`), "conven.yaml")
	if err == nil || !strings.Contains(err.Error(), "field dependencies not found") {
		t.Fatalf("error = %v", err)
	}
}

func TestManifestV2RejectsManagedResolutionMode(t *testing.T) {
	_, err := decodeManifest([]byte(`version: 2
workspace:
  name: demo
environments:
  local:
    resolutions:
      api:
        postgres:
          mode: managed
services:
  api:
    path: api
    runner:
      run: [api]
    dependencies:
      postgres: {}
`), "conven.yaml")
	if err == nil || !strings.Contains(err.Error(), "mode must be endpoint, remote, disabled, or error") {
		t.Fatalf("error = %v", err)
	}
}
