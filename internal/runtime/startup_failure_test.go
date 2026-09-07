package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/materialize"
)

func TestDiagnoseStartupFailureUsesCurrentFatalLogAndSupportedBinding(t *testing.T) {
	directory := t.TempDir()
	logPath := filepath.Join(directory, "api.log")
	previous := "FATAL old launch should not be reported\n"
	current := "ERROR recovered while loading optional defaults\nFATAL initialize orderRpc: endpoint missing\nFATAL later failure\n"
	if err := os.WriteFile(logPath, []byte(previous+current), 0600); err != nil {
		t.Fatal(err)
	}
	service := PlannedService{Config: &PlannedConfig{
		Plan: materialize.Plan{TargetDir: filepath.Join(directory, "configs", "api"), Application: "application.yaml"},
		Routes: []PlannedRoute{{Dependency: "order", Binding: "orderRpc"}},
	}}
	err := diagnoseStartupFailure(
		&processInitializationError{service: "api", exitCode: 7},
		ServiceProcess{Name: "api", LogPath: logPath, logOffset: int64(len(previous))},
		service,
	)
	want := []string{
		"api initialization failed: process exited with code 7",
		`first fatal log: "FATAL initialize orderRpc: endpoint missing"`,
		"config path: " + filepath.Join(directory, "configs", "api", "application.yaml"),
		"binding: orderRpc (dependency order)",
	}
	for _, part := range want {
		if !strings.Contains(err.Error(), part) {
			t.Fatalf("diagnostic %q does not contain %q", err, part)
		}
	}
	if strings.Contains(err.Error(), "old launch") || strings.Contains(err.Error(), "later failure") {
		t.Fatalf("diagnostic used the wrong fatal log: %v", err)
	}
}

func TestDiagnoseStartupFailureDoesNotGuessBinding(t *testing.T) {
	directory := t.TempDir()
	logPath := filepath.Join(directory, "api.log")
	if err := os.WriteFile(logPath, []byte("FATAL configuration is invalid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	service := PlannedService{Config: &PlannedConfig{Routes: []PlannedRoute{
		{Dependency: "order", Binding: "orderRpc"},
		{Dependency: "user", Binding: "userRpc"},
	}}}
	err := diagnoseStartupFailure(
		&processInitializationError{service: "api", exitCode: 1},
		ServiceProcess{Name: "api", LogPath: logPath},
		service,
	)
	if strings.Contains(err.Error(), "binding:") {
		t.Fatalf("diagnostic guessed a binding: %v", err)
	}
}

func TestDiagnoseStartupFailureDoesNotMatchBindingSubstring(t *testing.T) {
	directory := t.TempDir()
	logPath := filepath.Join(directory, "api.log")
	if err := os.WriteFile(logPath, []byte("FATAL orderRpcBackup configuration is invalid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	service := PlannedService{Config: &PlannedConfig{Routes: []PlannedRoute{
		{Dependency: "order", Binding: "orderRpc"},
	}}}
	err := diagnoseStartupFailure(
		&processInitializationError{service: "api", exitCode: 1},
		ServiceProcess{Name: "api", LogPath: logPath},
		service,
	)
	if strings.Contains(err.Error(), "binding:") {
		t.Fatalf("diagnostic treated a substring as binding evidence: %v", err)
	}
}

func TestStartupFatalLogRecognizesEmptyEtcdHosts(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "api.log")
	line := "2026/09/07 10:11:12 empty etcd hosts"
	if err := os.WriteFile(logPath, []byte("ERROR recovered optional lookup\n"+line+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, first, truncated := startupFatalLogEvidence(logPath, 0)
	if got != line || !first || truncated {
		t.Fatalf("fatal evidence = %q, first=%t, truncated=%t", got, first, truncated)
	}
}

func TestStartupFatalLogDoesNotPromoteOrdinaryError(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "api.log")
	if err := os.WriteFile(logPath, []byte("ERROR optional lookup failed; using cached value\nstartup continued\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, first, truncated := startupFatalLogEvidence(logPath, 0)
	if got != "" || first || truncated {
		t.Fatalf("ordinary error was promoted to fatal evidence: %q, first=%t, truncated=%t", got, first, truncated)
	}
}

func TestUsefulFatalLogLineRejectsInformationalMentions(t *testing.T) {
	lines := []string{
		"INFO startup: panic recovery enabled",
		"INFO fatal error reporting configured",
		`{"level":"info","message":"fatal shutdown handler installed"}`,
		`{"severity":"INFO","message":"panic recovery enabled"}`,
		"level=info message=fatal-handler-enabled",
	}
	for _, line := range lines {
		if usefulFatalLogLine(line) {
			t.Errorf("informational line certified fatal: %q", line)
		}
	}
}

func TestUsefulFatalLogLineAcceptsStructuredAndAnchoredFatalSeverity(t *testing.T) {
	lines := []string{
		"FATAL configuration invalid",
		"panic: empty etcd hosts",
		"2026/09/07 10:11:12 FATAL configuration invalid",
		`{"level":"fatal","message":"configuration invalid"}`,
		`{"severity":"PANIC","message":"configuration invalid"}`,
		`2026/09/07 11:22:40 {"@timestamp":"2026-09-07T11:22:40+08:00","level":"fatal","content":"configuration invalid"}`,
		"level=fatal message=configuration-invalid",
	}
	for _, line := range lines {
		if !usefulFatalLogLine(line) {
			t.Errorf("fatal line was not recognized: %q", line)
		}
	}
}

func TestUsefulFatalLogLineRejectsDatedInformationalJSONMention(t *testing.T) {
	line := `2026/09/07 11:22:40 {"@timestamp":"2026-09-07T11:22:40+08:00","level":"info","content":"panic recovery enabled"}`
	if usefulFatalLogLine(line) {
		t.Fatalf("dated informational JSON line certified fatal: %q", line)
	}
}

func TestStartupFatalLogUsesBoundedTailFallback(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "api.log")
	verbose := strings.Repeat("verbose initialization detail\n", startupFailureLogBytes/10)
	if err := os.WriteFile(logPath, []byte(verbose+"FATAL late initialization failure\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, first, truncated := startupFatalLogEvidence(logPath, 0)
	if got != "FATAL late initialization failure" || first || !truncated {
		t.Fatalf("fatal evidence = %q, first=%t, truncated=%t", got, first, truncated)
	}
}

func TestDiagnoseStartupFailureLeavesReadinessTimeoutUnchanged(t *testing.T) {
	timeout := &readinessTimeoutError{service: "api", cause: errors.New("connection refused")}
	if got := diagnoseStartupFailure(timeout, ServiceProcess{}, PlannedService{}); got != timeout {
		t.Fatalf("readiness timeout was replaced: %v", got)
	}
}
