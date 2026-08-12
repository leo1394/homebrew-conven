package runtime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBuildKtctlConnectionCommand(t *testing.T) {
	actual, err := BuildConnectionCommand(ConnectionConfig{
		Driver:     "ktctl",
		Kubeconfig: "/tmp/dev config",
		Context:    "dev-context",
		Namespace:  "dev",
		Args:       []string{"--debug"},
	})
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{
		"ktctl",
		"--kubeconfig", "/tmp/dev config",
		"--context", "dev-context",
		"--namespace", "dev",
		"connect",
		"--debug",
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("command = %#v, want %#v", actual, expected)
	}
}

func TestBuildElevatedKtctlCommandsUsesResolvedExecutableAndKubeconfig(t *testing.T) {
	directory := t.TempDir()
	ktctl := filepath.Join(directory, "custom-ktctl")
	if err := os.WriteFile(ktctl, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	managed, launch, err := buildConnectionCommands(ConnectionConfig{
		Driver:     "ktctl",
		Command:    "custom-ktctl",
		Kubeconfig: "/tmp/ktctl-config",
		Sudo:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantManaged := []string{ktctl, "--kubeconfig", "/tmp/ktctl-config", "connect"}
	wantLaunch := []string{"sudo", "-n", ktctl, "--kubeconfig", "/tmp/ktctl-config", "connect"}
	if !reflect.DeepEqual(managed, wantManaged) {
		t.Fatalf("managed command = %#v, want %#v", managed, wantManaged)
	}
	if !reflect.DeepEqual(launch, wantLaunch) {
		t.Fatalf("launch command = %#v, want %#v", launch, wantLaunch)
	}
}

func TestBuildCommandConnection(t *testing.T) {
	actual, err := BuildConnectionCommand(ConnectionConfig{
		Driver:  "command",
		Command: "vpn-up",
		Args:    []string{"--profile", "dev"},
	})
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"vpn-up", "--profile", "dev"}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("command = %#v, want %#v", actual, expected)
	}
}

func TestEnsureConnectionKeepsInteractiveSudoInForegroundAndReportsAuthorization(t *testing.T) {
	directory := t.TempDir()
	fakeSudo := filepath.Join(directory, "sudo")
	pgidPath := filepath.Join(directory, "sudo-auth-pgid")
	script := `#!/bin/sh
if [ "$1" = "-n" ] && [ "$2" = "-v" ]; then
  exit 0
fi
if [ "$1" = "-v" ]; then
  ps -o pgid= -p "$$" | tr -d ' ' > "$CONVEN_TEST_SUDO_AUTH_PGID"
  exit 0
fi
if [ "$1" = "-n" ]; then
  shift
  "$@" &
  child=$!
  wait "$child"
  exit $?
fi
exit 1
`
	if err := os.WriteFile(fakeSudo, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CONVEN_TEST_SUDO_AUTH_PGID", pgidPath)
	t.Setenv("HOME", t.TempDir())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	t.Setenv("CONVEN_CONNECTION_HELPER", "1")
	t.Setenv("CONVEN_CONNECTION_HELPER_ADDRESS", address)
	var output bytes.Buffer
	process, err := EnsureConnection(context.Background(), ConnectionConfig{
		Driver:  "command",
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestConnectionHelperProcess$"},
		Sudo:    true,
		Timeout: 2 * time.Second,
		Readiness: []ConnectionEndpoint{
			{Name: "helper", Address: address},
		},
	}, filepath.Join(directory, "connection.log"), "sudo-auth-workspace", &output)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stopConnection(process, true)
		_ = removeConnectionRecord(process.Fingerprint)
	}()
	data, err := os.ReadFile(pgidPath)
	if err != nil {
		t.Fatal(err)
	}
	authorizationPGID, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	foregroundPGID, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if authorizationPGID != foregroundPGID {
		t.Fatalf("interactive sudo process group = %d, want foreground process group %d", authorizationPGID, foregroundPGID)
	}
	if !strings.Contains(output.String(), "password input is hidden") {
		t.Fatalf("authorization output does not explain hidden password input: %q", output.String())
	}
	if !strings.Contains(output.String(), "Sudo authorization confirmed.") {
		t.Fatalf("authorization output does not confirm completion: %q", output.String())
	}
}

func TestAuthorizeSudoReturnsOnContextCancellation(t *testing.T) {
	directory := t.TempDir()
	fakeSudo := filepath.Join(directory, "sudo")
	startedPath := filepath.Join(directory, "sudo-auth-started")
	script := `#!/bin/sh
if [ "$1" = "-v" ]; then
  : > "$CONVEN_TEST_SUDO_STARTED"
  while :; do :; done
fi
exit 1
`
	if err := os.WriteFile(fakeSudo, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CONVEN_TEST_SUDO_STARTED", startedPath)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- authorizeSudo(ctx, &output)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(startedPath); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("interactive sudo command did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("authorization error = %v, want context cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("interactive sudo command did not stop after context cancellation")
	}
	if strings.Contains(output.String(), "Sudo authorization confirmed.") {
		t.Fatalf("cancelled authorization reported success: %q", output.String())
	}
}

func TestConnectionLogPathLivesDirectlyUnderWorkspaceRuntime(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), ".conven", "runtime")
	want := filepath.Join(runtimeDir, "connection.log")
	if got := ConnectionLogPath(runtimeDir); got != want {
		t.Fatalf("connection log path = %q, want %q", got, want)
	}
}

