package runtime

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestStartStartsOnlySelectedAndRoutesDependencies(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("HOME", stateHome)
	workspaceRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspaceRoot, ".conven"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"user-svc", "order-svc", "payment-svc"} {
		if err := os.Mkdir(filepath.Join(workspaceRoot, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	manifest := &model.Manifest{
		Version:   1,
		Workspace: model.Workspace{Name: "integration"},
		Environments: map[string]model.Environment{
			"dev": {Registry: "nacos"},
		},
		Services: map[string]model.Service{
			"user-svc": {
				Path:  "user-svc",
				Ports: map[string]int{"http": 18080},
				Runner: model.Runner{Run: []string{
					"sh", "-c", "printf '%s|%s\\n' \"$ORDER_ROUTE\" \"$PAYMENT_ROUTE\"; while :; do sleep 1; done",
				}},
				Dependencies: map[string]model.Dependency{
					"order-svc": {
						LocalEnv:  map[string]string{"ORDER_ROUTE": "local-order"},
						RemoteEnv: map[string]string{"ORDER_ROUTE": "remote-order"},
					},
					"payment-svc": {
						LocalEnv:  map[string]string{"PAYMENT_ROUTE": "local-payment"},
						RemoteEnv: map[string]string{"PAYMENT_ROUTE": "remote-payment"},
					},
				},
			},
			"order-svc": {
				Path:   "order-svc",
				Ports:  map[string]int{"http": 18081},
				Runner: model.Runner{Run: []string{"sh", "-c", "while :; do sleep 1; done"}},
			},
			"payment-svc": {
				Path:   "payment-svc",
				Ports:  map[string]int{"http": 18082},
				Runner: model.Runner{Run: []string{"sh", "-c", "while :; do sleep 1; done"}},
			},
		},
	}
	store, err := NewStore(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	workspace := &WorkspaceData{
		Root:       workspaceRoot,
		ConfigPath: filepath.Join(workspaceRoot, ".conven", "conven.yaml"),
		Manifest:   manifest,
		Store:      store,
	}
	var output strings.Builder
	session, err := Start(context.Background(), workspace, StartOptions{
		Common:   CommonOptions{Environment: "dev"},
		Services: []string{"user-svc", "order-svc"},
		Output:   &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer Stop(context.Background(), workspace, nil, true, false, &output)
	if len(session.Services) != 2 {
		t.Fatalf("started %d services, want 2", len(session.Services))
	}
	for _, process := range session.Services {
		if process.Name == "payment-svc" {
			t.Fatal("unselected payment-svc was started")
		}
		if process.Ports["http"] == 0 {
			t.Fatalf("%s did not retain its started port snapshot: %#v", process.Name, process.Ports)
		}
	}
	var logs strings.Builder
	if err := ShowLogs(context.Background(), session, []string{"user-svc"}, false, &logs); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "local-order|remote-payment") {
		t.Fatalf("user log does not contain resolved local/remote routes: %q", logs.String())
	}
	if err := Stop(context.Background(), workspace, nil, true, false, &output); err != nil {
		t.Fatalf("stop failed: %v\n%s", err, output.String())
	}
	for _, process := range session.Services {
		if ProcessAlive(process.PID) {
			t.Fatalf("%s process %d is still alive", process.Name, process.PID)
		}
	}
}

func TestStartRunsFromPrepareCreatedRunWorkdir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspaceRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspaceRoot, "api"), 0700); err != nil {
		t.Fatal(err)
	}
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{
		Version:   1,
		Workspace: model.Workspace{Name: "run-workdir"},
		Services: map[string]model.Service{
			"api": {
				Path: "api",
				Runner: model.Runner{
					Prepare:    []string{"sh", "-c", `mkdir -p "$CONVEN_CONFIG_DIR/go" && printf 'prepared\n' > "$CONVEN_CONFIG_DIR/go/ready"`},
					RunWorkdir: "${runDir}/configs/${service}/go",
					Run:        []string{"sh", "-c", "pwd; while :; do sleep 1; done"},
				},
				Health: model.Health{Type: "command", Command: []string{"test", "-f", "ready"}, Timeout: "2s"},
			},
		},
	})
	var output strings.Builder
	session, err := Start(context.Background(), workspace, StartOptions{
		Common:   CommonOptions{Environment: "dev"},
		Services: []string{"api"},
		Output:   &output,
	})
	if err != nil {
		t.Fatalf("start failed: %v\n%s", err, output.String())
	}
	defer Stop(context.Background(), workspace, nil, true, false, &output)
	want := filepath.Join(workspace.Store.CurrentDir, "configs", "api", "go")
	var logs strings.Builder
	if err := ShowLogs(context.Background(), session, []string{"api"}, false, &logs); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), want) {
		t.Fatalf("service did not run from %q: %q", want, logs.String())
	}
}

