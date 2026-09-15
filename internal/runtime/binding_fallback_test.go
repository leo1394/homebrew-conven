package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/config"
	"github.com/leo1394/homebrew-conven/internal/dependency"
	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestPlanRepositoryBindingFallbackRequiresDeclaredConsumer(t *testing.T) {
	fixture := disabledBindingTestService(t, "chatclbtRpc: {target: 'localhost:9000'}\nunknownRpc: {target: 'localhost:9001'}\n", true)
	if err := os.WriteFile(filepath.Join(fixture.Directory, "application.yaml"), []byte("chatclbtRpc: {target: 'localhost:9000'}\n"), 0600); err != nil { t.Fatal(err) }
	for _, test := range []struct { name, mode string; disabled bool; resolution string; want int }{
		{"default", "", false, "", 1},
		{"explicit", "repository-if-missing", false, "", 1},
		{"opt out", "disabled", false, "", 0},
		{"disabled binding", "", true, "", 0},
		{"local route", "", false, "local", 0},
		{"disabled route", "", false, "disabled", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := model.Service{Policy: "apollo", Discovery: model.ServiceDiscovery{ConsumerBindings: []string{"chatclbtRpc", "unknownRpc"}}}
			manifest := &model.Manifest{Policies: map[string]model.Policy{"apollo": {
				Drivers: model.PolicyDrivers{Runtime: "go-zero", Framework: "go-zero", ConfigSource: "apollo", Materializer: "yaml-overlay", Discovery: "consul"},
				Config: model.PolicyConfig{SourceDir: ".", Application: "application.yaml", BindingFallback: model.BindingFallback{Mode: test.mode}},
			}}}
			if test.disabled { manifest.Workspace.DisabledBindings = []string{"chatclbtRpc"} }
			plan := &Plan{Workspace: &WorkspaceData{Manifest: manifest}, RunDir: t.TempDir()}
			if test.resolution != "" {
				service.Dependencies = map[string]model.Dependency{"chat": {Binding: "chatclbtRpc"}}
				plan.Resolutions = map[string]map[string]dependency.Resolution{"api": {"chat": {Mode: test.resolution}}}
			}
			planned, err := planServiceConfig(plan, "api", service, fixture.Directory, config.ExpandContext{ConfigDir: t.TempDir()}, nil)
			if err != nil { t.Fatal(err) }
			if len(planned.Plan.BindingFallbacks) != test.want { t.Fatalf("fallbacks = %#v", planned.Plan.BindingFallbacks) }
		})
	}
}
