package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/dependency"
	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestStartDiagnosticsRecordsFirstPlanFailure(t *testing.T) {
	workspace := testWorkspace(t, t.TempDir(), &model.Manifest{Version: 1})
	_, err := Start(context.Background(), workspace, StartOptions{})
	if err == nil || !strings.Contains(err.Error(), "at least one local service") {
		t.Fatalf("start error = %v", err)
	}
	attempts, err := ReadDiagnostics(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 {
		t.Fatalf("diagnostic attempts = %d, want 1", len(attempts))
	}
	attempt := attempts[0]
	if attempt.ID == "" || attempt.Status != "failed" || attempt.FinishedAt == nil || attempt.Failure == nil {
		t.Fatalf("failed attempt = %#v", attempt)
	}
	if attempt.Failure.Stage != "plan" || !strings.Contains(attempt.Failure.Error, "at least one local service") {
		t.Fatalf("failure = %#v", attempt.Failure)
	}
}

func TestStartDiagnosticsRecordsSuccessfulAttemptAndEffectivePlan(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "api"), 0700); err != nil {
		t.Fatal(err)
	}
	workspace := testWorkspace(t, root, &model.Manifest{
		Version: 1,
		Services: map[string]model.Service{
			"api": {Path: "api", Runner: model.Runner{Run: []string{"sleep", "600"}}},
		},
	})
	session, err := Start(context.Background(), workspace, StartOptions{
		Common: CommonOptions{Environment: "dev"},
		Services: []string{"api"},
		SkipVerify: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer Stop(context.Background(), workspace, nil, true, false, nil)
	attempts, err := ReadDiagnostics(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0].Status != "succeeded" || attempts[0].FinishedAt == nil {
		t.Fatalf("attempts = %#v", attempts)
	}
	if session.AttemptID != attempts[0].ID || attempts[0].Environment != "dev" || len(attempts[0].Services) != 1 || attempts[0].Services[0] != "api" {
		t.Fatalf("session/attempt mismatch: session=%#v attempt=%#v", session, attempts[0])
	}
	if err := Stop(context.Background(), workspace, nil, true, false, nil); err != nil {
		t.Fatal(err)
	}
	attempts, err = ReadDiagnostics(workspace)
	if err != nil {
		t.Fatal(err)
	}
	lastStage := attempts[0].Stages[len(attempts[0].Stages)-1]
	if attempts[0].Status != "succeeded" || lastStage.Name != "stop" || lastStage.Service != "api" || lastStage.Status != "completed" || lastStage.FinishedAt == nil {
		t.Fatalf("stop diagnostics = attempt %#v stage %#v", attempts[0], lastStage)
	}
}

func TestDiagnosticsSurviveCurrentResetAndAreBoundedAndPrivate(t *testing.T) {
	workspace := testWorkspace(t, t.TempDir(), &model.Manifest{Version: 1})
	for index := 0; index < diagnosticsLimit+3; index++ {
		recorder, err := beginDiagnostics(workspace, "dev", []string{"api"})
		if err != nil {
			t.Fatal(err)
		}
		if err := recorder.fail("plan", "", errors.New("invalid token=secret-value"), "Authorization: Bearer secret-value"); err != nil {
			t.Fatal(err)
		}
	}
	if err := workspace.Store.ResetCurrent(); err != nil {
		t.Fatal(err)
	}
	attempts, err := ReadDiagnostics(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != diagnosticsLimit {
		t.Fatalf("diagnostic attempts = %d, want %d", len(attempts), diagnosticsLimit)
	}
	if strings.Contains(attempts[0].Failure.Error, "secret-value") || strings.Contains(attempts[0].Failure.LogTail, "secret-value") {
		t.Fatalf("diagnostics exposed a secret: %#v", attempts[0].Failure)
	}
	directory := filepath.Join(workspace.Store.Root, "diagnostics")
	directoryInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(filepath.Join(directory, "attempts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if directoryInfo.Mode().Perm() != 0700 || fileInfo.Mode().Perm() != 0600 {
		t.Fatalf("diagnostic permissions = directory %o file %o", directoryInfo.Mode().Perm(), fileInfo.Mode().Perm())
	}
}

func TestDiagnosticFailurePersistsActualCleanupEvidence(t *testing.T) {
	workspace := testWorkspace(t, t.TempDir(), &model.Manifest{Version: 1})
	recorder, err := beginDiagnostics(workspace, "dev", []string{"api", "worker"})
	if err != nil {
		t.Fatal(err)
	}
	cleanup := diagnosticCleanupEvidence{
		started: []string{"api"},
		pending: []string{"worker"},
		rolledBack: []string{"api"},
		status: "completed",
	}
	if err := recorder.failWithCleanup("verify", "api", errors.New("failed"), "", cleanup); err != nil {
		t.Fatal(err)
	}
	attempts, err := ReadDiagnostics(workspace)
	if err != nil {
		t.Fatal(err)
	}
	failure := attempts[0].Failure
	if failure == nil || failure.CleanupStatus != "completed" || !reflect.DeepEqual(failure.StartedServices, []string{"api"}) || !reflect.DeepEqual(failure.PendingServices, []string{"worker"}) || !reflect.DeepEqual(failure.RolledBackServices, []string{"api"}) || len(failure.RemainingServices) != 0 {
		t.Fatalf("cleanup evidence = %#v", failure)
	}
}

func TestDiagnosticsRejectSymlinkTraversal(t *testing.T) {
	workspace := testWorkspace(t, t.TempDir(), &model.Manifest{Version: 1})
	if err := workspace.Store.ensureRoot(); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(workspace.Store.Root, "diagnostics")); err != nil {
		t.Fatal(err)
	}
	if _, err := beginDiagnostics(workspace, "dev", []string{"api"}); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("symlink diagnostics error = %v", err)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("diagnostics traversed symlink: %#v", entries)
	}
}

func TestRedactDiagnosticTextCoversStructuredAndHeaderCredentials(t *testing.T) {
	input := "{\"password\":\"alpha beta\",\"token\":\"token-secret\"}\nAuthorization: Bearer bearer-secret\nCookie: a=one;b=two\nhttps://user:pass@example.test"
	redacted := RedactDiagnosticText(input)
	for _, secret := range []string{"alpha", "beta", "token-secret", "bearer-secret", "a=one", "b=two", "user:pass"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("redacted diagnostic contains %q: %q", secret, redacted)
		}
	}
	long := strings.Repeat("x", diagnosticTextLimit+100) + " trace=end"
	if redacted := RedactDiagnosticText(long); !strings.HasSuffix(redacted, "trace=end") {
		t.Fatalf("public redactor truncated non-secret metadata: length %d", len(redacted))
	}
}