func TestStartMaterializesConfigBeforePrepareWithoutWritingSource(t *testing.T) {
	workspaceRoot := t.TempDir()
	resources := filepath.Join(workspaceRoot, "api", "resources")
	if err := os.MkdirAll(resources, 0700); err != nil {
		t.Fatal(err)
	}
	sourceApplication := filepath.Join(resources, "application.yaml")
	if err := os.WriteFile(sourceApplication, []byte("host: 0.0.0.0\nport: 8080\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resources, "config-dev.yaml"), []byte("appId: test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(workspaceRoot, "api", "start-test-service")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nwhile :; do sleep 1; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{
		Version:   1,
		Workspace: model.Workspace{Name: "materialized-start", Policy: "retail"},
		Policies: map[string]model.Policy{
			"retail": {
				Drivers: model.PolicyDrivers{Framework: "go-zero", ConfigSource: "repository", Discovery: "consul", Materializer: "yaml-overlay"},
				Config:  model.PolicyConfig{SourceDir: "resources", Application: "application.yaml", Bootstrap: "config-dev.yaml", RuntimeBootstrap: "config-local.yaml"},
				Process: model.PolicyProcess{Env: map[string]string{"PROFILE_ACTIVE": "local"}, Args: []string{"-f", "${configDir}"}},
				Routing: model.PolicyRouting{Servers: map[string]model.ServerRoute{
					"http": {
						Port:    "http",
						Patches: []model.ConfigPatch{{Path: "port", Value: "${port.http}"}},
						Isolation: model.ServerIsolation{
							Registration: model.RegistrationGuard{Mode: "not-applicable"},
							Listener: model.ListenerGuard{Path: "host", Value: "127.0.0.1"},
						},
					},
				}},
			},
		},
		Services: map[string]model.Service{
			"api": {
				Path:  "api",
				Kind:  "http",
				Ports: map[string]int{"http": 18080},
				Runner: model.Runner{
					Prepare: []string{"sh", "-c", `grep -q 'port: 18080' "$CONVEN_CONFIG_DIR/application.yaml"`},
					Run:     []string{launcher},
				},
			},
		},
	})
	var output strings.Builder
	session, err := Start(context.Background(), workspace, StartOptions{
		Common:   CommonOptions{Environment: "dev"},
		Services: []string{"api"},
		Output:   &output,
	})
	if err != nil {
		t.Fatalf("start failed: %v\n%s", err, output.String())
	}
	defer Stop(context.Background(), workspace, nil, true, false, &output)
	if session == nil || len(session.Services) != 1 {
		t.Fatalf("session = %#v", session)
	}
	materializing := strings.Index(output.String(), "Materializing api config")
	preparing := strings.Index(output.String(), "Preparing api")
	if materializing < 0 || preparing < 0 || materializing > preparing {
		t.Fatalf("config was not materialized before prepare:\n%s", output.String())
	}
	if source, err := os.ReadFile(sourceApplication); err != nil || string(source) != "host: 0.0.0.0\nport: 8080\n" {
		t.Fatalf("source application changed: data=%q err=%v", source, err)
	}
	runtimeApplication := filepath.Join(workspace.Store.CurrentDir, "configs", "api", "application.yaml")
	if runtime, err := os.ReadFile(runtimeApplication); err != nil || !strings.Contains(string(runtime), "port: 18080") {
		t.Fatalf("runtime application was not patched: data=%q err=%v", runtime, err)
	}
}

func TestStartDryRunDoesNotFetchOrMaterializeApolloConfig(t *testing.T) {
	workspaceRoot := t.TempDir()
	resources := filepath.Join(workspaceRoot, "api", "resources")
	if err := os.MkdirAll(resources, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resources, "config-dev.yaml"), []byte("appId: demo\ncluster: dev\nip: 127.0.0.1:1\nnamespaceName: application.yml\n"), 0600); err != nil {
		t.Fatal(err)
	}
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{
		Version:   1,
		Workspace: model.Workspace{Name: "materialized-dry-run", Policy: "retail"},
		Policies: map[string]model.Policy{
			"retail": {
				Drivers: model.PolicyDrivers{ConfigSource: "apollo", Materializer: "yaml-overlay"},
				Config: model.PolicyConfig{
					SourceDir:        "resources",
					Application:      "application.yaml",
					Bootstrap:        "config-${env}.yaml",
					RuntimeBootstrap: "config-local.yaml",
				},
			},
		},
		Services: map[string]model.Service{
			"api": {Path: "api", Runner: model.Runner{Run: []string{"sleep", "600"}}},
		},
	})
	var output strings.Builder
	if _, err := Start(context.Background(), workspace, StartOptions{
		Common:   CommonOptions{Environment: "dev"},
		Services: []string{"api"},
		DryRun:   true,
		Output:   &output,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(workspace.Store.Root); !os.IsNotExist(err) {
		t.Fatalf("dry-run created materialization state: %v", err)
	}
	if !strings.Contains(output.String(), "no connection, config fetch/materialization") {
		t.Fatalf("dry-run output does not describe config behavior: %s", output.String())
	}
}

func TestStartDryRunDoesNotCreateRunWorkdir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspaceRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspaceRoot, "api"), 0700); err != nil {
		t.Fatal(err)
	}
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{
		Version:   1,
		Workspace: model.Workspace{Name: "dry-run-workdir"},
		Services: map[string]model.Service{
			"api": {
				Path: "api",
				Runner: model.Runner{
					Prepare:    []string{"sh", "-c", `mkdir -p "$CONVEN_CONFIG_DIR/go"`},
					RunWorkdir: "${runDir}/configs/${service}/go",
					Run:        []string{"sleep", "600"},
				},
			},
		},
	})
	var output strings.Builder
	_, err := Start(context.Background(), workspace, StartOptions{
		Common:   CommonOptions{Environment: "dev"},
		Services: []string{"api"},
		DryRun:   true,
		Output:   &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(workspace.Store.Root); !os.IsNotExist(err) {
		t.Fatalf("dry-run created runtime state: %v", err)
	}
}

func TestStartDryRunPreservesExistingCurrent(t *testing.T) {
	workspaceRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspaceRoot, "api"), 0700); err != nil {
		t.Fatal(err)
	}
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{
		Version:   1,
		Workspace: model.Workspace{Name: "dry-run-preserves-current"},
		Services: map[string]model.Service{
			"api": {Path: "api", Runner: model.Runner{Run: []string{"sleep", "600"}}},
		},
	})
	if err := workspace.Store.ResetCurrent(); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(workspace.Store.CurrentDir, "logs", "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(context.Background(), workspace, StartOptions{
		Common:   CommonOptions{Environment: "dev"},
		Services: []string{"api"},
		DryRun:   true,
		Output:   &strings.Builder{},
	}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "keep\n" {
		t.Fatalf("dry-run changed current sentinel: data=%q err=%v", data, err)
	}
}

