package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestRestartOnlyReloadsChangedServicesAndPreservesLogs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspaceRoot := t.TempDir()
	for _, name := range []string{"api", "order"} {
		directory := filepath.Join(workspaceRoot, name)
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "source.txt"), []byte("one\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{
		Version:   1,
		Workspace: model.Workspace{Name: "restart"},
		Services: map[string]model.Service{
			"api": {
				Path:  "api",
				Ports: map[string]int{"http": 18080},
				Runner: model.Runner{Run: []string{"sleep", "600"}},
				Dependencies: map[string]model.Dependency{
					"order": {LocalEnv: map[string]string{"ORDER_ROUTE": "local"}},
				},
			},
			"order": {
				Path:   "order",
				Ports:  map[string]int{"http": 18081},
				Runner: model.Runner{Run: []string{"sleep", "600"}},
			},
		},
	})
	var output strings.Builder
	session, err := Start(context.Background(), workspace, StartOptions{
		Common:   CommonOptions{Environment: "dev"},
		Services: []string{"api", "order"},
		Output:   &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer Stop(context.Background(), workspace, nil, true, false, &output)
	apiBefore := sessionProcess(session, "api")
	orderBefore := sessionProcess(session, "order")
	if apiBefore.Ports["http"] != 18080 || orderBefore.Ports["http"] != 18081 {
		t.Fatalf("start port snapshots = api %#v, order %#v", apiBefore.Ports, orderBefore.Ports)
	}
	if apiBefore.SourceFingerprint == "" || apiBefore.PlanFingerprint == "" {
		t.Fatal("start did not record restart fingerprints")
	}
	artifactSentinel := filepath.Join(workspace.Store.CurrentDir, "artifacts", "order-sentinel")
	configSentinel := filepath.Join(workspace.Store.CurrentDir, "configs", "order", "unchanged.txt")
	if err := os.WriteFile(artifactSentinel, []byte("unchanged artifact\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configSentinel, []byte("unchanged config\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orderBefore.LogPath, []byte("unchanged log\n"), 0600); err != nil {
		t.Fatal(err)
	}

	output.Reset()
	session, err = Restart(context.Background(), workspace, RestartOptions{Output: &output})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "No changed local services") {
		t.Fatalf("no-op output = %q", output.String())
	}
	if sessionProcess(session, "api").PID != apiBefore.PID || sessionProcess(session, "order").PID != orderBefore.PID {
		t.Fatal("no-op restart changed a process")
	}

	if err := os.WriteFile(filepath.Join(workspaceRoot, "api", "source.txt"), []byte("two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	session, err = Restart(context.Background(), workspace, RestartOptions{Output: &output})
	if err != nil {
		t.Fatal(err)
	}
	apiAfter := sessionProcess(session, "api")
	orderAfter := sessionProcess(session, "order")
	if apiAfter.PID == apiBefore.PID {
		t.Fatal("changed api was not restarted")
	}
	if orderAfter.PID != orderBefore.PID {
		t.Fatal("unchanged order service was restarted")
	}
	if apiAfter.LogPath != apiBefore.LogPath {
		t.Fatalf("api log path changed from %q to %q", apiBefore.LogPath, apiAfter.LogPath)
	}
	for path, want := range map[string]string{
		artifactSentinel:   "unchanged artifact\n",
		configSentinel:     "unchanged config\n",
		orderBefore.LogPath: "unchanged log\n",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read preserved runtime file %s: %v", path, err)
		}
		if string(data) != want {
			t.Fatalf("preserved runtime file %s = %q, want %q", path, data, want)
		}
	}
	apiLog, err := os.ReadFile(apiAfter.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(apiLog), "--- conven services --restart ") {
		t.Fatalf("api log is missing restart marker: %q", apiLog)
	}

	orderService := workspace.Manifest.Services["order"]
	orderService.Env = map[string]string{"ORDER_MODE": "changed"}
	orderService.Ports = map[string]int{"http": 28081}
	workspace.Manifest.Services["order"] = orderService
	output.Reset()
	session, err = Restart(context.Background(), workspace, RestartOptions{Output: &output})
	if err != nil {
		t.Fatal(err)
	}
	orderAfterPlanChange := sessionProcess(session, "order")
	if orderAfterPlanChange.PID == orderAfter.PID {
		t.Fatal("order plan change did not restart the service")
	}
	if orderAfterPlanChange.Ports["http"] != 28081 {
		t.Fatalf("restart port snapshot = %#v", orderAfterPlanChange.Ports)
	}
	if sessionProcess(session, "api").PID != apiAfter.PID {
		t.Fatal("unchanged api was restarted for an order plan change")
	}

	output.Reset()
	session, err = Restart(context.Background(), workspace, RestartOptions{
		Services: []string{"order"},
		Output:   &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sessionProcess(session, "order").PID == orderAfterPlanChange.PID {
		t.Fatal("explicit unchanged order service was not restarted")
	}
}

func TestRestartDoesNotRematerializeUnchangedServiceConfig(t *testing.T) {
	workspaceRoot := t.TempDir()
	for _, name := range []string{"api", "order"} {
		resources := filepath.Join(workspaceRoot, name, "resources")
		if err := os.MkdirAll(resources, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(resources, "application.yaml"), []byte("name: "+name+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workspaceRoot, name, "source.txt"), []byte("one\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{
		Version:   1,
		Workspace: model.Workspace{Name: "restart-materialized", Policy: "repository"},
		Policies: map[string]model.Policy{
			"repository": {
				Drivers: model.PolicyDrivers{ConfigSource: "repository", Materializer: "yaml-overlay"},
				Config:  model.PolicyConfig{SourceDir: "resources", Application: "application.yaml"},
			},
		},
		Services: map[string]model.Service{
			"api":   {Path: "api", Runner: model.Runner{Run: []string{"sleep", "600"}}},
			"order": {Path: "order", Runner: model.Runner{Run: []string{"sleep", "600"}}},
		},
	})
	var output strings.Builder
	session, err := Start(context.Background(), workspace, StartOptions{
		Common:   CommonOptions{Environment: "dev"},
		Services: []string{"api", "order"},
		Output:   &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer Stop(context.Background(), workspace, nil, true, false, &output)
	apiBefore := sessionProcess(session, "api")
	orderBefore := sessionProcess(session, "order")
	sentinel := filepath.Join(workspace.Store.CurrentDir, "configs", "order", "unchanged.txt")
	if err := os.WriteFile(sentinel, []byte("keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, "api", "source.txt"), []byte("two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	session, err = Restart(context.Background(), workspace, RestartOptions{Output: &output})
	if err != nil {
		t.Fatal(err)
	}
	if sessionProcess(session, "api").PID == apiBefore.PID {
		t.Fatal("changed api was not restarted")
	}
	if sessionProcess(session, "order").PID != orderBefore.PID {
		t.Fatal("unchanged order was restarted")
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "keep\n" {
		t.Fatalf("unchanged materialized config was replaced: data=%q err=%v", data, err)
	}
	if strings.Contains(output.String(), "Materializing order config") || !strings.Contains(output.String(), "Materializing api config") {
		t.Fatalf("restart materialization targets were wrong:\n%s", output.String())
	}
}

func TestRestartUsesPrepareCreatedRunWorkdir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspaceRoot := t.TempDir()
	serviceDirectory := filepath.Join(workspaceRoot, "api")
	if err := os.Mkdir(serviceDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(serviceDirectory, "source.txt")
	if err := os.WriteFile(sourcePath, []byte("one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{
		Version:   1,
		Workspace: model.Workspace{Name: "restart-run-workdir"},
		Services: map[string]model.Service{
			"api": {
				Path: "api",
				Runner: model.Runner{
					Prepare:    []string{"sh", "-c", `mkdir -p "$CONVEN_CONFIG_DIR/go" && printf 'prepared\n' > "$CONVEN_CONFIG_DIR/go/ready"`},
					RunWorkdir: "${runDir}/configs/${service}/go",
					Run:        []string{"sh", "-c", "pwd; test -f ready; while :; do sleep 1; done"},
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
		t.Fatal(err)
	}
	defer Stop(context.Background(), workspace, nil, true, false, &output)
	before := sessionProcess(session, "api")
	runWorkdir := filepath.Join(workspace.Store.CurrentDir, "configs", "api", "go")
	if err := os.RemoveAll(runWorkdir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, []byte("two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	session, err = Restart(context.Background(), workspace, RestartOptions{Output: &output})
	if err != nil {
		t.Fatalf("restart failed: %v\n%s", err, output.String())
	}
	after := sessionProcess(session, "api")
	if after.PID == before.PID {
		t.Fatal("service was not restarted")
	}
	data, err := os.ReadFile(after.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), runWorkdir) < 2 {
		t.Fatalf("start and restart did not both use %q: %s", runWorkdir, data)
	}
}

func TestRestartChecksRunWorkdirBeforeStoppingOldProcess(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspaceRoot := t.TempDir()
	serviceDirectory := filepath.Join(workspaceRoot, "api")
	if err := os.Mkdir(serviceDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(serviceDirectory, "source.txt")
	if err := os.WriteFile(sourcePath, []byte("one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{
		Version:   1,
		Workspace: model.Workspace{Name: "restart-missing-run-workdir"},
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
	session, err := Start(context.Background(), workspace, StartOptions{
		Common:   CommonOptions{Environment: "dev"},
		Services: []string{"api"},
		Output:   &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer Stop(context.Background(), workspace, nil, true, false, &output)
	before := sessionProcess(session, "api")
	if err := os.RemoveAll(filepath.Join(workspace.Store.CurrentDir, "configs", "api", "go")); err != nil {
		t.Fatal(err)
	}
	manifestService := workspace.Manifest.Services["api"]
	manifestService.Runner.Prepare = []string{"sh", "-c", "true"}
	workspace.Manifest.Services["api"] = manifestService
	if err := os.WriteFile(sourcePath, []byte("two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = Restart(context.Background(), workspace, RestartOptions{Output: &output})
	if err == nil || !strings.Contains(err.Error(), "run workdir") {
		t.Fatalf("restart error = %v\n%s", err, output.String())
	}
	after := sessionProcess(session, "api")
	if after.PID != before.PID || !ProcessAlive(before.PID) {
		t.Fatalf("old process changed before run workdir validation: before=%d after=%d alive=%v", before.PID, after.PID, ProcessAlive(before.PID))
	}
}

func TestRestartSkipBuildReusesCurrentRunArtifact(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspaceRoot := t.TempDir()
	serviceDirectory := filepath.Join(workspaceRoot, "api")
	if err := os.Mkdir(serviceDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(serviceDirectory, "source.txt")
	if err := os.WriteFile(sourcePath, []byte("one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{
		Version:   1,
		Workspace: model.Workspace{Name: "restart-artifact"},
		Services: map[string]model.Service{
			"api": {
				Path: "api",
				Runner: model.Runner{
					Build: []string{"sh", "-c", "printf '#!/bin/sh\\nexec sleep 600\\n' > \"$1\" && chmod +x \"$1\"", "sh", "${artifact}"},
					Run:   []string{"${artifact}"},
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
		t.Fatal(err)
	}
	defer Stop(context.Background(), workspace, nil, true, false, &output)
	before := sessionProcess(session, "api")
	if err := os.WriteFile(sourcePath, []byte("two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	session, err = Restart(context.Background(), workspace, RestartOptions{
		SkipBuild: true,
		Output:    &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sessionProcess(session, "api").PID == before.PID {
		t.Fatal("changed api was not restarted with the current run artifact")
	}
}

func TestRestartRejectsSymlinkedCurrentSubdirectory(t *testing.T) {
	workspaceRoot := t.TempDir()
	serviceDirectory := filepath.Join(workspaceRoot, "api")
	if err := os.Mkdir(serviceDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{
		Version:   1,
		Workspace: model.Workspace{Name: "restart-symlink"},
		Services: map[string]model.Service{
			"api": {
				Path:   "api",
				Runner: model.Runner{Run: []string{"sleep", "600"}},
			},
		},
	})
	var output strings.Builder
	_, err := Start(context.Background(), workspace, StartOptions{
		Common:   CommonOptions{Environment: "dev"},
		Services: []string{"api"},
		Output:   &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer Stop(context.Background(), workspace, nil, true, false, &output)
	artifactsDirectory := filepath.Join(workspace.Store.CurrentDir, "artifacts")
	if err := os.RemoveAll(artifactsDirectory); err != nil {
		t.Fatal(err)
	}
	externalDirectory := t.TempDir()
	externalSentinel := filepath.Join(externalDirectory, "sentinel")
	if err := os.WriteFile(externalSentinel, []byte("outside\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalDirectory, artifactsDirectory); err != nil {
		t.Fatal(err)
	}
	_, err = Restart(context.Background(), workspace, RestartOptions{Output: &output})
	if err == nil || !strings.Contains(err.Error(), "must be a real directory") {
		t.Fatalf("restart error = %v", err)
	}
	data, err := os.ReadFile(externalSentinel)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "outside\n" {
		t.Fatalf("external sentinel changed to %q", data)
	}
}

func TestStartDoesNotConsumeSourceChangesMadeDuringBuild(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspaceRoot := t.TempDir()
	workspace, sourcePath, markerPath, gatePath := blockingBuildWorkspace(t, workspaceRoot)
	var output strings.Builder
	type result struct {
		session *Session
		err     error
	}
	started := make(chan result, 1)
	go func() {
		session, err := Start(context.Background(), workspace, StartOptions{
			Common:   CommonOptions{Environment: "dev"},
			Services: []string{"api"},
			Output:   &output,
		})
		started <- result{session: session, err: err}
	}()
	waitForRestartTestFile(t, markerPath)
	if err := os.WriteFile(sourcePath, []byte("changed during build\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gatePath, []byte("continue\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var startResult result
	select {
	case startResult = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("start did not finish after releasing the build gate")
	}
	if startResult.err != nil {
		t.Fatal(startResult.err)
	}
	defer Stop(context.Background(), workspace, nil, true, false, &output)
	before := sessionProcess(startResult.session, "api")
	session, err := Restart(context.Background(), workspace, RestartOptions{Output: &output})
	if err != nil {
		t.Fatal(err)
	}
	if sessionProcess(session, "api").PID == before.PID {
		t.Fatal("source change made during start build was incorrectly recorded as loaded")
	}
}

func TestRestartDoesNotConsumeSourceChangesMadeDuringBuild(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspaceRoot := t.TempDir()
	workspace, sourcePath, markerPath, gatePath := blockingBuildWorkspace(t, workspaceRoot)
	if err := os.WriteFile(gatePath, []byte("initial start\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	session, err := Start(context.Background(), workspace, StartOptions{
		Common:   CommonOptions{Environment: "dev"},
		Services: []string{"api"},
		Output:   &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer Stop(context.Background(), workspace, nil, true, false, &output)
	if err := os.Remove(markerPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(gatePath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, []byte("first change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	type result struct {
		session *Session
		err     error
	}
	restarted := make(chan result, 1)
	go func() {
		restartedSession, restartErr := Restart(context.Background(), workspace, RestartOptions{Output: &output})
		restarted <- result{session: restartedSession, err: restartErr}
	}()
	waitForRestartTestFile(t, markerPath)
	if err := os.WriteFile(sourcePath, []byte("second change during build\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gatePath, []byte("continue\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var firstRestart result
	select {
	case firstRestart = <-restarted:
	case <-time.After(5 * time.Second):
		t.Fatal("restart did not finish after releasing the build gate")
	}
	if firstRestart.err != nil {
		t.Fatal(firstRestart.err)
	}
	firstRestartPID := sessionProcess(firstRestart.session, "api").PID
	if firstRestartPID == sessionProcess(session, "api").PID {
		t.Fatal("first source change did not restart api")
	}
	secondRestart, err := Restart(context.Background(), workspace, RestartOptions{Output: &output})
	if err != nil {
		t.Fatal(err)
	}
	if sessionProcess(secondRestart, "api").PID == firstRestartPID {
		t.Fatal("source change made during restart build was incorrectly recorded as loaded")
	}
}

func blockingBuildWorkspace(t *testing.T, workspaceRoot string) (*WorkspaceData, string, string, string) {
	t.Helper()
	serviceDirectory := filepath.Join(workspaceRoot, "api")
	if err := os.Mkdir(serviceDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(serviceDirectory, "source.txt")
	if err := os.WriteFile(sourcePath, []byte("initial\n"), 0600); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(workspaceRoot, "build.started")
	gatePath := filepath.Join(workspaceRoot, "build.continue")
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{
		Version:   1,
		Workspace: model.Workspace{Name: "restart-build-race"},
		Services: map[string]model.Service{
			"api": {
				Path: "api",
				Runner: model.Runner{
					Build: []string{"sh", "-c", "touch \"$1\"; while [ ! -e \"$2\" ]; do sleep 0.05; done; printf '#!/bin/sh\\nexec sleep 600\\n' > \"$3\"; chmod +x \"$3\"", "sh", markerPath, gatePath, "${artifact}"},
					Run:   []string{"${artifact}"},
				},
			},
		},
	})
	return workspace, sourcePath, markerPath, gatePath
}

func waitForRestartTestFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
