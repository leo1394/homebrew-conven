package runtime

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestWebSnapshotExposesOnlySafeSessionDataAndAttemptRoutes(t *testing.T) {
	workspace := newWebTestWorkspace(t)
	process := currentWebTestProcess(t, workspace, "api")
	process.Command = []string{"run", "--password=hunter2"}
	process.Verification = "listener-verified"
	process.Ports = map[string]int{"http": 8080}
	process.Listeners = map[string]ListenerEvidence{"http": {Address: "127.0.0.1", Port: 8080, Mode: "loopback", OwnerPID: process.PID, VerifiedAt: time.Now().UTC()}}
	session := &Session{AttemptID: "attempt-1", Workspace: workspace.Root, ConfigPath: workspace.ConfigPath, Environment: "dev", Services: []ServiceProcess{process}, Connection: &ConnectionProcess{Driver: "ktctl", PID: process.PID, PGID: process.PGID, Command: []string{"connect", "--token=connection-secret"}, Identity: process.Identity, Owned: true, Managed: true}}
	if err := workspace.Store.Save(session); err != nil {
		t.Fatal(err)
	}
	finished := time.Now().UTC()
	history := diagnosticHistory{Version: diagnosticsVersion, Attempts: []DiagnosticAttempt{{ID: "attempt-1", Environment: "dev", Services: []string{"api"}, StartedAt: finished.Add(-time.Second), FinishedAt: &finished, Status: "succeeded", Stages: []DiagnosticStage{}, Routes: []DiagnosticRoute{{Service: "api", Dependency: "users", Binding: "http", Mode: "local", Target: "127.0.0.1:9000", Source: "local-selection"}}}, {ID: "older", Environment: "dev", Services: []string{"api"}, StartedAt: finished.Add(-time.Hour), Status: "failed", Stages: []DiagnosticStage{}, Routes: []DiagnosticRoute{{Service: "wrong", Dependency: "route", Mode: "remote"}}}}}
	if err := writeDiagnosticHistory(workspace.Store, history); err != nil {
		t.Fatal(err)
	}
	snapshot, err := WebSnapshot(context.Background(), workspace, "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	wantToken, err := replacementSessionToken(session)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SessionToken != wantToken {
		t.Fatalf("session token = %q, want exact replacement token %q", snapshot.SessionToken, wantToken)
	}
	if len(snapshot.Services) != 1 || snapshot.Services[0].State != "running" || len(snapshot.Services[0].Listeners) != 1 {
		t.Fatalf("unexpected services: %#v", snapshot.Services)
	}
	if len(snapshot.Routes) != 1 || snapshot.Routes[0].Consumer != "api" || snapshot.Routes[0].Provider != "users" || snapshot.Routes[0].Source != "local-selection" {
		t.Fatalf("routes did not come from matching attempt: %#v", snapshot.Routes)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, forbidden := range []string{"hunter2", "connection-secret", `\"command\"`, `\"identity\"`, `\"logPath\"`, `\"fingerprint\"`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("snapshot exposed %q: %s", forbidden, text)
		}
	}
}

func TestWebSnapshotIncludesWorkspaceNetworkAndDisabledBindingsWithoutSession(t *testing.T) {
	workspace := newWebTestWorkspace(t)
	workspace.Manifest.Workspace.DisabledBindings = []string{"smartviewMgrRpc", "chatclbtRpc"}
	snapshot, err := WebSnapshot(context.Background(), workspace, "test")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LANAddress == "" {
		t.Fatal("LAN address must report an address or unavailable, as in the TUI")
	}
	if strings.Join(snapshot.DisabledBindings, ",") != "chatclbtRpc,smartviewMgrRpc" {
		t.Fatalf("disabled bindings = %v", snapshot.DisabledBindings)
	}
	if workspace.Manifest.Workspace.DisabledBindings[0] != "smartviewMgrRpc" {
		t.Fatal("snapshot sorted the manifest in place")
	}
}