func TestStartStaticValidationFailurePreservesCurrent(t *testing.T) {
	workspaceRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspaceRoot, "api"), 0700); err != nil {
		t.Fatal(err)
	}
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{
		Version:   1,
		Workspace: model.Workspace{Name: "invalid-command-preserves-current"},
		Services: map[string]model.Service{
			"api": {Path: "api", Runner: model.Runner{Run: []string{"conven-command-that-does-not-exist"}}},
		},
	})
	if err := workspace.Store.ResetCurrent(); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(workspace.Store.CurrentDir, "artifacts", "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Start(context.Background(), workspace, StartOptions{
		Common:   CommonOptions{Environment: "dev"},
		Services: []string{"api"},
		Output:   &strings.Builder{},
	})
	if err == nil || !strings.Contains(err.Error(), "run command") {
		t.Fatalf("error = %v, want static run command failure", err)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "keep\n" {
		t.Fatalf("static validation changed current sentinel: data=%q err=%v", data, err)
	}
}

func TestStartUnverifiedSessionPreservesCurrent(t *testing.T) {
	workspaceRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspaceRoot, "api"), 0700); err != nil {
		t.Fatal(err)
	}
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{
		Version:   1,
		Workspace: model.Workspace{Name: "unverified-session"},
		Services: map[string]model.Service{
			"api": {Path: "api", Runner: model.Runner{Run: []string{"sleep", "600"}}},
		},
	})
	if err := workspace.Store.ResetCurrent(); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(workspace.Store.CurrentDir, "configs", "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Store.Save(&Session{Services: []ServiceProcess{{Name: "api", PGID: syscall.Getpgrp()}}}); err != nil {
		t.Fatal(err)
	}
	_, err := Start(context.Background(), workspace, StartOptions{
		Common:   CommonOptions{Environment: "dev"},
		Services: []string{"api"},
		Output:   &strings.Builder{},
	})
	if err == nil || !strings.Contains(err.Error(), "unverified process group") {
		t.Fatalf("error = %v, want unverified process rejection", err)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "keep\n" {
		t.Fatalf("unverified session changed current sentinel: data=%q err=%v", data, err)
	}
}