func TestRedactDiagnosticTextRecursivelyReplacesSensitiveJSONValues(t *testing.T) {
	input := "{\n  \"token\": [\"secret-value\", {\"nested\": true}],\n  \"profile\": {\"password\": {\"value\": \"secret-value\"}, \"name\": \"visible\"},\n  \"items\": [{\"credential\": [1, 2, 3]}],\n  \"message\": \"Authorization: Bearer deep-secret\"\n}"
	redacted := RedactDiagnosticText(input)
	if strings.Contains(redacted, "secret-value") || strings.Contains(redacted, "deep-secret") || strings.Contains(redacted, `"value"`) || strings.Contains(redacted, "[1,2,3]") {
		t.Fatalf("recursive JSON redaction leaked a sensitive composite: %s", redacted)
	}
	if !strings.Contains(redacted, `"token":"[REDACTED]"`) || !strings.Contains(redacted, `"password":"[REDACTED]"`) || !strings.Contains(redacted, `"credential":"[REDACTED]"`) || !strings.Contains(redacted, `"name":"visible"`) {
		t.Fatalf("recursive JSON redaction lost structure or safe metadata: %s", redacted)
	}
	encoded := RedactDiagnosticText(`{"content":"{\"token\":[\"encoded-secret\"]}","level":"info"}`)
	if strings.Contains(encoded, "encoded-secret") || !strings.Contains(encoded, `"level":"info"`) {
		t.Fatalf("encoded JSON content was not safely redacted: %s", encoded)
	}
	deep := `{"token":["deep-secret"]}`
	for index := 0; index < diagnosticJSONDepthLimit+3; index++ {
		data, err := json.Marshal(map[string]string{"content": deep})
		if err != nil {
			t.Fatal(err)
		}
		deep = string(data)
	}
	if redacted := RedactDiagnosticText(deep); strings.Contains(redacted, "deep-secret") {
		t.Fatalf("depth-limited JSON redaction leaked nested content: %s", redacted)
	}
}