func TestConnectionStateDirectoryUsesConvenHome(t *testing.T) {
	convenHome := t.TempDir()
	t.Setenv("HOME", convenHome)
	directory, err := connectionStateDirectory()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(convenHome, ".conven", "state", "connections")
	if directory != want {
		t.Fatalf("connection state directory = %q, want %q", directory, want)
	}
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("connection state directory permissions = %o, want 700", info.Mode().Perm())
	}
}

func TestConnectionStateDirectoryIgnoresXDGStateHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "ignored"))
	directory, err := connectionStateDirectory()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".conven", "state", "connections")
	if directory != want {
		t.Fatalf("connection state directory = %q, want %q", directory, want)
	}
}

func TestConnectionStateDirectoryProtectsExistingDirectories(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".conven")
	state := filepath.Join(root, "state")
	directory := filepath.Join(state, "connections")
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, state, directory} {
		if err := os.Chmod(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := connectionStateDirectory(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, state, directory} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0700 {
			t.Fatalf("directory %q permissions = %o, want 700", path, info.Mode().Perm())
		}
	}
}

func TestConnectionStateDirectoryRejectsSymlinkComponents(t *testing.T) {
	for _, component := range []string{"home", "state", "connections"} {
		t.Run(component, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			root := filepath.Join(home, ".conven")
			state := filepath.Join(root, "state")
			directory := filepath.Join(state, "connections")
			target := t.TempDir()
			switch component {
			case "home":
				if err := os.Symlink(target, root); err != nil {
					t.Fatal(err)
				}
			case "state":
				if err := os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, state); err != nil {
					t.Fatal(err)
				}
			case "connections":
				if err := os.MkdirAll(state, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, directory); err != nil {
					t.Fatal(err)
				}
			}
			_, err := connectionStateDirectory()
			if err == nil || !strings.Contains(err.Error(), "symbolic links are not allowed") {
				t.Fatalf("error = %v, want symbolic link rejection", err)
			}
		})
	}
}