func TestStartFailureRetainsPartialCurrentAndClearsSession(t *testing.T) {
	workspaceRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspaceRoot, "api"), 0700); err != nil {
		t.Fatal(err)
	}
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{
		Version:   1,
		Workspace: model.Workspace{Name: "failed-start-partial-current"},
		Services: map[string]model.Service{
			"api": {
				Path: "api",
				Runner: model.Runner{
					Prepare: []string{"sh", "-c", `printf 'partial\n' > "$CONVEN_CONFIG_DIR/partial"; exit 7`},
					Run:     []string{"sleep", "600"},
				},
			},
		},
	})
	if err := workspace.Store.ResetCurrent(); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(workspace.Store.CurrentDir, "logs", "old-sentinel")
	if err := os.WriteFile(sentinel, []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Start(context.Background(), workspace, StartOptions{
		Common:   CommonOptions{Environment: "dev"},
		Services: []string{"api"},
		Output:   &strings.Builder{},
	})
	if err == nil {
		t.Fatal("failing prepare unexpectedly started")
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("fresh start retained old sentinel: %v", err)
	}
	partial := filepath.Join(workspace.Store.CurrentDir, "configs", "api", "partial")
	if data, err := os.ReadFile(partial); err != nil || string(data) != "partial\n" {
		t.Fatalf("failed start did not retain partial config: data=%q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(workspace.Store.CurrentDir, "logs", "api-prepare.log")); err != nil {
		t.Fatalf("failed start did not retain prepare log: %v", err)
	}
	session, err := workspace.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if session != nil {
		t.Fatalf("complete rollback retained session: %#v", session)
	}
}

func TestStopPreservesCurrent(t *testing.T) {
	workspaceRoot := t.TempDir()
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{})
	if err := workspace.Store.ResetCurrent(); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(workspace.Store.CurrentDir, "logs", "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Store.Save(&Session{Workspace: workspaceRoot}); err != nil {
		t.Fatal(err)
	}
	if err := Stop(context.Background(), workspace, nil, true, false, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "keep\n" {
		t.Fatalf("stop changed current sentinel: data=%q err=%v", data, err)
	}
	if session, err := workspace.Store.Load(); err != nil || session != nil {
		t.Fatalf("stop did not clear session: session=%#v err=%v", session, err)
	}
}

func TestStartRejectsMissingRunWorkdirAfterPrepare(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspaceRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspaceRoot, "api"), 0700); err != nil {
		t.Fatal(err)
	}
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{
		Version:   1,
		Workspace: model.Workspace{Name: "missing-run-workdir"},
		Services: map[string]model.Service{
			"api": {
				Path: "api",
				Runner: model.Runner{
					Prepare:    []string{"sh", "-c", "true"},
					RunWorkdir: "${runDir}/configs/${service}/missing",
					Run:        []string{"sleep", "600"},
				},
			},
		},
	})
	var output strings.Builder
	_, err := Start(context.Background(), workspace, StartOptions{
		Common:   CommonOptions{Environment: "dev"},
		Services: []string{"api"},
		Output:   &output,
	})
	if err == nil || !strings.Contains(err.Error(), "run workdir") || !strings.Contains(err.Error(), "before run") {
		t.Fatalf("error = %v\n%s", err, output.String())
	}
	session, loadErr := workspace.Store.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if session != nil {
		t.Fatalf("failed start left a session: %#v", session)
	}
}

func TestDoctorAllowsPrepareCreatedRunWorkdirAndChecksHealthCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspaceRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspaceRoot, "api"), 0700); err != nil {
		t.Fatal(err)
	}
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{
		Version:   1,
		Workspace: model.Workspace{Name: "doctor-run-workdir"},
		Services: map[string]model.Service{
			"api": {
				Path: "api",
				Runner: model.Runner{
					Prepare:    []string{"sh", "-c", "true"},
					RunWorkdir: "${runDir}/configs/${service}/go",
					Run:        []string{"./server"},
				},
				Health: model.Health{Type: "command", Command: []string{"${runDir}/configs/${service}/go/health"}},
			},
		},
	})
	var output strings.Builder
	if err := Doctor(workspace, CommonOptions{Environment: "dev"}, &output); err != nil {
		t.Fatalf("doctor rejected prepare-created run workdir: %v", err)
	}
	if !strings.Contains(output.String(), "Doctor checks passed.") {
		t.Fatalf("doctor output = %q", output.String())
	}

	manifestService := workspace.Manifest.Services["api"]
	manifestService.Runner.Run = []string{"sh", "-c", "exit 0"}
	manifestService.Health.Command = []string{"conven-health-command-that-does-not-exist"}
	workspace.Manifest.Services["api"] = manifestService
	err := Doctor(workspace, CommonOptions{Environment: "dev"}, &output)
	if err == nil || !strings.Contains(err.Error(), "health command") {
		t.Fatalf("doctor health error = %v", err)
	}
}