func TestWebSnapshotDoesNotFabricateRoutesWithoutMatchingAttempt(t *testing.T) {
	workspace := newWebTestWorkspace(t)
	process := currentWebTestProcess(t, workspace, "api")
	if err := workspace.Store.Save(&Session{AttemptID: "missing", Workspace: workspace.Root, Environment: "dev", Services: []ServiceProcess{process}}); err != nil {
		t.Fatal(err)
	}
	if err := writeDiagnosticHistory(workspace.Store, diagnosticHistory{Version: diagnosticsVersion, Attempts: []DiagnosticAttempt{{ID: "other", Status: "succeeded", Stages: []DiagnosticStage{}, Routes: []DiagnosticRoute{{Service: "api", Dependency: "users", Mode: "local"}}}}}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := WebSnapshot(context.Background(), workspace, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Routes) != 0 {
		t.Fatalf("snapshot fabricated live routes: %#v", snapshot.Routes)
	}
}

func TestWebLogsRedactsSecretsScrubsControlsAndAdvancesCursor(t *testing.T) {
	workspace := newWebTestWorkspace(t)
	logPath := filepath.Join(workspace.Store.CurrentDir, "logs", "api.log")
	first := "{\"timestamp\":\"2026-01-02T03:04:05Z\",\"level\":\"INFO\",\"requestId\":\"req-1\",\"traceId\":\"trace-1\",\"message\":\"Authorization: Bearer abc.def password=hunter2 cookie=session-cookie https://user:pass@example.test/?token=url-secret \\u001b[31mred\\u001b[0m\"}\npassword:\nmultiline-secret\n"
	if err := os.WriteFile(logPath, []byte(first), 0600); err != nil {
		t.Fatal(err)
	}
	process := currentWebTestProcess(t, workspace, "api")
	process.LogPath = logPath
	if err := workspace.Store.Save(&Session{Workspace: workspace.Root, Environment: "dev", Services: []ServiceProcess{process}}); err != nil {
		t.Fatal(err)
	}
	page, err := WebLogs(workspace, WebLogQuery{Services: []string{"api"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 3 || page.Cursor == "" {
		t.Fatalf("unexpected first page: %#v", page)
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	visible := string(encoded)
	for _, secret := range []string{"abc.def", "hunter2", "session-cookie", "user:pass", "url-secret", "multiline-secret", "\\u001b"} {
		if strings.Contains(visible, secret) {
			t.Fatalf("Web log page exposed %q: %s", secret, visible)
		}
	}
	if page.Entries[0].Time == nil || page.Entries[0].Level != "INFO" || page.Entries[0].RequestID != "req-1" || page.Entries[0].TraceID != "trace-1" {
		t.Fatalf("structured metadata was not extracted: %#v", page.Entries[0])
	}
	file, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("2026-01-02T03:04:06Z ERROR requestId=req-2 next line\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	next, err := WebLogs(workspace, WebLogQuery{Services: []string{"api"}, Cursor: page.Cursor})
	if err != nil {
		t.Fatal(err)
	}
	if next.Reset || len(next.Entries) != 1 || next.Entries[0].RequestID != "req-2" {
		t.Fatalf("cursor did not advance to appended entry: %#v", next)
	}
}

func TestWebLogsRejectsOutsideAndSymlinkSources(t *testing.T) {
	workspace := newWebTestWorkspace(t)
	outside := filepath.Join(t.TempDir(), "outside.log")
	if err := os.WriteFile(outside, []byte("secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	process := currentWebTestProcess(t, workspace, "api")
	process.LogPath = outside
	if err := workspace.Store.Save(&Session{Workspace: workspace.Root, Services: []ServiceProcess{process}}); err != nil {
		t.Fatal(err)
	}
	if _, err := WebLogs(workspace, WebLogQuery{Services: []string{"api"}}); err == nil {
		t.Fatal("outside log source was accepted")
	}
	link := filepath.Join(workspace.Store.CurrentDir, "logs", "api.log")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	process.LogPath = link
	if err := workspace.Store.Save(&Session{Workspace: workspace.Root, Services: []ServiceProcess{process}}); err != nil {
		t.Fatal(err)
	}
	if _, err := WebLogs(workspace, WebLogQuery{Services: []string{"api"}}); err == nil {
		t.Fatal("symbolic-link log source was accepted")
	}
}

func TestWebLogsDetectsRotationAndResetsCursor(t *testing.T) {
	workspace := newWebTestWorkspace(t)
	logPath := filepath.Join(workspace.Store.CurrentDir, "logs", "api.log")
	if err := os.WriteFile(logPath, []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	process := currentWebTestProcess(t, workspace, "api")
	process.LogPath = logPath
	if err := workspace.Store.Save(&Session{Workspace: workspace.Root, Services: []ServiceProcess{process}}); err != nil {
		t.Fatal(err)
	}
	first, err := WebLogs(workspace, WebLogQuery{Services: []string{"api"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(logPath, logPath+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("after\n"), 0600); err != nil {
		t.Fatal(err)
	}
	next, err := WebLogs(workspace, WebLogQuery{Services: []string{"api"}, Cursor: first.Cursor})
	if err != nil {
		t.Fatal(err)
	}
	if !next.Reset || len(next.Entries) != 1 || next.Entries[0].Text != "after" {
		t.Fatalf("rotation was not reset to the new generation: %#v", next)
	}
}

func TestWebLogsWaitsForCompleteJSONLine(t *testing.T) {
	workspace := newWebTestWorkspace(t)
	logPath := filepath.Join(workspace.Store.CurrentDir, "logs", "api.log")
	if err := os.WriteFile(logPath, []byte(`{"@timestamp":"2026-01-02T03:04:05Z","content":"password=`), 0600); err != nil {
		t.Fatal(err)
	}
	process := currentWebTestProcess(t, workspace, "api")
	process.LogPath = logPath
	if err := workspace.Store.Save(&Session{AttemptID: "attempt-current", Workspace: workspace.Root, Services: []ServiceProcess{process}}); err != nil {
		t.Fatal(err)
	}
	partial, err := WebLogs(workspace, WebLogQuery{Services: []string{"api"}, Attempt: "attempt-current"})
	if err != nil {
		t.Fatal(err)
	}
	if len(partial.Entries) != 0 {
		t.Fatalf("incomplete JSON line was emitted: %#v", partial.Entries)
	}
	file, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`split-secret trace=trace-go-zero requestId=req-content"}` + "\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	complete, err := WebLogs(workspace, WebLogQuery{Services: []string{"api"}, Attempt: "attempt-current", Cursor: partial.Cursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(complete.Entries) != 1 || strings.Contains(complete.Entries[0].Text, "split-secret") || complete.Entries[0].TraceID != "trace-go-zero" || complete.Entries[0].RequestID != "req-content" || complete.Entries[0].Time == nil {
		t.Fatalf("completed go-zero JSON line was not safely parsed: %#v", complete.Entries)
	}
}

func TestWebLogsDiscardsOversizedSplitLineWithoutLeakingSecret(t *testing.T) {
	workspace := newWebTestWorkspace(t)
	logPath := filepath.Join(workspace.Store.CurrentDir, "logs", "api.log")
	if err := os.WriteFile(logPath, []byte("start\n"), 0600); err != nil {
		t.Fatal(err)
	}
	process := currentWebTestProcess(t, workspace, "api")
	process.LogPath = logPath
	if err := workspace.Store.Save(&Session{Workspace: workspace.Root, Services: []ServiceProcess{process}}); err != nil {
		t.Fatal(err)
	}
	first, err := WebLogs(workspace, WebLogQuery{Services: []string{"api"}})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	oversized := strings.Repeat("x", webLogScanBytes-len("password=")) + "password="
	if _, err := file.WriteString(oversized); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	middle, err := WebLogs(workspace, WebLogQuery{Services: []string{"api"}, Cursor: first.Cursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(middle.Entries) != 0 {
		t.Fatalf("oversized partial line was emitted: %#v", middle.Entries)
	}
	file, err = os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("boundary-secret\nsafe\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	last, err := WebLogs(workspace, WebLogQuery{Services: []string{"api"}, Cursor: middle.Cursor})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(last)
	if strings.Contains(string(data), "boundary-secret") || len(last.Entries) != 1 || last.Entries[0].Text != "safe" {
		t.Fatalf("split oversized secret was exposed or safe line was lost: %s", data)
	}
}

func TestWebLogsAllowsOnlyFixedConnectionLogPath(t *testing.T) {
	workspace := newWebTestWorkspace(t)
	connectionPath := ConnectionLogPath(workspace.Store.Root)
	if err := os.WriteFile(connectionPath, []byte("connected\n"), 0600); err != nil {
		t.Fatal(err)
	}
	session := &Session{Workspace: workspace.Root, Services: []ServiceProcess{}, Connection: &ConnectionProcess{Driver: "ktctl", LogPath: connectionPath}}
	if err := workspace.Store.Save(session); err != nil {
		t.Fatal(err)
	}
	page, err := WebLogs(workspace, WebLogQuery{Services: []string{"connection/ktctl"}})
	if err != nil || len(page.Entries) != 1 {
		t.Fatalf("fixed connection log was not readable: page=%#v err=%v", page, err)
	}
	session.Connection.LogPath = filepath.Join(workspace.Store.Root, "other.log")
	if err := os.WriteFile(session.Connection.LogPath, []byte("wrong\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Store.Save(session); err != nil {
		t.Fatal(err)
	}
	if _, err := WebLogs(workspace, WebLogQuery{Services: []string{"connection/ktctl"}}); err == nil {
		t.Fatal("non-fixed connection log path was accepted")
	}
}

func TestWebLogsServesHistoricalFailureTailByConvenAttempt(t *testing.T) {
	workspace := newWebTestWorkspace(t)
	attempt := DiagnosticAttempt{ID: "attempt-old", Services: []string{"api"}, Status: "failed", Stages: []DiagnosticStage{}, Routes: []DiagnosticRoute{}, Failure: &DiagnosticFailure{Stage: "run", Service: "api", Error: "failed", LogTail: "Authorization: Bearer historical-secret\nhistorical failure\n"}}
	if err := writeDiagnosticHistory(workspace.Store, diagnosticHistory{Version: diagnosticsVersion, Attempts: []DiagnosticAttempt{attempt}}); err != nil {
		t.Fatal(err)
	}
	page, err := WebLogs(workspace, WebLogQuery{Services: []string{"api"}, Attempt: "attempt-old"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(page)
	if len(page.Entries) != 2 || strings.Contains(string(data), "historical-secret") {
		t.Fatalf("historical failure tail was not served safely: %s", data)
	}
}

func TestWebLogsCurrentAttemptStartsAtPersistedLogOffset(t *testing.T) {
	workspace := newWebTestWorkspace(t)
	logPath := filepath.Join(workspace.Store.CurrentDir, "logs", "api.log")
	previous := "previous restart output\n"
	current := "current attempt output\n"
	if err := os.WriteFile(logPath, []byte(previous+current), 0600); err != nil {
		t.Fatal(err)
	}
	process := currentWebTestProcess(t, workspace, "api")
	process.LogPath = logPath
	process.LogOffset = int64(len(previous))
	if err := workspace.Store.Save(&Session{AttemptID: "attempt-current", Workspace: workspace.Root, Services: []ServiceProcess{process}}); err != nil {
		t.Fatal(err)
	}
	page, err := WebLogs(workspace, WebLogQuery{Services: []string{"api"}, Attempt: "attempt-current"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Text != "current attempt output" {
		t.Fatalf("current attempt crossed its persisted log offset: %#v", page.Entries)
	}
}

func TestReadWebLogSourceRetainsNormalLineAcrossScanBoundary(t *testing.T) {
	workspace := newWebTestWorkspace(t)
	logPath := filepath.Join(workspace.Store.CurrentDir, "logs", "api.log")
	line := strings.Repeat("ordinary-", 25)
	if err := os.WriteFile(logPath, []byte(line+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source := webLogSource{name: "api", path: logPath, root: filepath.Join(workspace.Store.CurrentDir, "logs")}
	entries, position, more, _, _, err := readWebLogSource(workspace, source, webLogPosition{}, false, 10, 64, WebLogQuery{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 || position.Offset != 0 || position.Discarding || !more {
		t.Fatalf("normal split record was consumed or marked oversized: entries=%#v position=%#v more=%v", entries, position, more)
	}
	entries, position, more, _, _, err = readWebLogSource(workspace, source, position, false, 10, 1024, WebLogQuery{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Text != line || position.Offset != int64(len(line)+1) || more {
		t.Fatalf("complete re-read did not emit the preserved record: entries=%#v position=%#v more=%v", entries, position, more)
	}
}

func TestWebLogsWithoutSessionDefaultsToNewestArchivedAttempt(t *testing.T) {
	workspace := newWebTestWorkspace(t)
	now := time.Now().UTC()
	history := diagnosticHistory{Version: diagnosticsVersion, Attempts: []DiagnosticAttempt{
		{ID: "newest", Services: []string{"api"}, Status: "succeeded", Stages: []DiagnosticStage{}, Routes: []DiagnosticRoute{}, Logs: []DiagnosticLog{{Service: "api", CapturedAt: now, Tail: "newest archived line\n"}}},
		{ID: "older", Services: []string{"api"}, Status: "failed", Stages: []DiagnosticStage{}, Routes: []DiagnosticRoute{}, Logs: []DiagnosticLog{{Service: "api", CapturedAt: now.Add(-time.Minute), Tail: "older archived line\n"}}},
	}}
	if err := writeDiagnosticHistory(workspace.Store, history); err != nil {
		t.Fatal(err)
	}
	page, err := WebLogs(workspace, WebLogQuery{Services: []string{"api"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Text != "newest archived line" {
		t.Fatalf("stopped session did not default to newest archive: %#v", page.Entries)
	}
}

func TestRefreshWebHealthForceBypassesBackoffAndPreservesEvidenceStates(t *testing.T) {
	workspace := newWebTestWorkspace(t)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	process := currentWebTestProcess(t, workspace, "api")
	session := &Session{Workspace: workspace.Root, Environment: "dev", Services: []ServiceProcess{process}, HealthChecks: []SessionHealthCheck{{Name: "api", Type: "tcp", Address: listener.Addr().String()}}}
	if err := workspace.Store.Save(session); err != nil {
		listener.Close()
		t.Fatal(err)
	}
	first, err := RefreshWebHealth(context.Background(), workspace)
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	if first["api"].Status != "healthy" {
		listener.Close()
		t.Fatalf("first health = %#v", first["api"])
	}
	listener.Close()
	second, err := RefreshWebHealth(context.Background(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	if second["api"].Status != "stale" {
		t.Fatalf("single failure should retain stale evidence: %#v", second["api"])
	}
	backedOff, err := RefreshWebHealth(context.Background(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	if backedOff["api"].Status != "stale" || backedOff["api"].CheckedAt.IsZero() || second["api"].CheckedAt.IsZero() || !backedOff["api"].CheckedAt.Equal(second["api"].CheckedAt) {
		t.Fatalf("automatic refresh ignored failure backoff: before=%#v after=%#v", second["api"], backedOff["api"])
	}
	forced, err := RefreshWebHealth(context.Background(), workspace, true)
	if err != nil {
		t.Fatal(err)
	}
	if forced["api"].Status != "unhealthy" {
		t.Fatalf("forced refresh did not bypass backoff: %#v", forced["api"])
	}
	session.HealthChecks = []SessionHealthCheck{{Name: "api", Type: "command", Address: listener.Addr().String()}}
	if err := workspace.Store.Save(session); err != nil {
		t.Fatal(err)
	}
	unknown, err := RefreshWebHealth(context.Background(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	if unknown["api"].Status != "unknown" {
		t.Fatalf("command-only health must stay unknown: %#v", unknown["api"])
	}
}

func TestOpenWebWorkspacePreservesDiagnosticsForMalformedManifest(t *testing.T) {
	root := t.TempDir()
	boundary := filepath.Join(root, ".conven")
	if err := os.Mkdir(boundary, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(boundary, "conven.yaml"), []byte("version: [broken\npassword: hunter2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	workspace, err := OpenWebWorkspace(CommonOptions{Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := WebSnapshot(context.Background(), workspace, "test")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ConfigurationError == "" {
		t.Fatal("malformed manifest did not produce a configuration error")
	}
	if strings.Contains(snapshot.ConfigurationError, "hunter2") {
		t.Fatalf("configuration error was not redacted: %q", snapshot.ConfigurationError)
	}
	if len(snapshot.Services) != 0 || snapshot.SessionToken != "" {
		t.Fatalf("malformed manifest fabricated a session: %#v", snapshot)
	}
}

func newWebTestWorkspace(t *testing.T) *WorkspaceData {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".conven"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ResetCurrent(); err != nil {
		t.Fatal(err)
	}
	return &WorkspaceData{Root: root, ConfigPath: filepath.Join(root, ".conven", "conven.yaml"), Manifest: &model.Manifest{Workspace: model.Workspace{Name: "web-test"}, Services: map[string]model.Service{"api": {}}, Environments: map[string]model.Environment{}, Policies: map[string]model.Policy{}}, Settings: map[string]string{}, Store: store}
}

func currentWebTestProcess(t *testing.T, workspace *WorkspaceData, name string) ServiceProcess {
	t.Helper()
	identity, err := processIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	pgid, err := syscall.Getpgid(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	return ServiceProcess{Name: name, PID: os.Getpid(), PGID: pgid, Command: []string{"test-process"}, Identity: identity, LogPath: filepath.Join(workspace.Store.CurrentDir, "logs", name+".log"), StartedAt: time.Now().UTC(), Ports: map[string]int{}, SourceFingerprint: "source", PlanFingerprint: "plan", Verification: "verified"}
}