func TestEnsureConnectionExitReportsStatusLogAndEndpoints(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	directory := t.TempDir()
	logPath := filepath.Join(directory, "connection.log")
	ktctl := filepath.Join(directory, "ktctl")
	if err := os.WriteFile(ktctl, []byte("#!/bin/sh\nprintf '\\033[31mPost /api/v1/namespaces/default/pods: EOF\\033[0m\\n'\nsleep 0.5\nexit 7\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	startedAt := time.Now()

	process, err := EnsureConnection(context.Background(), ConnectionConfig{
		Driver:     "ktctl",
		Command:    ktctl,
		Timeout:   3 * time.Second,
		Readiness: []ConnectionEndpoint{{Name: "cluster-api", Address: address}},
	}, logPath, "exit-diagnostics-workspace", &output)
	if err == nil {
		t.Fatal("exited connection unexpectedly became ready")
	}
	if process != nil {
		t.Fatalf("exited connection returned residual process: %#v", process)
	}
	if elapsed := time.Since(startedAt); elapsed >= 2*time.Second {
		t.Fatalf("exited connection was not reported promptly: %s", elapsed)
	}
	for _, expected := range []string{"exit status 7", logPath} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("exit error %q does not contain %q", err, expected)
		}
	}
	for _, expected := range []string{"first-time shadow pod creation", "cluster-api", address, "secrets are not redacted", "Post /api/v1/namespaces/default/pods: EOF"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("connection diagnostics %q do not contain %q", output.String(), expected)
		}
	}
	if strings.Contains(output.String(), "\x1b") {
		t.Fatalf("connection diagnostics retained raw terminal escapes: %q", output.String())
	}
}

func TestEnsureConnectionDoesNotRetryUnknownKtctlPodCreate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	directory := t.TempDir()
	logPath := filepath.Join(directory, "connection.log")
	attemptsPath := filepath.Join(directory, "attempts")
	pidPath := filepath.Join(directory, "pid")
	ktctl := filepath.Join(directory, "ktctl")
	script := `#!/bin/sh
attempts=0
if [ -f "$CONVEN_TEST_CONNECTION_ATTEMPTS" ]; then
  attempts=$(cat "$CONVEN_TEST_CONNECTION_ATTEMPTS")
fi
attempts=$((attempts + 1))
printf '%s\n' "$attempts" > "$CONVEN_TEST_CONNECTION_ATTEMPTS"
printf '%s\n' "$$" > "$CONVEN_TEST_CONNECTION_PID"
printf '\033[31mERR Exit: Post \"https://cluster.example/api/v1/namespaces/test/pods\": EOF\033[0m\n'
sleep 0.5
exit 0
`
	if err := os.WriteFile(ktctl, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONVEN_TEST_CONNECTION_ATTEMPTS", attemptsPath)
	t.Setenv("CONVEN_TEST_CONNECTION_PID", pidPath)
	var output bytes.Buffer

	process, err := EnsureConnection(context.Background(), ConnectionConfig{
		Driver:     "ktctl",
		Command:    ktctl,
		Namespace:  "test",
		Timeout:    3 * time.Second,
		Readiness:  []ConnectionEndpoint{{Name: "cluster-api", Address: address}},
	}, logPath, "pod-create-eof-workspace", &output)
	if err == nil {
		t.Fatal("uncertain Pod creation unexpectedly became ready")
	}
	if process != nil {
		t.Fatalf("uncertain Pod creation returned residual process: %#v", process)
	}
	for _, expected := range []string{"Kubernetes Pod CREATE EOF", "remote shadow pod state is unknown", "did not retry automatically", "Kubernetes namespace \"test\"", logPath} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("connection error %q does not contain %q", err, expected)
		}
	}
	if strings.Contains(err.Error(), "exit status 0") {
		t.Fatalf("connection error reports misleading success status: %q", err)
	}
	attempts, err := os.ReadFile(attemptsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(attempts)) != "1" {
		t.Fatalf("connection attempts = %q, want one fail-closed attempt", attempts)
	}
	pidData, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		t.Fatal(err)
	}
	if ProcessGroupAlive(pid) {
		t.Fatalf("failed connection process group %d is still active", pid)
	}
	stateDirectory, err := connectionStateDirectory()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(stateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			t.Fatalf("uncertain Pod creation retained registry record %q", entry.Name())
		}
	}
}