func TestStartStartsCycleBeforeCheckingGroupHealth(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspaceRoot := t.TempDir()
	for _, name := range []string{"api", "worker"} {
		if err := os.Mkdir(filepath.Join(workspaceRoot, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	apiReady := filepath.Join(workspaceRoot, "api.ready")
	workerReady := filepath.Join(workspaceRoot, "worker.ready")
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{
		Version:   1,
		Workspace: model.Workspace{Name: "cycle"},
		Services: map[string]model.Service{
			"api": {
				Path:   "api",
				Runner: model.Runner{Run: []string{"sh", "-c", "touch \"$1\"; while :; do sleep 1; done", "sh", apiReady}},
				Health: model.Health{Type: "command", Command: []string{"sh", "-c", "test -f \"$1\"", "sh", workerReady}, Timeout: "2s"},
				Dependencies: map[string]model.Dependency{
					"worker": {LocalEnv: map[string]string{"WORKER_ROUTE": "local"}},
				},
			},
			"worker": {
				Path:   "worker",
				Runner: model.Runner{Run: []string{"sh", "-c", "touch \"$1\"; while :; do sleep 1; done", "sh", workerReady}},
				Health: model.Health{Type: "command", Command: []string{"sh", "-c", "test -f \"$1\"", "sh", apiReady}, Timeout: "2s"},
				Dependencies: map[string]model.Dependency{
					"api": {LocalEnv: map[string]string{"API_ROUTE": "local"}},
				},
			},
		},
	})
	var output strings.Builder
	session, err := Start(context.Background(), workspace, StartOptions{
		Common:   CommonOptions{Environment: "dev"},
		Services: []string{"worker", "api"},
		Output:   &output,
	})
	if err != nil {
		t.Fatalf("cycle startup failed: %v\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "Starting dependency cycle together: api, worker") {
		t.Fatalf("cycle startup was not reported: %s", output.String())
	}
	if err := Stop(context.Background(), workspace, nil, true, false, &output); err != nil {
		t.Fatalf("stop cycle: %v", err)
	}
	for _, process := range session.Services {
		if ProcessGroupAlive(process.PGID) {
			t.Fatalf("%s process group %d is still active", process.Name, process.PGID)
		}
	}
}

func TestValidateSkipBuildRejectsDefaultPerRunArtifact(t *testing.T) {
	current := filepath.Join(t.TempDir(), "current")
	artifact := filepath.Join(current, "artifacts", "api")
	plan := &Plan{
		RunDir: current,
		Order:  []string{"api"},
		Services: map[string]PlannedService{
			"api": {Artifact: artifact, Build: []string{"go", "build", "-o", artifact}, Run: []string{artifact}},
		},
	}
	err := validateSkipBuild(plan)
	if err == nil || !strings.Contains(err.Error(), "fresh start") {
		t.Fatalf("error = %v, want current runtime artifact error", err)
	}
}

func TestValidateSkipBuildAllowsPersistentArtifact(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, ".conven", "runtime", "current")
	artifact := filepath.Join(root, "api", "bin", "api")
	plan := &Plan{
		RunDir: current,
		Order:  []string{"api"},
		Services: map[string]PlannedService{
			"api": {Artifact: artifact, Build: []string{"go", "build", "-o", artifact}, Run: []string{artifact}},
		},
	}
	if err := validateSkipBuild(plan); err != nil {
		t.Fatalf("persistent artifact was rejected: %v", err)
	}
}

func TestValidateSkipBuildRejectsRelativeCustomCurrentArtifact(t *testing.T) {
	current := filepath.Join(t.TempDir(), "current")
	artifact := filepath.Join(current, "artifacts", "custom", "server")
	plan := &Plan{
		RunDir: current,
		Order:  []string{"api"},
		Services: map[string]PlannedService{
			"api": {
				Artifact: artifact,
				Build:    []string{"sh", "-c", "build"},
				Run:      []string{"sh", "-c", `exec "$CONVEN_ARTIFACT"`},
			},
		},
	}
	if err := validateSkipBuild(plan); err == nil || !strings.Contains(err.Error(), artifact) {
		t.Fatalf("error = %v, want relative custom current artifact rejection", err)
	}
}

func TestStartCancellationDuringPrepareDoesNotStartService(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspaceRoot := t.TempDir()
	serviceDir := filepath.Join(workspaceRoot, "api")
	if err := os.Mkdir(serviceDir, 0700); err != nil {
		t.Fatal(err)
	}
	startedPath := filepath.Join(workspaceRoot, "started")
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{
		Version:   1,
		Workspace: model.Workspace{Name: "cancel-prepare"},
		Services: map[string]model.Service{
			"api": {
				Path: "api",
				Runner: model.Runner{
					Prepare: []string{"sleep", "600"},
					Run:     []string{"sh", "-c", "touch \"$1\"; while :; do sleep 1; done", "sh", startedPath},
				},
			},
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := Start(ctx, workspace, StartOptions{
			Common:   CommonOptions{Environment: "dev"},
			Services: []string{"api"},
			Output:   &strings.Builder{},
		})
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context cancellation", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("start did not return after cancellation")
	}
	if _, err := os.Stat(startedPath); !os.IsNotExist(err) {
		t.Fatalf("service run command was started: %v", err)
	}
	session, err := workspace.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if session != nil {
		t.Fatalf("cancelled startup retained state without live processes: %#v", session)
	}
}

func TestStartReleasesPreviousConnectionBeforeReplacingStaleSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	t.Setenv("CONVEN_CONNECTION_HELPER", "1")
	t.Setenv("CONVEN_CONNECTION_HELPER_ADDRESS", address)
	workspaceRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspaceRoot, "api"), 0700); err != nil {
		t.Fatal(err)
	}
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{
		Version:   1,
		Workspace: model.Workspace{Name: "replace-stale-session"},
		Services: map[string]model.Service{
			"api": {Path: "api", Runner: model.Runner{Run: []string{"sleep", "600"}}},
		},
	})
	connection, err := EnsureConnection(context.Background(), ConnectionConfig{
		Driver:     "command",
		Command:    os.Args[0],
		Args:       []string{"-test.run=^TestConnectionHelperProcess$"},
		Timeout:    2 * time.Second,
		Readiness:  []ConnectionEndpoint{{Name: "helper", Address: address}},
	}, filepath.Join(workspaceRoot, "connection.log"), workspace.Store.Root, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stopConnection(connection, true)
		_ = removeConnectionRecord(connection.Fingerprint)
	}()
	if err := workspace.Store.Save(&Session{
		Workspace:   workspaceRoot,
		Environment: "old",
		CreatedAt:   time.Now(),
		Connection:  connection,
	}); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	newSession, err := Start(context.Background(), workspace, StartOptions{
		Common:   CommonOptions{Environment: "dev"},
		Services: []string{"api"},
		Output:   &output,
	})
	if err != nil {
		t.Fatalf("replace stale session: %v\n%s", err, output.String())
	}
	if ProcessGroupAlive(connection.PGID) {
		t.Fatalf("previous connection process group %d is still active", connection.PGID)
	}
	if err := Stop(context.Background(), workspace, nil, true, false, &output); err != nil {
		t.Fatal(err)
	}
	for _, process := range newSession.Services {
		if ProcessGroupAlive(process.PGID) {
			t.Fatalf("new service process group %d is still active", process.PGID)
		}
	}
}

