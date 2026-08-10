package runtime

import (
	"context"
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

func TestConnectionLogPathLivesDirectlyUnderWorkspaceRuntime(t *testing.T) {
	runtimeDir := filepath.Join(t.TempDir(), ".loom", "runtime")
	want := filepath.Join(runtimeDir, "connection.log")
	if got := ConnectionLogPath(runtimeDir); got != want {
		t.Fatalf("connection log path = %q, want %q", got, want)
	}
}

func TestConnectionStateDirectoryUsesLoomStateHome(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("LOOM_STATE_HOME", stateHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "ignored"))
	directory, err := connectionStateDirectory()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(stateHome, ".connections")
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

func TestConnectionStateDirectoryUsesXDGStateHome(t *testing.T) {
	xdgStateHome := t.TempDir()
	t.Setenv("LOOM_STATE_HOME", "")
	t.Setenv("XDG_STATE_HOME", xdgStateHome)
	directory, err := connectionStateDirectory()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(xdgStateHome, "loom", ".connections")
	if directory != want {
		t.Fatalf("connection state directory = %q, want %q", directory, want)
	}
}

func TestConnectionStateDirectoryUsesDefaultUserStateHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LOOM_STATE_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	directory, err := connectionStateDirectory()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".local", "state", "loom", ".connections")
	if directory != want {
		t.Fatalf("connection state directory = %q, want %q", directory, want)
	}
}

func TestConnectionStateDirectoryRejectsRelativeUserStateHome(t *testing.T) {
	t.Setenv("LOOM_STATE_HOME", "relative-state")
	_, err := connectionStateDirectory()
	if err == nil || !strings.Contains(err.Error(), "LOOM_STATE_HOME must be an absolute path") {
		t.Fatalf("error = %v, want absolute path validation", err)
	}
}

func TestEnsureConnectionTimeoutStopsOwnedProcessGroup(t *testing.T) {
	t.Setenv("LOOM_STATE_HOME", t.TempDir())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	directory := t.TempDir()
	pidPath := filepath.Join(directory, "connection.pid")

	process, err := EnsureConnection(context.Background(), ConnectionConfig{
		Driver:  "command",
		Command: "sh",
		Args: []string{
			"-c",
			"echo $$ > \"$1\"; trap '' INT TERM; while :; do sleep 1; done",
			"sh",
			pidPath,
		},
		Timeout:   100 * time.Millisecond,
		Readiness: []ConnectionEndpoint{{Name: "closed", Address: address}},
	}, filepath.Join(directory, "connection.log"), "test-workspace", io.Discard)
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
  : > "$LOOM_SUDO_MARKER"
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
	t.Setenv("LOOM_SUDO_MARKER", marker)
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
	t.Setenv("LOOM_STATE_HOME", t.TempDir())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	t.Setenv("LOOM_CONNECTION_HELPER", "1")
	t.Setenv("LOOM_CONNECTION_HELPER_ADDRESS", address)
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
	t.Setenv("LOOM_STATE_HOME", t.TempDir())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	t.Setenv("LOOM_CONNECTION_HELPER", "1")
	t.Setenv("LOOM_CONNECTION_HELPER_ADDRESS", address)
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
	t.Setenv("LOOM_STATE_HOME", t.TempDir())
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
	if err == nil || !strings.Contains(err.Error(), "another ktctl connection is active in this Loom state root") {
		t.Fatalf("process=%#v error=%v, want shared connection conflict", process, err)
	}
}

func TestKtctlRegistryIgnoresManagedCommandConnection(t *testing.T) {
	t.Setenv("LOOM_STATE_HOME", t.TempDir())
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
		Command:    "loom-ktctl-that-does-not-exist",
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
	t.Setenv("LOOM_STATE_HOME", t.TempDir())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	t.Setenv("LOOM_CONNECTION_HELPER", "1")
	t.Setenv("LOOM_CONNECTION_HELPER_ADDRESS", address)
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
	executable := "/tmp/loom tools/kt ctl"
	if !commandMatchesExecutable(executable+" --kubeconfig /tmp/dev connect", executable, filepath.Base(executable)) {
		t.Fatal("executable path containing spaces did not match ps command line")
	}
	if commandMatchesExecutable("/tmp/loom tools/kt ctl-other connect", executable, filepath.Base(executable)) {
		t.Fatal("executable prefix matched a different command")
	}
}

func TestParseProcessTreeLinePreservesRepeatedCommandWhitespace(t *testing.T) {
	entry, valid := parseProcessTreeLine("  123  45 /tmp/loom  tools/vpn-up --profile dev")
	if !valid {
		t.Fatal("valid process tree line was rejected")
	}
	if entry.PID != 123 || entry.Parent != 45 || entry.Command != "/tmp/loom  tools/vpn-up --profile dev" {
		t.Fatalf("process tree entry = %#v", entry)
	}
}

func TestConnectionHelperProcess(t *testing.T) {
	if os.Getenv("LOOM_CONNECTION_HELPER") != "1" {
		return
	}
	listener, err := net.Listen("tcp", os.Getenv("LOOM_CONNECTION_HELPER_ADDRESS"))
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
