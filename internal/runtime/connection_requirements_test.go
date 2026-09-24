package runtime

import (
	"testing"

	"github.com/leo1394/homebrew-conven/internal/dependency"
	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestConnectionReadinessUsesExplicitRemoteEndpoint(t *testing.T) {
	for _, mode := range []string{"remote", "local", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			store, err := NewStore(t.TempDir()); if err != nil { t.Fatal(err) }
			plan := &Plan{
				Workspace: &WorkspaceData{Root: store.Root, Store: store, Manifest: &model.Manifest{}},
				Environment: model.Environment{Connection: model.Connection{Driver: "ktctl", Readiness: []model.Endpoint{
					{Name: "consul", Address: "consul.default:8500"},
					{Name: "consul-test", Address: "consul.test:8500"},
				}}, Resolutions: map[string]map[string]model.DependencyResolution{"api": {"directory": {Mode: "remote", Readiness: []string{"consul-test"}}}}},
				Services: map[string]PlannedService{"api": {Config: &PlannedConfig{Discovery: "consul", Routes: []PlannedRoute{{Dependency: "directory", Binding: "directoryRpc", Mode: "preserve"}}}}},
				Resolutions: map[string]map[string]dependency.Resolution{"api": {"directory": {Mode: mode}}},
			}
			connection, err := planConnection(plan, CommonOptions{}); if err != nil { t.Fatal(err) }
			if mode == "remote" {
				if len(connection.Readiness)!=1 || connection.Readiness[0].Address!="consul.test:8500" { t.Fatalf("wrong remote readiness: %+v", connection.Readiness) }
			} else {
				for _, endpoint := range connection.Readiness { if endpoint.Name=="consul-test" { t.Fatalf("inactive route added readiness: %+v", connection.Readiness) } }
			}
			plan.Services["other"] = PlannedService{RegistryRef: "consul", Registry: &model.Registry{Driver: "consul"}}
			connection, err = planConnection(plan, CommonOptions{}); if err != nil { t.Fatal(err) }
			want:=1; if mode=="remote" {want=2}
			if len(connection.Readiness)!=want || connection.Readiness[0].Address!="consul.default:8500" { t.Fatalf("shared registry changed: %+v", connection.Readiness) }
			plan.Services["other"] = PlannedService{RegistryRef: "consul-test"}
			connection, err = planConnection(plan, CommonOptions{}); if err != nil { t.Fatal(err) }
			if len(connection.Readiness)!=1 || connection.Readiness[0].Address!="consul.test:8500" { t.Fatalf("explicit registry reference lost: %+v", connection.Readiness) }
		})
	}
}

func TestRemoteReadinessDoesNotInferRegistryDriver(t *testing.T) {
	for _, driver := range []string{"consul", "nacos", "custom-discovery"} {
		plan:=&Plan{
			Environment:model.Environment{
				Registries:map[string]model.Registry{"first":{Driver:driver},"unrelated":{Driver:driver}},
				Resolutions:map[string]map[string]model.DependencyResolution{"api":{"directory":{Mode:"remote",Readiness:[]string{"directory-check"}}}},
			},
			Services:map[string]PlannedService{"api":{Config:&PlannedConfig{Discovery:driver,Routes:[]PlannedRoute{{Dependency:"directory",Binding:"directoryRpc",Mode:"preserve"}}}}},
			Resolutions:map[string]map[string]dependency.Resolution{"api":{"directory":{Mode:"remote"}}},
		}
		required:=selectedConnectionRequirements(plan)
		if len(required)!=1 || !required["endpoint:directory-check"] {t.Fatalf("%s inferred unrelated registries: %v",driver,required)}
		plan.Environment.Resolutions=nil
		if required=selectedConnectionRequirements(plan); len(required)!=0 {t.Fatalf("%s inferred registry for undeclared/direct route: %v",driver,required)}
	}
}