func TestStartRollbackStopsExecServiceAndClearsState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspaceRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspaceRoot, "api"), 0700); err != nil {
		t.Fatal(err)
	}
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{
		Version:   1,
		Workspace: model.Workspace{Name: "exec-rollback"},
		Services: map[string]model.Service{
			"api": {
				Path:   "api",
				Runner: model.Runner{Run: []string{"sh", "-c", "exec sleep 600"}},
				Health: model.Health{Type: "command", Command: []string{"sh", "-c", "exit 1"}, Timeout: "100ms"},
			},
		},
	})
	_, err := Start(context.Background(), workspace, StartOptions{
		Common:   CommonOptions{Environment: "dev"},
		Services: []string{"api"},
		Output:   &strings.Builder{},
	})
	if err == nil {
		t.Fatal("unhealthy service unexpectedly started")
	}
	session, loadErr := workspace.Store.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if session != nil {
		t.Fatalf("successful rollback retained session: %#v", session)
	}
}

func TestStartPreservesStateWhenRollbackCannotVerifyOrphanGroup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspaceRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspaceRoot, "api"), 0700); err != nil {
		t.Fatal(err)
	}
	leaderPIDPath := filepath.Join(workspaceRoot, "api", "leader.pid")
	releasePath := filepath.Join(workspaceRoot, "api", "release")
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{
		Version:   1,
		Workspace: model.Workspace{Name: "orphan-rollback"},
		Services: map[string]model.Service{
			"api": {
				Path:   "api",
				Runner: model.Runner{Run: []string{"sh", "-c", `trap '' HUP
sleep 600 &
printf '%s\n' "$$" > "$1"
while [ ! -f "$2" ]; do sleep 0.01; done`, "sh", leaderPIDPath, releasePath}},
				Health: model.Health{Type: "command", Command: []string{"sh", "-c", `while [ ! -s "$1" ]; do sleep 0.01; done
: > "$2"
while kill -0 "$(cat "$1")" 2>/dev/null; do sleep 0.01; done
exit 1`, "sh", leaderPIDPath, releasePath}, Timeout: "2s"},
			},
		},
	})
	var returnedSession *Session
	t.Cleanup(func() {
		sessions := []*Session{returnedSession}
		if savedSession, err := workspace.Store.Load(); err == nil {
			sessions = append(sessions, savedSession)
		}
		stoppedGroups := make(map[int]bool)
		for _, session := range sessions {
			if session == nil {
				continue
			}
			for _, process := range session.Services {
				if process.PGID <= 0 || stoppedGroups[process.PGID] {
					continue
				}
				_ = ForceStopProcessGroup(process, time.Second)
				stoppedGroups[process.PGID] = true
			}
		}
	})
	returnedSession, err := Start(context.Background(), workspace, StartOptions{
		Common:   CommonOptions{Environment: "dev"},
		Services: []string{"api"},
		Output:   &strings.Builder{},
	})
	if err == nil || !strings.Contains(err.Error(), "rollback incomplete") {
		t.Fatalf("error = %v, want incomplete rollback", err)
	}
	session, loadErr := workspace.Store.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if session == nil || len(session.Services) != 1 {
		t.Fatalf("rollback did not preserve orphan state: %#v", session)
	}
	process := session.Services[0]
	if !ProcessGroupAlive(process.PGID) {
		t.Fatalf("expected orphan process group %d to remain for recovery test", process.PGID)
	}
	var stopOutput strings.Builder
	if stopErr := Stop(context.Background(), workspace, nil, true, true, &stopOutput); stopErr != nil {
		t.Fatalf("force recovery failed: %v\n%s", stopErr, stopOutput.String())
	}
	if ProcessGroupAlive(process.PGID) {
		t.Fatalf("force recovery left process group %d active", process.PGID)
	}
	cleared, clearErr := workspace.Store.Load()
	if clearErr != nil {
		t.Fatal(clearErr)
	}
	if cleared != nil {
		t.Fatalf("force recovery did not clear session: %#v", cleared)
	}
}

