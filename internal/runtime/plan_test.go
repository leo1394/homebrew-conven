package runtime

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/dependency"
	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestDependencyOrderStartsDependenciesFirst(t *testing.T) {
	manifest := &model.Manifest{Version: 2, Services: map[string]model.Service{
		"api": {Dependencies: map[string]model.Dependency{"order": {}, "user": {}}},
		"order": {Dependencies: map[string]model.Dependency{"user": {}}},
		"user": {},
	}}
	actual, err := dependencyOrder(manifest, []string{"api", "order", "user"})
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"user", "order", "api"}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("order = %#v, want %#v", actual, expected)
	}
}

func TestExpandLocalServiceDependenciesIncludesTransitiveAliases(t *testing.T) {
	manifest := &model.Manifest{Version: 2, Services: map[string]model.Service{
		"api": {
			Dependencies: map[string]model.Dependency{
				"events": {LocalService: "worker"},
				"users":  {LocalService: "user"},
			},
		},
		"worker": {Dependencies: map[string]model.Dependency{"database": {LocalService: "storage"}}},
		"user":   {Dependencies: map[string]model.Dependency{"api": {}}},
		"storage": {},
	}}

	expanded, err := ExpandLocalServiceDependencies(manifest, []string{"api"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(expanded, []string{"api", "user", "worker", "storage"}) {
		t.Fatalf("expanded services = %#v", expanded)
	}
}

func TestExpandLocalServiceDependenciesKeepsDefaultSelectionUnchanged(t *testing.T) {
	manifest := &model.Manifest{Version: 1, Services: map[string]model.Service{
		"api":    {Dependencies: map[string]model.Dependency{"worker": {}}},
		"worker": {},
	}}
	selected := []string{"api"}

	expanded, err := ExpandLocalServiceDependencies(manifest, selected)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(selected, []string{"api"}) || !reflect.DeepEqual(expanded, []string{"api", "worker"}) {
		t.Fatalf("selected = %#v, expanded = %#v", selected, expanded)
	}
}

func TestExpandedVersionTwoAliasBuildsAsLocalRoute(t *testing.T) {
	workspaceRoot := t.TempDir()
	for _, name := range []string{"api", "worker"} {
		if err := os.Mkdir(filepath.Join(workspaceRoot, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	store, err := NewStore(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	manifest := &model.Manifest{
		Version:      2,
		Workspace:    model.Workspace{Name: "aliases"},
		Environments: map[string]model.Environment{"local": {Connection: model.Connection{Driver: "none"}}},
		Services: map[string]model.Service{
			"api": {
				Path:   "api",
				Runner: model.Runner{Run: []string{"api"}},
				Dependencies: map[string]model.Dependency{
					"events": {LocalService: "worker", LocalEnv: map[string]string{"WORKER_ADDRESS": "${dependency.address}"}},
				},
			},
			"worker": {Path: "worker", Runner: model.Runner{Run: []string{"worker"}}, Ports: map[string]int{"rpc": 18081}},
		},
	}
	expanded, err := ExpandLocalServiceDependencies(manifest, []string{"api"})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan(&WorkspaceData{Root: workspaceRoot, Manifest: manifest, Store: store}, CommonOptions{}, expanded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Order, []string{"worker", "api"}) || plan.Resolutions["api"]["events"].Mode != "local" {
		t.Fatalf("plan order = %#v, resolution = %#v", plan.Order, plan.Resolutions["api"]["events"])
	}
	if got := plannedEnvironment(plan.Services["api"].Environment)["WORKER_ADDRESS"]; got != "127.0.0.1:18081" {
		t.Fatalf("WORKER_ADDRESS = %q", got)
	}
}

func TestBuildPlanVersionOnePreservesLegacyDependencyRouting(t *testing.T) {
	workspaceRoot := t.TempDir()
	for _, name := range []string{"api", "db"} {
		if err := os.Mkdir(filepath.Join(workspaceRoot, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	store, err := NewStore(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	manifest := &model.Manifest{
		Version:      1,
		Workspace:    model.Workspace{Name: "legacy"},
		Environments: map[string]model.Environment{"dev": {Connection: model.Connection{Driver: "none"}}},
		Services: map[string]model.Service{
			"api": {
				Path:   "api",
				Runner: model.Runner{Run: []string{"api"}},
				Dependencies: map[string]model.Dependency{
					"db": {
						LocalEnv:  map[string]string{"DB_ADDRESS": "127.0.0.1:${services.db.ports.tcp}"},
						RemoteEnv: map[string]string{"DB_ADDRESS": "db.dev:5432"},
					},
				},
			},
			"db": {Path: "db", Runner: model.Runner{Run: []string{"db"}}, Ports: map[string]int{"tcp": 15432}},
		},
	}
	workspace := &WorkspaceData{Root: workspaceRoot, Manifest: manifest, Store: store}

	remotePlan, err := BuildPlan(workspace, CommonOptions{}, []string{"api"})
	if err != nil {
		t.Fatal(err)
	}
	if remotePlan.EnvironmentName != "dev" || !reflect.DeepEqual(remotePlan.DeclaredRemote, []string{"db"}) {
		t.Fatalf("remote plan = %#v", remotePlan)
	}
	if got := plannedEnvironment(remotePlan.Services["api"].Environment)["DB_ADDRESS"]; got != "db.dev:5432" {
		t.Fatalf("remote DB_ADDRESS = %q", got)
	}

	localPlan, err := BuildPlan(workspace, CommonOptions{}, []string{"api", "db"})
	if err != nil {
		t.Fatal(err)
	}
	if len(localPlan.DeclaredRemote) != 0 {
		t.Fatalf("legacy local plan remote dependencies = %#v", localPlan.DeclaredRemote)
	}
	if got := plannedEnvironment(localPlan.Services["api"].Environment)["DB_ADDRESS"]; got != "127.0.0.1:15432" {
		t.Fatalf("local DB_ADDRESS = %q", got)
	}
}

func TestDependencyOrderStartsStronglyConnectedComponentsInDependencyOrder(t *testing.T) {
	manifest := &model.Manifest{Services: map[string]model.Service{
		"frontend": {Dependencies: map[string]model.Dependency{"api": {}}},
		"api": {Dependencies: map[string]model.Dependency{"worker": {}}},
		"worker": {Dependencies: map[string]model.Dependency{"api": {}, "db": {}}},
		"db": {Dependencies: map[string]model.Dependency{"cache": {}}},
		"cache": {Dependencies: map[string]model.Dependency{"db": {}}},
	}}
	actual, err := dependencyOrder(manifest, []string{"worker", "frontend", "db", "api", "cache"})
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"cache", "db", "api", "worker", "frontend"}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("order = %#v, want %#v", actual, expected)
	}
}

func TestDependencyStartGroupsKeepsCyclesTogether(t *testing.T) {
	manifest := &model.Manifest{Services: map[string]model.Service{
		"frontend": {Dependencies: map[string]model.Dependency{"api": {}}},
		"api": {Dependencies: map[string]model.Dependency{"worker": {}}},
		"worker": {Dependencies: map[string]model.Dependency{"api": {}, "db": {}}},
		"db": {Dependencies: map[string]model.Dependency{"cache": {}}},
		"cache": {Dependencies: map[string]model.Dependency{"db": {}}},
	}}
	actual, err := dependencyStartGroups(manifest, []string{"worker", "frontend", "db", "api", "cache"})
	if err != nil {
		t.Fatal(err)
	}
	expected := [][]string{{"cache", "db"}, {"api", "worker"}, {"frontend"}}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("groups = %#v, want %#v", actual, expected)
	}
}

func TestRemoteResolutionNamesOnlyIncludesRemoteMode(t *testing.T) {
	resolutions := map[string]map[string]dependency.Resolution{
		"api": {
			"payment": {Mode: "remote"},
			"postgres": {Mode: "endpoint"},
			"worker": {Mode: "local"},
		},
	}
	actual := remoteResolutionNames(resolutions)
	expected := []string{"payment"}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("remote = %#v, want %#v", actual, expected)
	}
}

func TestBuildRestartPlanRejectsManifestCommandConnection(t *testing.T) {
	workspaceRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspaceRoot, "api"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	workspace := &WorkspaceData{
		Root: workspaceRoot,
		Manifest: &model.Manifest{
			Environments: map[string]model.Environment{
				"dev": {Connection: model.Connection{Driver: "command"}},
			},
			Services: map[string]model.Service{
				"api": {Path: "api", Runner: model.Runner{Run: []string{"api"}}},
			},
		},
		Store: store,
	}
	_, err = BuildRestartPlan(workspace, CommonOptions{Environment: "dev"}, []string{"api"})
	if err == nil || !strings.Contains(err.Error(), "cannot prove") {
		t.Fatalf("restart plan command connection error = %v", err)
	}
}

func TestPlanServiceRejectsConflictingDependencyEnvironment(t *testing.T) {
	plan := dependencyEnvironmentPlan(t, "false", "true")
	_, err := planService(plan, "api", map[string]bool{"api": true, "a-svc": true})
	if err == nil || !strings.Contains(err.Error(), "dependency environment key") {
		t.Fatalf("conflicting dependency environment error = %v", err)
	}
}

func TestPlanServiceAllowsMatchingDependencyEnvironment(t *testing.T) {
	plan := dependencyEnvironmentPlan(t, "false", "false")
	service, err := planService(plan, "api", map[string]bool{"api": true, "a-svc": true})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, value := range service.Environment {
		if value == "DISCOVERY_ENABLED=false" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("matching dependency environment was not applied: %#v", service.Environment)
	}
}

func TestPlanServiceMaterializesSelectedDependencyWithoutLocalEnv(t *testing.T) {
	workspaceRoot := t.TempDir()
	for _, name := range []string{"api", "db"} {
		if err := os.MkdirAll(filepath.Join(workspaceRoot, name, "resources"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workspaceRoot, name, "resources", "application.yaml"), []byte("host: 0.0.0.0\nport: 8080\ndbRpc:\n  discovType: consul\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workspaceRoot, name, "resources", "config-dev.yaml"), []byte("appId: test\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := NewStore(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	manifest := &model.Manifest{
		Version: 2,
		Environments: map[string]model.Environment{
			"dev": {Resolutions: map[string]map[string]model.DependencyResolution{
				"api": {"db": {Mode: "remote"}},
			}},
		},
		Policies: map[string]model.Policy{
			"retail": {
				Drivers: model.PolicyDrivers{
					Framework:    "go-zero",
					ConfigSource: "repository",
					Discovery:    "consul",
					Materializer: "yaml-overlay",
				},
				Config: model.PolicyConfig{SourceDir: "resources", Application: "application.yaml", Bootstrap: "config-dev.yaml", RuntimeBootstrap: "config-local.yaml"},
				Process: model.PolicyProcess{
					Env:  map[string]string{"PROFILE_ACTIVE": "local"},
					Args: []string{"-f", "${configDir}"},
				},
				Routing: model.PolicyRouting{
					Servers: map[string]model.ServerRoute{
						"http": {
							Port:    "http",
							Patches: []model.ConfigPatch{{Path: "port", Value: "${port.http}"}},
							Isolation: model.ServerIsolation{
								Registration: model.RegistrationGuard{Mode: "not-applicable"},
								Listener: model.ListenerGuard{Path: "host", Value: "127.0.0.1"},
							},
						},
					},
					LocalDependency: model.RouteRule{
						Mode: "replace",
						Value: map[string]interface{}{
							"target": "127.0.0.1:${dependency.port}",
						},
					},
					RemoteDependency: model.RouteRule{Mode: "preserve"},
				},
			},
		},
		Services: map[string]model.Service{
			"api": {
				Path:   "api",
				Policy: "retail",
				Kind:   "http",
				Runner: model.Runner{Run: []string{"api"}},
				Ports:  map[string]int{"http": 18080},
				Dependencies: map[string]model.Dependency{
					"db": {Binding: "dbRpc", Port: "rpc"},
				},
			},
			"db": {
				Path:   "db",
				Runner: model.Runner{Run: []string{"db"}},
				Ports:  map[string]int{"rpc": 18081},
			},
		},
	}
	workspace := &WorkspaceData{Root: workspaceRoot, Manifest: manifest, Store: store}
	plan, err := BuildPlan(workspace, CommonOptions{Environment: "dev"}, []string{"api", "db"})
	if err != nil {
		t.Fatal(err)
	}
	api := plan.Services["api"]
	if api.Config == nil {
		t.Fatal("api has no planned config materialization")
	}
	if api.Config.Policy != "retail" || api.Config.Plan.SourceDir != filepath.Join(workspaceRoot, "api", "resources") {
		t.Fatalf("planned config = %#v", api.Config)
	}
	if len(api.Config.Routes) != 1 || !api.Config.Routes[0].Local || api.Config.Routes[0].Dependency != "db" {
		t.Fatalf("planned routes = %#v", api.Config.Routes)
	}
	if len(api.Config.Plan.Patches) != 2 {
		t.Fatalf("planned patches = %#v", api.Config.Plan.Patches)
	}
	if len(api.Config.Plan.Guards) != 3 || api.Config.Plan.Guards[0].Path != "host" || api.Config.Plan.Guards[0].Value != "127.0.0.1" {
		t.Fatalf("planned local isolation guards = %#v", api.Config.Plan.Guards)
	}
	if api.Config.Plan.Guards[1].File != "config-local.yaml" || api.Config.Plan.Guards[1].Path != "localConfigEnable" || api.Config.Plan.Guards[1].Value != true || !api.Config.Plan.Guards[1].AllowCreate {
		t.Fatalf("planned local bootstrap enable guard = %#v", api.Config.Plan.Guards[1])
	}
	applicationPath := filepath.Join(store.CurrentDir, "configs", "api", "application.yaml")
	if api.Config.Plan.Guards[2].File != "config-local.yaml" || api.Config.Plan.Guards[2].Path != "localConfigPath" || api.Config.Plan.Guards[2].Value != applicationPath || !api.Config.Plan.Guards[2].AllowCreate {
		t.Fatalf("planned local isolation guards = %#v", api.Config.Plan.Guards)
	}
	if api.Config.Plan.Patches[0].Value != 18080 {
		t.Fatalf("server port patch = %#v", api.Config.Plan.Patches[0].Value)
	}
	routeValue, ok := api.Config.Plan.Patches[1].Value.(map[string]interface{})
	if !ok || routeValue["target"] != "127.0.0.1:18081" {
		t.Fatalf("local dependency patch = %#v", api.Config.Plan.Patches[1].Value)
	}
	if !reflect.DeepEqual(api.Run[len(api.Run)-2:], []string{"-f", filepath.Join(store.CurrentDir, "configs", "api")}) {
		t.Fatalf("policy process args = %#v", api.Run)
	}
	if plannedEnvironment(api.Environment)["PROFILE_ACTIVE"] != "local" {
		t.Fatalf("policy process env = %#v", api.Environment)
	}

	remotePlan, err := BuildPlan(workspace, CommonOptions{Environment: "dev"}, []string{"api"})
	if err != nil {
		t.Fatal(err)
	}
	remoteRoutes := remotePlan.Services["api"].Config.Routes
	if len(remoteRoutes) != 1 || remoteRoutes[0].Local || remoteRoutes[0].Mode != "preserve" {
		t.Fatalf("remote route was not preserved: %#v", remoteRoutes)
	}
	if !reflect.DeepEqual(remotePlan.DeclaredRemote, []string{"db"}) {
		t.Fatalf("declared remote dependencies = %#v", remotePlan.DeclaredRemote)
	}
}

func TestPlanServiceSnapshotsPorts(t *testing.T) {
	plan := dependencyEnvironmentPlan(t, "false", "false")
	manifestService := plan.Workspace.Manifest.Services["api"]
	manifestService.Ports = map[string]int{"http": 8080}
	plan.Workspace.Manifest.Services["api"] = manifestService

	service, err := planService(plan, "api", map[string]bool{"api": true, "a-svc": true})
	if err != nil {
		t.Fatal(err)
	}
	manifestService.Ports["http"] = 8181
	if service.Ports["http"] != 8080 {
		t.Fatalf("planned http port = %d, want snapshot 8080", service.Ports["http"])
	}
	service.Ports["metrics"] = 9090
	if _, found := manifestService.Ports["metrics"]; found {
		t.Fatal("planned service ports share the manifest port map")
	}
}

func TestPlanServiceUsesPrepareCreatedRunWorkdir(t *testing.T) {
	plan := dependencyEnvironmentPlan(t, "false", "false")
	manifestService := plan.Workspace.Manifest.Services["api"]
	manifestService.Runner.Prepare = []string{"sh", "-c", "mkdir -p \"$CONVEN_CONFIG_DIR/go\""}
	manifestService.Runner.RunWorkdir = "${runDir}/configs/${service}/go"
	manifestService.Health = model.Health{Type: "command", Command: []string{"test", "-f", "ready"}}
	plan.Workspace.Manifest.Services["api"] = manifestService

	service, err := planService(plan, "api", map[string]bool{"api": true, "a-svc": true})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(plan.RunDir, "configs", "api", "go")
	if service.RunWorkdir != want {
		t.Fatalf("run workdir = %q, want %q", service.RunWorkdir, want)
	}
	if service.Workdir == service.RunWorkdir {
		t.Fatalf("prepare/build workdir unexpectedly changed to %q", service.Workdir)
	}
	if service.Health.Directory != want {
		t.Fatalf("health workdir = %q, want %q", service.Health.Directory, want)
	}
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Fatalf("planning created run workdir: %v", err)
	}
}

func TestPlanServiceRunWorkdirDefaultsAndResolvesRelativePath(t *testing.T) {
	plan := dependencyEnvironmentPlan(t, "false", "false")
	service, err := planService(plan, "api", map[string]bool{"api": true, "a-svc": true})
	if err != nil {
		t.Fatal(err)
	}
	if service.RunWorkdir != service.Workdir {
		t.Fatalf("default run workdir = %q, want workdir %q", service.RunWorkdir, service.Workdir)
	}

	runtimeDirectory := filepath.Join(plan.Workspace.Root, "api", "runtime")
	if err := os.Mkdir(runtimeDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	manifestService := plan.Workspace.Manifest.Services["api"]
	manifestService.Runner.RunWorkdir = "runtime"
	plan.Workspace.Manifest.Services["api"] = manifestService
	service, err = planService(plan, "api", map[string]bool{"api": true, "a-svc": true})
	if err != nil {
		t.Fatal(err)
	}
	if service.RunWorkdir != runtimeDirectory {
		t.Fatalf("relative run workdir = %q, want %q", service.RunWorkdir, runtimeDirectory)
	}
}

func TestPlanServiceExpandsRunWorkdirAfterCustomArtifact(t *testing.T) {
	plan := dependencyEnvironmentPlan(t, "false", "false")
	manifestService := plan.Workspace.Manifest.Services["api"]
	manifestService.Runner.Artifact = "custom/server"
	manifestService.Runner.Prepare = []string{"true"}
	manifestService.Runner.RunWorkdir = "${artifact}.runtime"
	plan.Workspace.Manifest.Services["api"] = manifestService

	service, err := planService(plan, "api", map[string]bool{"api": true, "a-svc": true})
	if err != nil {
		t.Fatal(err)
	}
	wantArtifact := filepath.Join(plan.RunDir, "artifacts", "custom", "server")
	if service.Artifact != wantArtifact {
		t.Fatalf("artifact = %q, want %q", service.Artifact, wantArtifact)
	}
	if service.RunWorkdir != wantArtifact+".runtime" {
		t.Fatalf("run workdir = %q, want %q", service.RunWorkdir, wantArtifact+".runtime")
	}
}

func TestPlanServiceRejectsMissingRunWorkdirWithoutPrepare(t *testing.T) {
	plan := dependencyEnvironmentPlan(t, "false", "false")
	manifestService := plan.Workspace.Manifest.Services["api"]
	manifestService.Runner.RunWorkdir = "missing"
	plan.Workspace.Manifest.Services["api"] = manifestService

	_, err := planService(plan, "api", map[string]bool{"api": true, "a-svc": true})
	if err == nil || !strings.Contains(err.Error(), "does not exist and runner.prepare is empty") {
		t.Fatalf("error = %v", err)
	}
}

func TestPlanServiceInjectsResolvedWorkspaceOverUserValue(t *testing.T) {
	t.Setenv("CONVEN_WORKSPACE", "/user/workspace")
	plan := dependencyEnvironmentPlan(t, "false", "false")
	manifestService := plan.Workspace.Manifest.Services["api"]
	manifestService.Env = map[string]string{"CONVEN_WORKSPACE": "/service/workspace"}
	plan.Workspace.Manifest.Services["api"] = manifestService

	service, err := planService(plan, "api", map[string]bool{"api": true, "a-svc": true})
	if err != nil {
		t.Fatal(err)
	}
	want := "CONVEN_WORKSPACE=" + plan.Workspace.Root
	for _, value := range service.Environment {
		if strings.HasPrefix(value, "CONVEN_WORKSPACE=") {
			if value != want {
				t.Fatalf("CONVEN_WORKSPACE = %q, want %q", value, want)
			}
			return
		}
	}
	t.Fatalf("CONVEN_WORKSPACE was not injected: %#v", service.Environment)
}

func TestBuildPlanUsesFixedWorkspaceRuntimePaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspaceRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspaceRoot, "api"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	workspace := &WorkspaceData{
		Root: workspaceRoot,
		Manifest: &model.Manifest{Services: map[string]model.Service{
			"api": {
				Path: "api",
				Env: map[string]string{
					"EXPANDED_STATE":    "${stateDir}",
					"EXPANDED_RUN":      "${runDir}",
					"EXPANDED_ARTIFACT": "${artifact}",
				},
				Runner: model.Runner{Run: []string{"true"}},
			},
		}},
		Store: store,
	}
	plan, err := BuildPlan(workspace, CommonOptions{Environment: "dev"}, []string{"api"})
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	wantStateDir := filepath.Join(canonicalRoot, ".conven", "runtime")
	wantRunDir := filepath.Join(wantStateDir, "current")
	wantArtifact := filepath.Join(wantRunDir, "artifacts", "api")
	wantConfigDir := filepath.Join(wantRunDir, "configs", "api")
	if store.Root != wantStateDir {
		t.Fatalf("state directory = %q, want %q", store.Root, wantStateDir)
	}
	if plan.RunDir != wantRunDir {
		t.Fatalf("run directory = %q, want %q", plan.RunDir, wantRunDir)
	}
	service := plan.Services["api"]
	if service.Artifact != wantArtifact {
		t.Fatalf("artifact = %q, want %q", service.Artifact, wantArtifact)
	}
	environment := plannedEnvironment(service.Environment)
	wantEnvironment := map[string]string{
		"CONVEN_STATE_DIR":    wantStateDir,
		"CONVEN_RUN_DIR":      wantRunDir,
		"CONVEN_ARTIFACT":     wantArtifact,
		"CONVEN_CONFIG_DIR":   wantConfigDir,
		"EXPANDED_STATE":    wantStateDir,
		"EXPANDED_RUN":      wantRunDir,
		"EXPANDED_ARTIFACT": wantArtifact,
	}
	for key, want := range wantEnvironment {
		if environment[key] != want {
			t.Fatalf("%s = %q, want %q", key, environment[key], want)
		}
	}
}

func TestFreshPlanRejectsStaleCurrentRunWorkdirButRestartReusesIt(t *testing.T) {
	workspaceRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspaceRoot, "api"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	staleRunWorkdir := filepath.Join(store.CurrentDir, "configs", "api", "generated")
	if err := os.MkdirAll(staleRunWorkdir, 0700); err != nil {
		t.Fatal(err)
	}
	workspace := &WorkspaceData{
		Root: workspaceRoot,
		Manifest: &model.Manifest{Services: map[string]model.Service{
			"api": {
				Path: "api",
				Runner: model.Runner{
					RunWorkdir: "${runDir}/configs/${service}/generated",
					Run:        []string{"true"},
				},
			},
		}},
		Store: store,
	}
	if _, err := BuildPlan(workspace, CommonOptions{Environment: "dev"}, []string{"api"}); err == nil || !strings.Contains(err.Error(), "will be removed by the fresh runtime reset") {
		t.Fatalf("fresh plan error = %v", err)
	}
	plan, err := BuildRestartPlan(workspace, CommonOptions{Environment: "dev"}, []string{"api"})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.ReuseCurrent {
		t.Fatal("restart plan did not mark the fixed current runtime for reuse")
	}
	if plan.RunDir != store.CurrentDir {
		t.Fatalf("restart run directory = %q, want %q", plan.RunDir, store.CurrentDir)
	}
	if plan.Services["api"].RunWorkdir != staleRunWorkdir {
		t.Fatalf("restart run workdir = %q, want %q", plan.Services["api"].RunWorkdir, staleRunWorkdir)
	}
}

func TestPlanConnectionUsesConfiguredKtctlPathBeforeManifestCommand(t *testing.T) {
	workspaceRoot := t.TempDir()
	store, err := NewStore(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	plan := &Plan{
		Workspace: &WorkspaceData{
			Root:     workspaceRoot,
			Manifest: &model.Manifest{},
			Settings: map[string]string{"ktctl.path": "/custom/ktctl"},
			Store:    store,
		},
		EnvironmentName: "dev",
		Environment: model.Environment{Connection: model.Connection{
			Driver:  "ktctl",
			Command: "${unknown}",
			Readiness: []model.Endpoint{
				{Name: "api", Address: "127.0.0.1:8080"},
			},
		}},
		RunDir: t.TempDir(),
	}
	connection, err := planConnection(plan, CommonOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if connection.Command != "/custom/ktctl" {
		t.Fatalf("connection command = %q", connection.Command)
	}
}

func TestPlanConnectionUsesConfiguredKtctlKubeconfig(t *testing.T) {
	t.Setenv("CONVEN_KUBECONFIG", "")
	t.Setenv("KTCTL_KUBECONFIG", "")
	t.Setenv("PROFILE_KUBECONFIG", "")
	t.Setenv("KUBECONFIG", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspaceRoot := t.TempDir()
	store, err := NewStore(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	plan := &Plan{
		Workspace: &WorkspaceData{
			Root:     workspaceRoot,
			Manifest: &model.Manifest{},
			Settings: map[string]string{
				"ktctl.path":       "/custom/ktctl",
				"ktctl.kubeconfig": "~/.kube/custom",
			},
			Store:    store,
		},
		EnvironmentName: "dev",
		Environment: model.Environment{Connection: model.Connection{
			Driver:        "ktctl",
			KubeconfigEnv: "PROFILE_KUBECONFIG",
			Kubeconfig:    "/manifest/kubeconfig",
			Sudo:          true,
			Readiness: []model.Endpoint{
				{Name: "api", Address: "127.0.0.1:8080"},
			},
		}},
		RunDir: t.TempDir(),
	}
	connection, err := planConnection(plan, CommonOptions{})
	if err != nil {
		t.Fatal(err)
	}
	wantKubeconfig := filepath.Join(home, ".kube", "custom")
	if connection.Kubeconfig != wantKubeconfig {
		t.Fatalf("kubeconfig = %q, want %q", connection.Kubeconfig, wantKubeconfig)
	}
	if !connection.Sudo {
		t.Fatal("configured ktctl connection did not retain sudo")
	}
	argv, err := BuildConnectionCommand(connection)
	if err != nil {
		t.Fatal(err)
	}
	wantArgv := []string{"/custom/ktctl", "--kubeconfig", wantKubeconfig, "connect"}
	if !reflect.DeepEqual(argv, wantArgv) {
		t.Fatalf("ktctl argv = %#v, want %#v", argv, wantArgv)
	}
}

func TestPlanConnectionAnchorsRelativeKtctlCommandBeforeSudo(t *testing.T) {
	workspaceRoot := t.TempDir()
	toolDirectory := filepath.Join(workspaceRoot, "tools")
	if err := os.MkdirAll(toolDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	ktctl := filepath.Join(toolDirectory, "ktctl")
	if err := os.WriteFile(ktctl, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	plan := &Plan{
		Workspace: &WorkspaceData{
			Root:     workspaceRoot,
			Manifest: &model.Manifest{},
			Settings: map[string]string{},
			Store:    store,
		},
		EnvironmentName: "dev",
		Environment: model.Environment{Connection: model.Connection{
			Driver:     "ktctl",
			Command:    "tools/ktctl",
			Sudo:       true,
			Readiness: []model.Endpoint{{Name: "api", Address: "127.0.0.1:8080"}},
		}},
		RunDir: t.TempDir(),
	}
	connection, err := planConnection(plan, CommonOptions{Kubeconfig: "/secure/dev-kubeconfig"})
	if err != nil {
		t.Fatal(err)
	}
	if connection.Command != ktctl {
		t.Fatalf("connection command = %q, want %q", connection.Command, ktctl)
	}
	managed, launch, err := buildConnectionCommands(connection)
	if err != nil {
		t.Fatal(err)
	}
	wantManaged := []string{ktctl, "--kubeconfig", "/secure/dev-kubeconfig", "connect"}
	wantLaunch := []string{"sudo", "-n", ktctl, "--kubeconfig", "/secure/dev-kubeconfig", "connect"}
	if !reflect.DeepEqual(managed, wantManaged) {
		t.Fatalf("managed command = %#v, want %#v", managed, wantManaged)
	}
	if !reflect.DeepEqual(launch, wantLaunch) {
		t.Fatalf("launch command = %#v, want %#v", launch, wantLaunch)
	}
}

func TestPlanConnectionDoesNotOverrideCommandDriver(t *testing.T) {
	workspaceRoot := t.TempDir()
	store, err := NewStore(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	plan := &Plan{
		Workspace: &WorkspaceData{
			Root:     workspaceRoot,
			Manifest: &model.Manifest{},
			Settings: map[string]string{"ktctl.path": "/custom/ktctl"},
			Store:    store,
		},
		EnvironmentName: "dev",
		Environment: model.Environment{Connection: model.Connection{
			Driver:  "command",
			Command: "/manifest/network",
			Readiness: []model.Endpoint{
				{Name: "api", Address: "127.0.0.1:8080"},
			},
		}},
		RunDir: t.TempDir(),
	}
	connection, err := planConnection(plan, CommonOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if connection.Command != "/manifest/network" {
		t.Fatalf("connection command = %q", connection.Command)
	}
}

func dependencyEnvironmentPlan(t *testing.T, localValue string, remoteValue string) *Plan {
	t.Helper()
	workspaceRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspaceRoot, "api"), 0700); err != nil {
		t.Fatal(err)
	}
	manifest := &model.Manifest{Services: map[string]model.Service{
		"api": {
			Path: "api",
			Runner: model.Runner{Run: []string{"api"}},
			Dependencies: map[string]model.Dependency{
				"a-svc": {LocalEnv: map[string]string{"DISCOVERY_ENABLED": localValue}},
				"z-svc": {RemoteEnv: map[string]string{"DISCOVERY_ENABLED": remoteValue}},
			},
		},
		"a-svc": {},
		"z-svc": {},
	}}
	store, err := NewStore(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	return &Plan{
		Workspace: &WorkspaceData{
			Root:     workspaceRoot,
			Manifest: manifest,
			Store:    store,
		},
		EnvironmentName: "dev",
		Selected:        []string{"api", "a-svc"},
		RunDir:          t.TempDir(),
		Resolutions: map[string]map[string]dependency.Resolution{
			"api": {
				"a-svc": {Owner: "api", Alias: "a-svc", Mode: "local", Target: "a-svc", Env: map[string]string{"DISCOVERY_ENABLED": localValue}},
				"z-svc": {Owner: "api", Alias: "z-svc", Mode: "remote", Env: map[string]string{"DISCOVERY_ENABLED": remoteValue}},
			},
		},
	}
}

func TestDefaultEnvironmentNamePrefersDevThenLocalOrSoleProfile(t *testing.T) {
	tests := []struct {
		name         string
		environments map[string]model.Environment
		want         string
	}{
		{name: "dev", environments: map[string]model.Environment{"local": {}, "dev": {}}, want: "dev"},
		{name: "local", environments: map[string]model.Environment{"local": {}}, want: "local"},
		{name: "sole custom", environments: map[string]model.Environment{"sandbox": {}}, want: "sandbox"},
		{name: "local among custom", environments: map[string]model.Environment{"local": {}, "sandbox": {}}, want: "local"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := defaultEnvironmentName(&model.Manifest{Environments: test.environments})
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("environment = %q, want %q", got, test.want)
			}
		})
	}
}

func plannedEnvironment(values []string) map[string]string {
	environment := make(map[string]string, len(values))
	for _, value := range values {
		key, resolved, found := strings.Cut(value, "=")
		if found {
			environment[key] = resolved
		}
	}
	return environment
}