func TestEnsureConnectionReportsElevatedTargetExit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	directory := t.TempDir()
	fakeSudo := filepath.Join(directory, "sudo")
	pidPath := filepath.Join(directory, "elevated.pid")
	script := `#!/bin/sh
if [ "$1" = "-v" ]; then
  exit 0
fi
if [ "$1" = "-n" ] && [ "$2" = "-v" ]; then
  exit 0
fi
if [ "$1" = "-n" ]; then
  shift
  echo 'Post /api/v1/namespaces/default/pods: EOF'
  "$1" >/dev/null 2>&1 &
  child=$!
  echo "$child" > "$CONVEN_TEST_ELEVATED_PID"
  sleep 0.5
  kill "$child" 2>/dev/null || true
  wait "$child" 2>/dev/null || true
  exit 7
fi
exit 1
`
	if err := os.WriteFile(fakeSudo, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CONVEN_TEST_ELEVATED_PID", pidPath)
	yesPath, err := exec.LookPath("yes")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	var output bytes.Buffer
	process, err := EnsureConnection(context.Background(), ConnectionConfig{
		Driver:     "ktctl",
		Command:    yesPath,
		Sudo:       true,
		Timeout:    3 * time.Second,
		Readiness:  []ConnectionEndpoint{{Name: "cluster-api", Address: address}},
	}, filepath.Join(directory, "connection.log"), "elevated-exit-workspace", &output)
	if err == nil || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("elevated connection process=%#v error=%v, want exit status 7", process, err)
	}
	if process != nil {
		t.Fatalf("elevated connection returned residual process: %#v", process)
	}
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if ProcessAlive(pid) {
		t.Fatalf("elevated connection target %d remains active", pid)
	}
	if !strings.Contains(output.String(), "Post /api/v1/namespaces/default/pods: EOF") {
		t.Fatalf("elevated connection diagnostics = %q", output.String())
	}
}

func TestConnectionProcessStateTreatsZombieAsExited(t *testing.T) {
	for _, test := range []struct {
		state string
		alive bool
	}{
		{state: "S+", alive: true},
		{state: "R", alive: true},
		{state: "Z"},
		{state: "Z+"},
		{state: ""},
	} {
		if actual := connectionProcessStateAlive(test.state); actual != test.alive {
			t.Fatalf("process state %q alive = %t, want %t", test.state, actual, test.alive)
		}
	}
}