func TestForceStopAllRecoversUnleasedSharedConnectionWithoutSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspaceRoot := t.TempDir()
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{})
	directory := t.TempDir()
	argv := []string{"sh", "-c", "trap '' HUP; sleep 600 & sleep 0.2"}
	process, err := startConnection(context.Background(), "command", argv, argv, filepath.Join(directory, "connection.log"), "orphaned-shared", false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stopConnection(process, true)
		_ = removeConnectionRecord(process.Fingerprint)
	}()
	time.Sleep(300 * time.Millisecond)
	if ProcessAlive(process.PID) || !ProcessGroupAlive(process.PGID) {
		t.Fatalf("connection did not reach orphaned group state: %#v", process)
	}
	process.Managed = true
	if err := saveConnectionRecord(&connectionRecord{
		Version:     1,
		Fingerprint: process.Fingerprint,
		Process:     *process,
		Leases: map[string]time.Time{
			workspace.Store.Root: time.Now().Add(-connectionLeaseGrace - time.Minute),
		},
	}); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := Stop(context.Background(), workspace, nil, true, true, &output); err != nil {
		t.Fatalf("recover shared connection: %v\n%s", err, output.String())
	}
	if ProcessGroupAlive(process.PGID) {
		t.Fatalf("force recovery left process group %d active", process.PGID)
	}
	record, err := loadConnectionRecord(process.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if record != nil {
		t.Fatalf("force recovery retained record: %#v", record)
	}
	if !strings.Contains(output.String(), "Recovered 1 unleased shared connection record") {
		t.Fatalf("force recovery was not reported: %s", output.String())
	}
}

func TestStopAllStopsFinalManagedConnection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspaceRoot := t.TempDir()
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{})
	service, err := StartService("connection/ktctl", []string{"sleep", "600"}, workspaceRoot, CommandEnvironment(), filepath.Join(t.TempDir(), "connection.log"))
	if err != nil {
		t.Fatal(err)
	}
	connection := &ConnectionProcess{
		Driver:      "ktctl",
		PID:         service.PID,
		PGID:        service.PGID,
		Command:     service.Command,
		Identity:    service.Identity,
		LogPath:     service.LogPath,
		StartedAt:   service.StartedAt,
		Owned:       true,
		Managed:     true,
		Fingerprint: "stop-all-final-lease",
	}
	defer func() {
		_ = stopConnection(connection, true)
		_ = removeConnectionRecord(connection.Fingerprint)
	}()
	if err := saveConnectionRecord(&connectionRecord{
		Version:     1,
		Fingerprint: connection.Fingerprint,
		Process:     *connection,
		Leases:      map[string]time.Time{workspace.Store.Root: time.Now()},
	}); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Store.Save(&Session{Workspace: workspaceRoot, Connection: connection}); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := Stop(context.Background(), workspace, nil, true, false, &output); err != nil {
		t.Fatalf("stop all: %v\n%s", err, output.String())
	}
	if ProcessGroupAlive(connection.PGID) {
		t.Fatalf("final managed connection group %d is still active", connection.PGID)
	}
	session, err := workspace.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if session != nil {
		t.Fatalf("stop all retained session: %#v", session)
	}
	record, err := loadConnectionRecord(connection.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if record != nil {
		t.Fatalf("stop all retained connection record: %#v", record)
	}
	if !strings.Contains(output.String(), "Shared ktctl connection stopped after its final workspace lease was released.") {
		t.Fatalf("connection stop was not reported: %s", output.String())
	}
}

func TestStopAllKeepsManagedConnectionWithAnotherLease(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspaceRoot := t.TempDir()
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{})
	service, err := StartService("connection/ktctl", []string{"sleep", "600"}, workspaceRoot, CommandEnvironment(), filepath.Join(t.TempDir(), "connection.log"))
	if err != nil {
		t.Fatal(err)
	}
	connection := &ConnectionProcess{
		Driver:      "ktctl",
		PID:         service.PID,
		PGID:        service.PGID,
		Command:     service.Command,
		Identity:    service.Identity,
		LogPath:     service.LogPath,
		StartedAt:   service.StartedAt,
		Owned:       true,
		Managed:     true,
		Fingerprint: "stop-all-shared-lease",
	}
	defer func() {
		_ = stopConnection(connection, true)
		_ = removeConnectionRecord(connection.Fingerprint)
	}()
	otherLease := filepath.Join(t.TempDir(), "other-workspace")
	if err := saveConnectionRecord(&connectionRecord{
		Version:     1,
		Fingerprint: connection.Fingerprint,
		Process:     *connection,
		Leases: map[string]time.Time{
			workspace.Store.Root: time.Now(),
			otherLease:          time.Now(),
		},
	}); err != nil {
		t.Fatal(err)
	}
	reused := *connection
	reused.Owned = false
	if err := workspace.Store.Save(&Session{Workspace: workspaceRoot, Connection: &reused}); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := Stop(context.Background(), workspace, nil, true, false, &output); err != nil {
		t.Fatalf("stop all: %v\n%s", err, output.String())
	}
	if !ProcessGroupAlive(connection.PGID) {
		t.Fatal("stop all terminated a managed connection with another workspace lease")
	}
	record, err := loadConnectionRecord(connection.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if record == nil || len(record.Leases) != 1 {
		t.Fatalf("shared connection record = %#v", record)
	}
	if _, found := record.Leases[otherLease]; !found {
		t.Fatalf("other workspace lease was removed: %#v", record.Leases)
	}
	if !strings.Contains(output.String(), "Shared ktctl connection kept for 1 other workspace lease") {
		t.Fatalf("shared connection retention was not reported: %s", output.String())
	}
}

