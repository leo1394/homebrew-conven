package runtime

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/materialize"
	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestApolloReadinessUsesSelectedBootstrap(t *testing.T) {
	for _, tt := range []struct {
		name string
		addresses []string
		want []string
	}{
		{"test only", []string{"http://apollo-configservice.test:8080"}, []string{"apollo-test", "consul"}},
		{"default only", []string{"apollo-configservice:8080"}, []string{"apollo-default", "consul"}},
		{"mixed services", []string{"http://apollo-configservice.test:8080", "http://apollo-configservice:8080"}, []string{"apollo-default", "apollo-test", "consul"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			store, err := NewStore(root)
			if err != nil { t.Fatal(err) }
			plan := &Plan{
				Workspace: &WorkspaceData{Root: root, Store: store, Manifest: &model.Manifest{}},
				EnvironmentName: "test",
				Environment: model.Environment{Connection: model.Connection{Driver: "ktctl", Readiness: []model.Endpoint{
					{Name: "apollo-default", Address: "apollo-configservice:8080"},
					{Name: "apollo-test", Address: "apollo-configservice.test:8080"},
					{Name: "consul", Address: "consul-server.default:8500"},
				}}},
				Services: map[string]PlannedService{},
			}
			for i, address := range tt.addresses {
				name := string(rune('a'+i))
				source := filepath.Join(root, name)
				if err := os.MkdirAll(source, 0700); err != nil { t.Fatal(err) }
				if err := os.WriteFile(filepath.Join(source, "config-test.yaml"), []byte("appId: demo\ncluster: test\nip: "+address+"\nnamespaceName: application.yml\n"), 0600); err != nil { t.Fatal(err) }
				plan.Services[name] = PlannedService{RegistryRef: "consul", Config: &PlannedConfig{Plan: materialize.Plan{SourceDriver: materialize.SourceApollo, SourceDir: source, Bootstrap: "config-test.yaml"}}}
			}
			connection, err := planConnection(plan, CommonOptions{})
			if err != nil { t.Fatal(err) }
			var names []string
			for _, endpoint := range connection.Readiness { names = append(names, endpoint.Name) }
			if !reflect.DeepEqual(names, tt.want) { t.Fatalf("readiness = %v, want %v", names, tt.want) }
		})
	}
}