func TestEnsureConnectionCancellationWinsOverConcurrentExit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	directory := t.TempDir()
	startedPath := filepath.Join(directory, "started")
	exitPath := filepath.Join(directory, "exit")
	ktctl := filepath.Join(directory, "ktctl")
	if err := os.WriteFile(ktctl, []byte("#!/bin/sh\necho 'connection detail should stay in the private log'\n: > \"$CONVEN_TEST_CONNECTION_STARTED\"\nwhile [ ! -e \"$CONVEN_TEST_CONNECTION_EXIT\" ]; do sleep 0.01; done\nexit 7\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONVEN_TEST_CONNECTION_STARTED", startedPath)
	t.Setenv("CONVEN_TEST_CONNECTION_EXIT", exitPath)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output bytes.Buffer
	type result struct {
		process *ConnectionProcess
		err     error
	}
	done := make(chan result, 1)
	go func() {
		process, err := EnsureConnection(ctx, ConnectionConfig{
			Driver:     "ktctl",
			Command:    ktctl,
			Timeout:    5 * time.Second,
			Readiness:  []ConnectionEndpoint{{Name: "cancel-target", Address: address}},
		}, filepath.Join(directory, "connection.log"), "cancel-workspace", &output)
		done <- result{process: process, err: err}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(startedPath); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("connection process did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := os.WriteFile(exitPath, []byte("exit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.process != nil {
			t.Fatalf("cancelled connection returned residual process: %#v; error=%v", got.process, got.err)
		}
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("cancelled connection error = %v, want context.Canceled", got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled connection remained blocked")
	}
	if strings.Contains(output.String(), "Readiness cancel-target (") || strings.Contains(output.String(), "connection detail should stay") {
		t.Fatalf("cancelled connection emitted post-cancel diagnostics: %q", output.String())
	}
}

func TestEnsureConnectionTimeoutStopsOwnedProcessGroup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	directory := t.TempDir()
	pidPath := filepath.Join(directory, "connection.pid")
	logPath := filepath.Join(directory, "connection.log")
	ktctl := filepath.Join(directory, "ktctl")
	if err := os.WriteFile(ktctl, []byte("#!/bin/sh\necho $$ > \"$CONVEN_TEST_CONNECTION_PID\"\necho 'ERR Exit: Post /api/v1/namespaces/default/pods: EOF'\ntrap '' INT TERM\nwhile :; do sleep 1; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONVEN_TEST_CONNECTION_PID", pidPath)
	var output bytes.Buffer

	process, err := EnsureConnection(context.Background(), ConnectionConfig{
		Driver:     "ktctl",
		Command:    ktctl,
		Timeout:   750 * time.Millisecond,
		Readiness: []ConnectionEndpoint{{Name: "closed-endpoint", Address: address}},
	}, logPath, "test-workspace", &output)
	if err == nil {
		t.Fatal("unreachable connection unexpectedly became ready")
	}
	if process != nil {
		t.Fatalf("cleaned connection returned residual process: %#v", process)
	}
	data, readErr := os.ReadFile(pidPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	if ProcessGroupAlive(pid) {
		t.Fatalf("connection process group %d is still active", pid)
	}
	for _, expected := range []string{"closed-endpoint", address, "ERR Exit: Post /api/v1/namespaces/default/pods: EOF"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("timeout diagnostics %q do not contain %q", output.String(), expected)
		}
	}
	if !strings.Contains(err.Error(), logPath) {
		t.Fatalf("timeout error %q does not contain log path %q", err, logPath)
	}
	stateDirectory, err := connectionStateDirectory()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(stateDirectory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			t.Fatalf("failed connection retained registry record %q", entry.Name())
		}
	}
}

func TestStartConnectionTruncatesExistingLog(t *testing.T) {
	directory := t.TempDir()
	logPath := filepath.Join(directory, "connection.log")
	if err := os.WriteFile(logPath, []byte("stale connection output\n"), 0600); err != nil {
		t.Fatal(err)
	}
	argv := []string{"sleep", "600"}
	process, err := startConnection(context.Background(), "command", argv, argv, logPath, "fresh-log", false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stopConnection(process, true)
	}()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("new connection retained stale log output: %q", data)
	}
}

func TestForceReleaseCleansUnverifiedConnectionGroup(t *testing.T) {
	directory := t.TempDir()
	argv := []string{"sh", "-c", "trap '' HUP; sleep 600 & sleep 0.2"}
	process, err := startConnection(context.Background(), "command", argv, argv, filepath.Join(directory, "connection.log"), "force-test", false)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if ProcessAlive(process.PID) || !ProcessGroupAlive(process.PGID) {
		t.Fatalf("connection did not reach orphaned group state: %#v", process)
	}
	if err := releaseConnection(context.Background(), process, "workspace", true, io.Discard); err != nil {
		t.Fatal(err)
	}
	if ProcessGroupAlive(process.PGID) {
		t.Fatalf("force release left process group %d active", process.PGID)
	}
}