func TestStopAllLeavesExternalConnectionRunning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspaceRoot := t.TempDir()
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{})
	service, err := StartService("external/ktctl", []string{"sleep", "600"}, workspaceRoot, CommandEnvironment(), filepath.Join(t.TempDir(), "connection.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer StopProcess(service, 3*time.Second)
	connection := &ConnectionProcess{
		Driver:      "ktctl",
		PID:         service.PID,
		PGID:        service.PGID,
		Command:     service.Command,
		Identity:    service.Identity,
		LogPath:     service.LogPath,
		StartedAt:   service.StartedAt,
		Fingerprint: "external-connection",
	}
	if err := workspace.Store.Save(&Session{Workspace: workspaceRoot, Connection: connection}); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := Stop(context.Background(), workspace, nil, true, false, &output); err != nil {
		t.Fatalf("stop all: %v\n%s", err, output.String())
	}
	if !ProcessGroupAlive(connection.PGID) {
		t.Fatal("stop all terminated an external connection")
	}
	session, err := workspace.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if session != nil {
		t.Fatalf("stop all retained external connection session: %#v", session)
	}
	if !strings.Contains(output.String(), "Leaving external ktctl connection running; Conven does not own it.") {
		t.Fatalf("external connection retention was not reported: %s", output.String())
	}
}

func TestStatusShowsSavedProcessGroupIdentifiers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspaceRoot := t.TempDir()
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{})
	if err := workspace.Store.Save(&Session{
		Workspace:   workspaceRoot,
		Environment: "dev",
		Services: []ServiceProcess{{
			Name:    "api",
			PID:     99999991,
			PGID:    99999981,
			LogPath: "/tmp/api.log",
		}},
		Connection: &ConnectionProcess{
			Driver:  "command",
			PID:     99999971,
			PGID:    99999961,
			LogPath: "/tmp/connection.log",
		},
	}); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := Status(context.Background(), workspace, &output); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"api",
		"pid=99999991 pgid=99999981",
		"connection/command",
		"pid=99999971 pgid=99999961",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("status is missing %q: %s", expected, output.String())
		}
	}
}

func TestStopClearsSessionWhenDeadSharedConnectionRecordIsAlreadyAbsent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspaceRoot := t.TempDir()
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{})
	if err := workspace.Store.Save(&Session{
		Workspace: workspaceRoot,
		Connection: &ConnectionProcess{
			Driver:      "ktctl",
			PID:         99999951,
			PGID:        99999941,
			Managed:     true,
			Fingerprint: "already-removed",
		},
	}); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	if err := Stop(context.Background(), workspace, nil, true, false, &output); err != nil {
		t.Fatalf("stop stale shared connection session: %v\n%s", err, output.String())
	}
	session, err := workspace.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if session != nil {
		t.Fatalf("stale shared connection session remains: %#v", session)
	}
	if !strings.Contains(output.String(), "record is already absent") {
		t.Fatalf("idempotent release was not reported: %s", output.String())
	}
}

func TestStatusWithoutSessionShowsSharedConnectionRecoveryIdentifiers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspaceRoot := t.TempDir()
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{})
	record := &connectionRecord{
		Version:     1,
		Fingerprint: "status-preview",
		Process: ConnectionProcess{
			Driver:      "ktctl",
			PID:         99999931,
			PGID:        99999921,
			Fingerprint: "status-preview",
		},
		Leases: map[string]time.Time{},
	}
	if err := saveConnectionRecord(record); err != nil {
		t.Fatal(err)
	}
	defer removeConnectionRecord(record.Fingerprint)
	var output strings.Builder
	if err := Status(context.Background(), workspace, &output); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"No Conven session found.",
		"Shared connection records in this Conven state root:",
		"fingerprint=status-preview",
		"pid=99999931 pgid=99999921",
		"effective-leases=0",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("status is missing %q: %s", expected, output.String())
		}
	}
}

func testWorkspace(t *testing.T, root string, manifest *model.Manifest) *WorkspaceData {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".conven"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	return &WorkspaceData{
		Root:       root,
		ConfigPath: filepath.Join(root, ".conven", "conven.yaml"),
		Manifest:   manifest,
		Store:      store,
	}
}
