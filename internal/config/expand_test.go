package config

import (
	"strings"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestExpandAllSupportedTemplates(t *testing.T) {
	manifest := &model.Manifest{Services: map[string]model.Service{
		"user-svc": {
			Ports: map[string]int{"rpc": 18080},
		},
		"order.svc": {
			Ports: map[string]int{"http": 19090},
		},
	}}
	context := ExpandContext{
		Workspace:   "/work",
		Service:     "user-svc",
		ServiceDir:  "/work/services/user",
		StateDir:    "/state",
		RunDir:      "/state/run/user",
		ConfigDir:   "/state/run/user/configs/user-svc",
		Artifact:    "/state/bin/user",
		Environment: "dev",
		Manifest:    manifest,
	}
	value := "${workspace}|${service}|${serviceDir}|${stateDir}|${runDir}|${configDir}|${artifact}|${env}|${port.rpc}|${services.order.svc.ports.http}"
	want := "/work|user-svc|/work/services/user|/state|/state/run/user|/state/run/user/configs/user-svc|/state/bin/user|dev|18080|19090"

	got, err := Expand(value, context)
	if err != nil {
		t.Fatalf("Expand returned error: %v", err)
	}
	if got != want {
		t.Fatalf("Expand = %q, want %q", got, want)
	}
}

func TestExpandRejectsInvalidTemplates(t *testing.T) {
	manifest := &model.Manifest{Services: map[string]model.Service{
		"api": {Ports: map[string]int{"http": 8080}},
	}}
	context := ExpandContext{Service: "api", Manifest: manifest}
	tests := []struct {
		name      string
		value     string
		wantError string
	}{
		{name: "unknown", value: "${unknown}", wantError: "unknown template variable"},
		{name: "empty", value: "${}", wantError: "template expression is empty"},
		{name: "unterminated", value: "${workspace", wantError: "unterminated template expression"},
		{name: "missing current port", value: "${port.rpc}", wantError: "has no port"},
		{name: "missing service", value: "${services.order.ports.http}", wantError: "is not declared"},
		{name: "invalid service port syntax", value: "${services.order.http}", wantError: "services.NAME.ports.PORT"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Expand(test.value, context)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want error containing %q", err, test.wantError)
			}
		})
	}
}

func TestExpandRequiresManifestForPortTemplates(t *testing.T) {
	_, err := Expand("127.0.0.1:${port.http}", ExpandContext{Service: "api"})
	if err == nil || !strings.Contains(err.Error(), "manifest is required") {
		t.Fatalf("error = %v, want manifest requirement", err)
	}
}