func TestElevatedStopRefreshesExpiredSudoAuthorization(t *testing.T) {
	directory := t.TempDir()
	fakeSudo := filepath.Join(directory, "sudo")
	marker := filepath.Join(directory, "authorized")
	script := `#!/bin/sh
if [ "$1" = "-n" ] && [ "$2" = "-v" ]; then
  exit 1
fi
if [ "$1" = "-v" ]; then
  : > "$CONVEN_SUDO_MARKER"
  exit 0
fi
if [ "$1" = "-n" ] && [ "$2" = "/bin/kill" ]; then
  shift 2
  exec /bin/kill "$@"
fi
exit 1
`
	if err := os.WriteFile(fakeSudo, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CONVEN_SUDO_MARKER", marker)
	argv := []string{"sleep", "600"}
	process, err := startConnection(context.Background(), "command", argv, argv, filepath.Join(directory, "connection.log"), "sudo-refresh", false)
	if err != nil {
		t.Fatal(err)
	}
	process.Elevated = true
	defer func() {
		process.Elevated = false
		_ = stopConnection(process, true)
	}()
	if err := stopConnection(process, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("interactive sudo refresh was not invoked: %v", err)
	}
	if ProcessGroupAlive(process.PGID) {
		t.Fatalf("elevated process group %d is still active", process.PGID)
	}
}

func TestManagedConnectionLeasesPreventEarlyStop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	t.Setenv("CONVEN_CONNECTION_HELPER", "1")
	t.Setenv("CONVEN_CONNECTION_HELPER_ADDRESS", address)
	config := ConnectionConfig{
		Driver:     "command",
		Command:    os.Args[0],
		Args:       []string{"-test.run=^TestConnectionHelperProcess$"},
		Timeout:    2 * time.Second,
		Readiness:  []ConnectionEndpoint{{Name: "helper", Address: address}},
	}
	directory := t.TempDir()
	first, err := EnsureConnection(context.Background(), config, filepath.Join(directory, "first.log"), "workspace-a", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stopConnection(first, true)
		_ = removeConnectionRecord(first.Fingerprint)
	}()
	second, err := EnsureConnection(context.Background(), config, filepath.Join(directory, "second.log"), "workspace-b", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Managed || !second.Managed || second.Owned || first.PID != second.PID {
		t.Fatalf("unexpected shared connection processes: first=%#v second=%#v", first, second)
	}
	if err := releaseConnection(context.Background(), first, "workspace-a", false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !ProcessAlive(first.PID) {
		t.Fatal("first lease release stopped a connection still leased by workspace-b")
	}
	record, err := loadConnectionRecord(first.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if record == nil || len(record.Leases) != 1 {
		t.Fatalf("leases after first release = %#v", record)
	}
	if err := releaseConnection(context.Background(), second, "workspace-b", false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if ProcessGroupAlive(first.PGID) {
		t.Fatalf("final lease release left process group %d active", first.PGID)
	}
	record, err = loadConnectionRecord(first.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if record != nil {
		t.Fatalf("final lease release retained record: %#v", record)
	}
}

func TestManagedConnectionSecondLeasePreservesOriginalLog(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	t.Setenv("CONVEN_CONNECTION_HELPER", "1")
	t.Setenv("CONVEN_CONNECTION_HELPER_ADDRESS", address)
	config := ConnectionConfig{
		Driver:     "command",
		Command:    os.Args[0],
		Args:       []string{"-test.run=^TestConnectionHelperProcess$"},
		Timeout:    2 * time.Second,
		Readiness:  []ConnectionEndpoint{{Name: "helper", Address: address}},
	}
	directory := t.TempDir()
	firstLog := filepath.Join(directory, "first.log")
	first, err := EnsureConnection(context.Background(), config, firstLog, "workspace-a", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stopConnection(first, true)
		_ = removeConnectionRecord(first.Fingerprint)
	}()
	firstContent := []byte("owned connection log\n")
	if err := os.WriteFile(firstLog, firstContent, 0600); err != nil {
		t.Fatal(err)
	}
	secondLog := filepath.Join(directory, "second.log")
	secondContent := []byte("second workspace sentinel\n")
	if err := os.WriteFile(secondLog, secondContent, 0600); err != nil {
		t.Fatal(err)
	}
	second, err := EnsureConnection(context.Background(), config, secondLog, "workspace-b", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Managed || second.Owned {
		t.Fatalf("second lease did not reuse managed connection: %#v", second)
	}
	if second.LogPath != firstLog {
		t.Fatalf("second lease log path = %q, want original %q", second.LogPath, firstLog)
	}
	data, err := os.ReadFile(firstLog)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(data, firstContent) {
		t.Fatalf("original connection log changed during reuse: %q", data)
	}
	data, err = os.ReadFile(secondLog)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(data, secondContent) {
		t.Fatalf("second lease log was truncated during reuse: %q", data)
	}
	if err := releaseConnection(context.Background(), first, "workspace-a", false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := releaseConnection(context.Background(), second, "workspace-b", false, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestKtctlRegistryRejectsSecondStateRootConnection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	directory := t.TempDir()
	service, err := StartService("existing-ktctl", []string{"sleep", "600"}, directory, CommandEnvironment(), filepath.Join(directory, "existing.log"))
	if err != nil {
		t.Fatal(err)
	}
	record := &connectionRecord{
		Version:     1,
		Fingerprint: "existing",
		Process: ConnectionProcess{
			Driver:      "ktctl",
			PID:         service.PID,
			PGID:        service.PGID,
			Command:     service.Command,
			Identity:    service.Identity,
			Owned:       true,
			Managed:     true,
			Fingerprint: "existing",
		},
		Leases: map[string]time.Time{"workspace-a": time.Now()},
	}
	if err := saveConnectionRecord(record); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = StopProcess(service, time.Second)
		_ = removeConnectionRecord("existing")
	}()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	process, err := EnsureConnection(context.Background(), ConnectionConfig{
		Driver:     "ktctl",
		Command:    "different-ktctl",
		Timeout:    100 * time.Millisecond,
		Readiness:  []ConnectionEndpoint{{Name: "closed", Address: address}},
	}, filepath.Join(directory, "second.log"), "workspace-b", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "another ktctl connection is active in this Conven state root") {
		t.Fatalf("process=%#v error=%v, want shared connection conflict", process, err)
	}
}

func TestKtctlRegistryIgnoresManagedCommandConnection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	directory := t.TempDir()
	service, err := StartService("existing-command", []string{"sleep", "600"}, directory, CommandEnvironment(), filepath.Join(directory, "existing.log"))
	if err != nil {
		t.Fatal(err)
	}
	record := &connectionRecord{
		Version:     1,
		Fingerprint: "existing-command",
		Process: ConnectionProcess{
			Driver:      "command",
			PID:         service.PID,
			PGID:        service.PGID,
			Command:     service.Command,
			Identity:    "unverified-command-identity",
			Owned:       true,
			Managed:     true,
			Fingerprint: "existing-command",
		},
		Leases: map[string]time.Time{"workspace-a": time.Now()},
	}
	if err := saveConnectionRecord(record); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = StopProcess(service, time.Second)
		_ = removeConnectionRecord(record.Fingerprint)
	}()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	process, err := EnsureConnection(context.Background(), ConnectionConfig{
		Driver:     "ktctl",
		Command:    "conven-ktctl-that-does-not-exist",
		Timeout:    100 * time.Millisecond,
		Readiness:  []ConnectionEndpoint{{Name: "closed", Address: address}},
	}, filepath.Join(directory, "second.log"), "workspace-b", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("process=%#v error=%v, want missing command error", process, err)
	}
	if strings.Contains(err.Error(), "another ktctl connection") {
		t.Fatalf("managed command connection incorrectly blocked ktctl: %v", err)
	}
}

func TestPruneConnectionLeasesRemovesOldMissingWorkspaceSession(t *testing.T) {
	workspaceStateRoot := filepath.Join(t.TempDir(), "missing-workspace-state")
	record := &connectionRecord{
		Fingerprint: "shared",
		Leases: map[string]time.Time{
			workspaceStateRoot: time.Now().Add(-connectionLeaseGrace - time.Minute),
		},
	}
	if !pruneConnectionLeases(record, workspaceStateRoot) {
		t.Fatal("old missing workspace lease was not pruned")
	}
	if len(record.Leases) != 0 {
		t.Fatalf("stale leases remain: %#v", record.Leases)
	}
}

func TestActiveConnectionRecordsRetiresStaleUnleasedProcess(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	t.Setenv("CONVEN_CONNECTION_HELPER", "1")
	t.Setenv("CONVEN_CONNECTION_HELPER_ADDRESS", address)
	directory := t.TempDir()
	process, err := EnsureConnection(context.Background(), ConnectionConfig{
		Driver:     "command",
		Command:    os.Args[0],
		Args:       []string{"-test.run=^TestConnectionHelperProcess$"},
		Timeout:    2 * time.Second,
		Readiness:  []ConnectionEndpoint{{Name: "helper", Address: address}},
	}, filepath.Join(directory, "connection.log"), filepath.Join(directory, "missing-workspace"), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stopConnection(process, true)
		_ = removeConnectionRecord(process.Fingerprint)
	}()
	record, err := loadConnectionRecord(process.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	for lease := range record.Leases {
		record.Leases[lease] = time.Now().Add(-connectionLeaseGrace - time.Minute)
	}
	if err := saveConnectionRecord(record); err != nil {
		t.Fatal(err)
	}
	active, err := activeConnectionRecords("new-workspace", "command")
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("stale connection remains active: %#v", active)
	}
	if ProcessGroupAlive(process.PGID) {
		t.Fatalf("stale unleased process group %d is still active", process.PGID)
	}
	record, err = loadConnectionRecord(process.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if record != nil {
		t.Fatalf("stale connection record remains: %#v", record)
	}
}

func TestWaitForElevatedTargetFindsManagedDescendant(t *testing.T) {
	command := exec.Command("sh", "-c", "sh -c 'exec sleep 600' & wait")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		_ = command.Wait()
	}()
	pid, err := waitForElevatedTarget(context.Background(), command.Process.Pid, "sleep", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if pid == command.Process.Pid || !ProcessAlive(pid) {
		t.Fatalf("elevated target pid = %d, root pid = %d", pid, command.Process.Pid)
	}
}

func TestCommandMatchesExecutablePathContainingSpaces(t *testing.T) {
	executable := "/tmp/conven tools/kt ctl"
	if !commandMatchesExecutable(executable+" --kubeconfig /tmp/dev connect", executable, filepath.Base(executable)) {
		t.Fatal("executable path containing spaces did not match ps command line")
	}
	if commandMatchesExecutable("/tmp/conven tools/kt ctl-other connect", executable, filepath.Base(executable)) {
		t.Fatal("executable prefix matched a different command")
	}
}

func TestParseProcessTreeLinePreservesRepeatedCommandWhitespace(t *testing.T) {
	entry, valid := parseProcessTreeLine("  123  45 /tmp/conven  tools/vpn-up --profile dev")
	if !valid {
		t.Fatal("valid process tree line was rejected")
	}
	if entry.PID != 123 || entry.Parent != 45 || entry.Command != "/tmp/conven  tools/vpn-up --profile dev" {
		t.Fatalf("process tree entry = %#v", entry)
	}
}

func TestConnectionHelperProcess(t *testing.T) {
	if os.Getenv("CONVEN_CONNECTION_HELPER") != "1" {
		return
	}
	listener, err := net.Listen("tcp", os.Getenv("CONVEN_CONNECTION_HELPER_ADDRESS"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		connection.Close()
	}
}
