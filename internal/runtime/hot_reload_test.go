package runtime

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestHotReloadKeepsLastKnownGoodProcessUntilBuildSucceeds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspaceRoot := t.TempDir()
	serviceDirectory := filepath.Join(workspaceRoot, "api")
	if err := os.Mkdir(serviceDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(serviceDirectory, "service.sh")
	writeHotReloadSource(t, sourcePath, "one")
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{
		Version:   1,
		Workspace: model.Workspace{Name: "hot-reload"},
		Services: map[string]model.Service{
			"api": hotReloadTestService("api", sourcePath),
		},
	})
	var startOutput strings.Builder
	session, err := Start(context.Background(), workspace, StartOptions{
		Common:   CommonOptions{Environment: "dev"},
		Services: []string{"api"},
		Output:   &startOutput,
	})
	if err != nil {
		t.Fatal(err)
	}
	before := sessionProcess(session, "api")
	watchContext, cancelWatch := context.WithCancel(context.Background())
	watchDone := make(chan error, 1)
	go func() {
		watchDone <- watchHotReload(watchContext, workspace, HotReloadOptions{
			PollInterval: 20 * time.Millisecond,
			Debounce:     40 * time.Millisecond,
			Output:       io.Discard,
		})
	}()
	defer func() {
		cancelWatch()
		select {
		case err := <-watchDone:
			if err != nil {
				t.Errorf("hot reload watcher failed: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("hot reload watcher did not stop")
		}
		if err := Stop(context.Background(), workspace, nil, true, false, io.Discard); err != nil {
			t.Errorf("stop failed: %v", err)
		}
	}()

	if err := os.WriteFile(sourcePath, []byte("COMPILE_ERROR\n"), 0700); err != nil {
		t.Fatal(err)
	}
	waitForHotReloadCondition(t, func() bool {
		data, err := os.ReadFile(before.LogPath)
		return err == nil && strings.Contains(string(data), "api/main.go:7: syntax error: unexpected }") && strings.Contains(string(data), "last-known-good service remains running")
	})
	afterFailure, err := workspace.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	failedProcess := sessionProcess(afterFailure, "api")
	if failedProcess.PID != before.PID || !ProcessAlive(before.PID) || VerifyProcess(failedProcess) != nil {
		t.Fatalf("failed build replaced the service: before=%d after=%d alive=%v", before.PID, failedProcess.PID, ProcessAlive(before.PID))
	}

	writeHotReloadSource(t, sourcePath, "two")
	waitForHotReloadCondition(t, func() bool {
		current, err := workspace.Store.Load()
		if err != nil || current == nil {
			return false
		}
		process := sessionProcess(current, "api")
		if process.PID <= 0 || process.PID == before.PID || !ProcessAlive(process.PID) || VerifyProcess(process) != nil {
			return false
		}
		data, err := os.ReadFile(before.LogPath)
		return err == nil && strings.Contains(string(data), "hot reload complete: api") && strings.Contains(string(data), "version two")
	})
	data, err := os.ReadFile(before.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "hot reload complete: api") || !strings.Contains(string(data), "version two") {
		t.Fatalf("successful reload was not visible in the service log: %s", data)
	}
}

func TestHotReloadBuildFailureDoesNotBlockAnotherChangedService(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspaceRoot := t.TempDir()
	sources := make(map[string]string)
	services := make(map[string]model.Service)
	for _, name := range []string{"api", "worker"} {
		directory := filepath.Join(workspaceRoot, name)
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
		sourcePath := filepath.Join(directory, "service.sh")
		writeHotReloadSource(t, sourcePath, "one")
		sources[name] = sourcePath
		services[name] = hotReloadTestService(name, sourcePath)
	}
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{
		Version:   1,
		Workspace: model.Workspace{Name: "independent-hot-reload"},
		Services:  services,
	})
	session, err := Start(context.Background(), workspace, StartOptions{
		Common:   CommonOptions{Environment: "dev"},
		Services: []string{"api", "worker"},
		Output:   io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	apiBefore := sessionProcess(session, "api")
	workerBefore := sessionProcess(session, "worker")
	watchContext, cancelWatch := context.WithCancel(context.Background())
	watchDone := make(chan error, 1)
	go func() {
		watchDone <- watchHotReload(watchContext, workspace, HotReloadOptions{
			PollInterval: 20 * time.Millisecond,
			Debounce:     40 * time.Millisecond,
			Output:       io.Discard,
		})
	}()
	defer func() {
		cancelWatch()
		select {
		case err := <-watchDone:
			if err != nil {
				t.Errorf("hot reload watcher failed: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("hot reload watcher did not stop")
		}
		if err := Stop(context.Background(), workspace, nil, true, false, io.Discard); err != nil {
			t.Errorf("stop failed: %v", err)
		}
	}()

	if err := os.WriteFile(sources["api"], []byte("COMPILE_ERROR\n"), 0700); err != nil {
		t.Fatal(err)
	}
	writeHotReloadSource(t, sources["worker"], "two")
	waitForHotReloadCondition(t, func() bool {
		current, err := workspace.Store.Load()
		if err != nil || current == nil {
			return false
		}
		api := sessionProcess(current, "api")
		worker := sessionProcess(current, "worker")
		return api.PID == apiBefore.PID && ProcessAlive(api.PID) && worker.PID != workerBefore.PID && ProcessAlive(worker.PID)
	})
	apiLog, err := os.ReadFile(apiBefore.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(apiLog), "api/main.go:7: syntax error: unexpected }") {
		t.Fatalf("api compiler failure is missing: %s", apiLog)
	}
}

func TestStartRegistersHotReloadWatcherAndStopAllTerminatesIt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspaceRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspaceRoot, "api"), 0700); err != nil {
		t.Fatal(err)
	}
	watcherExecutable := filepath.Join(t.TempDir(), "fake-conven")
	if err := os.WriteFile(watcherExecutable, []byte("#!/bin/sh\nwhile :; do sleep 1; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	workspace := testWorkspace(t, workspaceRoot, &model.Manifest{
		Version:   1,
		Workspace: model.Workspace{Name: "watcher-lifecycle"},
		Services: map[string]model.Service{
			"api": {
				Path:   "api",
				Runner: model.Runner{Run: []string{"sleep", "600"}},
			},
		},
	})
	var output strings.Builder
	session, err := Start(context.Background(), workspace, StartOptions{
		Common:              CommonOptions{Environment: "dev"},
		Services:            []string{"api"},
		HotReloadExecutable: watcherExecutable,
		Output:              &output,
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.HotReload == nil || !ProcessAlive(session.HotReload.PID) || VerifyProcess(*session.HotReload) != nil {
		t.Fatalf("hot reload watcher was not registered: %#v", session.HotReload)
	}
	var statusOutput strings.Builder
	if err := Status(context.Background(), workspace, &statusOutput); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(statusOutput.String(), "Hot reload: watching") {
		t.Fatalf("status does not show the active watcher: %s", statusOutput.String())
	}
	watcherPID := session.HotReload.PID
	if err := Stop(context.Background(), workspace, nil, true, false, &output); err != nil {
		t.Fatalf("stop all failed: %v\n%s", err, output.String())
	}
	if ProcessAlive(watcherPID) {
		t.Fatalf("hot reload watcher %d is still alive", watcherPID)
	}
	loaded, err := workspace.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded != nil {
		t.Fatalf("session remains after stop all: %#v", loaded)
	}
}

func writeHotReloadSource(t *testing.T, path string, version string) {
	t.Helper()
	contents := "#!/bin/sh\nprintf 'version " + version + "\\n'\nwhile :; do sleep 1; done\n"
	if err := os.WriteFile(path, []byte(contents), 0700); err != nil {
		t.Fatal(err)
	}
}

func hotReloadTestService(name string, sourcePath string) model.Service {
	return model.Service{
		Path: name,
		Runner: model.Runner{
			Build: []string{"sh", "-c", `if grep -q COMPILE_ERROR "$1"; then printf '%s/main.go:7: syntax error: unexpected }\n' "$3" >&2; exit 1; fi; cp "$1" "$2.next"; chmod +x "$2.next"; mv "$2.next" "$2"`, "sh", sourcePath, "${artifact}", name},
			Run:   []string{"${artifact}"},
		},
	}
}

func waitForHotReloadCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for hot reload condition")
}
