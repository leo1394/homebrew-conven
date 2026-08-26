package dependency

import (
	"strings"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestResolveLocalAndEndpointDependencies(t *testing.T) {
	manifest := &model.Manifest{
		Version: 2,
		Environments: map[string]model.Environment{
			"local": {
				Endpoints: map[string]model.EnvironmentEndpoint{
					"payment": {Address: "127.0.0.1:19001"},
				},
				Resolutions: map[string]map[string]model.DependencyResolution{
					"api": {
						"payment-rpc": {Mode: "endpoint", Target: "payment"},
					},
				},
			},
		},
		Services: map[string]model.Service{
			"api": {
				Dependencies: map[string]model.Dependency{
					"worker": {Env: map[string]string{"WORKER": "${dependency.address}"}},
					"payment-rpc": {Env: map[string]string{"PAYMENT": "${dependency.address}"}},
				},
			},
			"worker": {Ports: map[string]int{"rpc": 18081}},
		},
	}
	resolutions, err := Resolve(manifest, "local", []string{"api", "worker"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolutions["api"]["worker"].Mode != "local" || resolutions["api"]["worker"].Env["WORKER"] != "127.0.0.1:18081" {
		t.Fatalf("local resolution = %#v", resolutions["api"]["worker"])
	}
	if got := resolutions["api"]["payment-rpc"].Env["PAYMENT"]; got != "127.0.0.1:19001" {
		t.Fatalf("endpoint environment = %q", got)
	}
}

func TestResolveFailsClosedWithoutV2Resolution(t *testing.T) {
	manifest := &model.Manifest{
		Version: 2,
		Environments: map[string]model.Environment{"local": {}},
		Services: map[string]model.Service{
			"api": {Dependencies: map[string]model.Dependency{"missing": {}}},
		},
	}
	_, err := Resolve(manifest, "local", []string{"api"}, nil)
	if err == nil || !strings.Contains(err.Error(), "has no resolution") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveRejectsUnknownAddressForRemoteDependency(t *testing.T) {
	manifest := &model.Manifest{
		Version: 2,
		Environments: map[string]model.Environment{
			"dev": {Resolutions: map[string]map[string]model.DependencyResolution{
				"api": {"payment": {Mode: "remote"}},
			}},
		},
		Services: map[string]model.Service{
			"api": {Dependencies: map[string]model.Dependency{
				"payment": {Env: map[string]string{"PAYMENT": "${dependency.address}"}},
			}},
		},
	}
	_, err := Resolve(manifest, "dev", []string{"api"}, nil)
	if err == nil || !strings.Contains(err.Error(), "dependency.address is unavailable for remote resolution") {
		t.Fatalf("error = %v", err)
	}
}