func TestDiagnosticLogPersistenceRedactsJSONLCompositeSecrets(t *testing.T) {
	workspace := testWorkspace(t, t.TempDir(), &model.Manifest{Version: 1})
	if err := workspace.Store.ResetCurrent(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace.Store.CurrentDir, "logs", "api.log")
	data := "{\"token\":[\"secret-value\"]}\n{\"level\":\"info\",\"msg\":\"next\"}\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	recorder, err := beginDiagnostics(workspace, "dev", []string{"api"})
	if err != nil {
		t.Fatal(err)
	}
	session := &Session{AttemptID: recorder.attempt.ID, Services: []ServiceProcess{{Name: "api", LogPath: path}}}
	if err := recorder.captureLogs(session, nil); err != nil {
		t.Fatal(err)
	}
	if err := recorder.succeed(); err != nil {
		t.Fatal(err)
	}
	attempts, err := ReadDiagnostics(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || len(attempts[0].Logs) != 1 {
		t.Fatalf("persisted JSONL logs = %#v", attempts)
	}
	tail := attempts[0].Logs[0].Tail
	if strings.Contains(tail, "secret-value") || !strings.Contains(tail, `"token":"[REDACTED]"`) || !strings.Contains(tail, `"msg":"next"`) {
		t.Fatalf("persisted JSONL tail was not safely redacted: %s", tail)
	}
}

func TestStopWithSessionTokenRejectsStaleSessionInsideLock(t *testing.T) {
	workspace := testWorkspace(t, t.TempDir(), &model.Manifest{Version: 1})
	session := &Session{Workspace: workspace.Root, Environment: "dev", Selected: []string{"api"}}
	if err := workspace.Store.Save(session); err != nil {
		t.Fatal(err)
	}
	token, err := WebSessionToken(session)
	if err != nil {
		t.Fatal(err)
	}
	session.Selected = []string{"worker"}
	if err := workspace.Store.Save(session); err != nil {
		t.Fatal(err)
	}
	if err := StopWithSessionToken(context.Background(), workspace, nil, token, nil); err == nil || !strings.Contains(err.Error(), "session changed") {
		t.Fatalf("stale stop error = %v", err)
	}
	preserved, err := workspace.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if preserved == nil || len(preserved.Selected) != 1 || preserved.Selected[0] != "worker" {
		t.Fatalf("stale stop changed session: %#v", preserved)
	}
}

func TestStopContinuesCleanupWhenDiagnosticsArchiveIsUnavailable(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "api"), 0700); err != nil {
		t.Fatal(err)
	}
	workspace := testWorkspace(t, root, &model.Manifest{
		Version: 1,
		Services: map[string]model.Service{
			"api": {Path: "api", Runner: model.Runner{Run: []string{"sleep", "600"}}},
		},
	})
	session, err := Start(context.Background(), workspace, StartOptions{Services: []string{"api"}, SkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	diagnosticsPath := filepath.Join(workspace.Store.Root, "diagnostics")
	if err := os.Rename(diagnosticsPath, diagnosticsPath+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(diagnosticsPath, []byte("not a directory\n"), 0600); err != nil {
		t.Fatal(err)
	}
	err = Stop(context.Background(), workspace, nil, true, false, nil)
	if err == nil || !strings.Contains(err.Error(), "archive startup logs") {
		t.Fatalf("stop diagnostics error = %v", err)
	}
	for _, process := range session.Services {
		if ProcessAlive(process.PID) || ProcessGroupAlive(process.PGID) {
			t.Fatalf("diagnostics failure left process active: %#v", process)
		}
	}
	stored, loadErr := workspace.Store.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if stored != nil {
		t.Fatalf("diagnostics failure left session state: %#v", stored)
	}
}

func TestSessionHealthChecksExcludeCommandsAndURLCredentials(t *testing.T) {
	plan := &Plan{
		Order: []string{"api"},
		Services: map[string]PlannedService{
			"api": {HealthChecks: []HealthCheck{
				{Server: "http", Type: "http", URL: "http://user:pass@127.0.0.1:8080/ready?token=secret&probe=full", Command: []string{"echo", "secret"}, Environment: []string{"TOKEN=secret"}},
				{Server: "safe", Type: "http", URL: "http://127.0.0.1:8080/ready?probe=full"},
				{Type: "command", Command: []string{"echo", "secret"}},
			}},
		},
	}
	checks := sessionHealthChecks(plan)
	if len(checks) != 1 {
		t.Fatalf("health checks = %#v", checks)
	}
	if checks[0].Server != "safe" || checks[0].URL != "http://127.0.0.1:8080/ready?probe=full" {
		t.Fatalf("safe health URL = %q", checks[0].URL)
	}
	if strings.Contains(checks[0].URL, "secret") || strings.Contains(checks[0].URL, "pass") {
		t.Fatalf("health snapshot exposed credentials: %#v", checks[0])
	}
}

func TestRestartEvidenceMergePreservesUntouchedService(t *testing.T) {
	existingHealth := []SessionHealthCheck{{Name: "api", Type: "http", URL: "http://old-api"}, {Name: "worker", Type: "tcp", Address: "old-worker"}}
	plannedHealth := []SessionHealthCheck{{Name: "api", Type: "http", URL: "http://new-api"}, {Name: "worker", Type: "tcp", Address: "new-worker"}}
	mergedHealth := mergeSessionHealthChecks(existingHealth, plannedHealth, []string{"api"})
	if len(mergedHealth) != 2 || mergedHealth[0].Name != "worker" || mergedHealth[0].Address != "old-worker" || mergedHealth[1].URL != "http://new-api" {
		t.Fatalf("merged health = %#v", mergedHealth)
	}
	existingRoutes := []DiagnosticRoute{{Service: "api", Dependency: "db", Target: "old-api-db"}, {Service: "worker", Dependency: "queue", Target: "old-worker-queue"}}
	plannedRoutes := []DiagnosticRoute{{Service: "api", Dependency: "db", Target: "new-api-db"}, {Service: "worker", Dependency: "queue", Target: "new-worker-queue"}}
	mergedRoutes := mergeSessionRuntimeRoutes(existingRoutes, plannedRoutes, []string{"api"})
	if len(mergedRoutes) != 2 || mergedRoutes[0].Service != "worker" || mergedRoutes[0].Target != "old-worker-queue" || mergedRoutes[1].Target != "new-api-db" {
		t.Fatalf("merged routes = %#v", mergedRoutes)
	}
}

func TestDiagnosticLogsUseAttemptOffsetAndRedact(t *testing.T) {
	workspace := testWorkspace(t, t.TempDir(), &model.Manifest{Version: 1})
	if err := workspace.Store.ResetCurrent(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace.Store.CurrentDir, "logs", "api.log")
	previous := "old launch\n"
	current := "ready password=alpha beta\n"
	if err := os.WriteFile(path, []byte(previous+current), 0600); err != nil {
		t.Fatal(err)
	}
	session := &Session{Services: []ServiceProcess{{Name: "api", LogPath: path, LogOffset: int64(len(previous))}}}
	logs := diagnosticSessionLogs(workspace.Store, session, nil, nil)
	if len(logs) != 1 || strings.Contains(logs[0].Tail, "old launch") || strings.Contains(logs[0].Tail, "alpha beta") {
		t.Fatalf("diagnostic logs = %#v", logs)
	}
	session.Services[0].LogOffset = int64(len(previous + current))
	if logs := diagnosticSessionLogs(workspace.Store, session, nil, nil); len(logs) != 0 {
		t.Fatalf("empty current attempt returned old logs: %#v", logs)
	}
}

func TestDiagnosticRouteValueRedactsCredentialBearingTargets(t *testing.T) {
	for _, target := range []string{"https://user:pass@example.test", "remote?token=secret", "credential=alpha beta"} {
		if got := diagnosticRouteValue(target); got != "[REDACTED]" {
			t.Fatalf("route target %q persisted as %q", target, got)
		}
	}
	if got := diagnosticRouteValue("payments"); got != "payments" {
		t.Fatalf("safe route target = %q", got)
	}
}

func TestDiagnosticRoutesSnapshotResolvedAddressAndDeclaredDisabledBinding(t *testing.T) {
	plan := &Plan{
		Workspace: &WorkspaceData{Manifest: &model.Manifest{
			Workspace: model.Workspace{DisabledBindings: []string{"legacyRpc"}},
			Services: map[string]model.Service{
				"api": {Dependencies: map[string]model.Dependency{"legacy": {Binding: "legacyRpc"}}},
			},
		}},
		Order: []string{"api"},
		Services: map[string]PlannedService{
			"api": {Config: &PlannedConfig{Routes: []PlannedRoute{{Dependency: "db", Binding: "dbRpc"}}}},
		},
		Resolutions: map[string]map[string]dependency.Resolution{
			"api": {"db": {Mode: "endpoint", Target: "database", Address: "127.0.0.1:5432"}},
		},
	}
	routes := diagnosticRoutes(plan)
	if len(routes) != 2 {
		t.Fatalf("routes = %#v", routes)
	}
	if routes[0].Target != "127.0.0.1:5432" || routes[0].Source != "endpoint" || routes[0].Binding != "dbRpc" {
		t.Fatalf("resolved route = %#v", routes[0])
	}
	if routes[1].Binding != "legacyRpc" || routes[1].Mode != "disabled" || routes[1].Source != "workspace.disabledBindings" {
		t.Fatalf("disabled route = %#v", routes[1])
	}
}

func TestRestartPreservesUntouchedAndNoOpRuntimeEvidence(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"api", "worker"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	manifest := &model.Manifest{
		Version: 1,
		Services: map[string]model.Service{
			"api": {Path: "api", Runner: model.Runner{Run: []string{"sleep", "600"}}, Health: model.Health{Type: "http", URL: "http://127.0.0.1:1/api-old"}},
			"worker": {Path: "worker", Runner: model.Runner{Run: []string{"sleep", "600"}}, Health: model.Health{Type: "http", URL: "http://127.0.0.1:1/worker-old"}},
		},
	}
	workspace := testWorkspace(t, root, manifest)
	session, err := Start(context.Background(), workspace, StartOptions{Services: []string{"api", "worker"}, SkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer Stop(context.Background(), workspace, nil, true, false, nil)
	beforeAttempt := session.AttemptID
	beforeHealth := append([]SessionHealthCheck(nil), session.HealthChecks...)
	beforeRoutes := append([]DiagnosticRoute(nil), session.RuntimeRoutes...)
	session, err = Restart(context.Background(), workspace, RestartOptions{SkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	if session.AttemptID != beforeAttempt || !reflect.DeepEqual(session.HealthChecks, beforeHealth) || !reflect.DeepEqual(session.RuntimeRoutes, beforeRoutes) {
		t.Fatalf("no-op restart changed runtime evidence: %#v", session)
	}
	session.HealthChecks = []SessionHealthCheck{{Name: "api", Type: "http", URL: "http://old-api"}, {Name: "worker", Type: "http", URL: "http://old-worker"}}
	session.RuntimeRoutes = []DiagnosticRoute{{Service: "api", Dependency: "db", Target: "old-api-db"}, {Service: "worker", Dependency: "queue", Target: "old-worker-queue"}}
	if err := workspace.Store.Save(session); err != nil {
		t.Fatal(err)
	}
	api := manifest.Services["api"]
	api.Health.URL = "http://127.0.0.1:1/api-new"
	manifest.Services["api"] = api
	worker := manifest.Services["worker"]
	worker.Health.URL = "http://127.0.0.1:1/worker-new"
	manifest.Services["worker"] = worker
	session, err = Restart(context.Background(), workspace, RestartOptions{Services: []string{"api"}, SkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(session.HealthChecks) != 2 || session.HealthChecks[0].Name != "worker" || session.HealthChecks[0].URL != "http://old-worker" || session.HealthChecks[1].Name != "api" || session.HealthChecks[1].URL != "http://127.0.0.1:1/api-new" {
		t.Fatalf("subset restart health evidence = %#v", session.HealthChecks)
	}
	if len(session.RuntimeRoutes) != 1 || session.RuntimeRoutes[0].Service != "worker" || session.RuntimeRoutes[0].Target != "old-worker-queue" {
		t.Fatalf("subset restart route evidence = %#v", session.RuntimeRoutes)
	}
}
