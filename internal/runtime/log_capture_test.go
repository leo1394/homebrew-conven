package runtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogCaptureSurvivesLauncherExit(t *testing.T) {
	if path := os.Getenv("CONVEN_TEST_CAPTURE_PATH"); path != "" {
		_, err := StartService("capture", []string{"sh", "-c", "sleep 1; printf 'profile active: local\\n'; printf 'stderr output\\n' >&2"}, filepath.Dir(path), CommandEnvironment(), path)
		if err != nil { t.Fatal(err) }
		return
	}
	path := filepath.Join(t.TempDir(), "service.log")
	command := exec.Command(os.Args[0], "-test.run=^TestLogCaptureSurvivesLauncherExit$")
	command.Env = append(os.Environ(), "CONVEN_TEST_CAPTURE_PATH="+path)
	if output, err := command.CombinedOutput(); err != nil { t.Fatalf("launcher: %v: %s", err, output) }
	deadline := time.Now().Add(5*time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(path)
		if string(data) == "profile active: local\nstderr output\n" {
			info, _ := os.Stat(path)
			index := openLogTimes(filepath.Dir(path), path, info)
			if index != nil {
				observed := collectedLogTime(index, 0); index.Close()
				if observed != nil { return }
			}
		}
		time.Sleep(20*time.Millisecond)
	}
	t.Fatal("detached output or persisted capture time missing")
}

func TestCapturedLogsPreserveRawOutputAndSourceTime(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "service.log")
	log, err := openLog(path)
	if err != nil { t.Fatal(err) }
	writer, err := startLogCapture(log, path)
	if err != nil { t.Fatal(err) }
	log.Close()
	raw := "profile active: local\n{\"@timestamp\":\"2026-10-13T22:50:29+08\",\"level\":\"warn\",\"content\":\"ready\"}\n"
	if _, err := writer.Write([]byte(raw)); err != nil { t.Fatal(err) }
	writer.Close()
	var entries []WebLogEntry
	deadline := time.Now().Add(3*time.Second)
	for time.Now().Before(deadline) {
		entries, _, _, _, _, err = readWebLogSource(nil, webLogSource{name: "api", root: root, path: path}, webLogPosition{}, true, 100, 65536, WebLogQuery{}, nil, nil)
		if err != nil { t.Fatal(err) }
		if len(entries) == 2 && entries[0].Time != nil { break }
		time.Sleep(10*time.Millisecond)
	}
	if len(entries) != 2 || entries[0].TimeSource != "collected" || entries[1].TimeSource != "source" { t.Fatalf("entries: %#v", entries) }
	if entries[1].Time.Format(time.RFC3339) != "2026-10-13T14:50:29Z" { t.Fatal("source time replaced") }
	if entries[0].Level != "info" || entries[0].LevelSource != "default" || entries[1].Level != "warn" || entries[1].LevelSource != "source" { t.Fatalf("levels: %#v", entries) }
	data, _ := os.ReadFile(path)
	if string(data) != raw { t.Fatalf("raw output modified: %s", data) }
	info, _ := os.Stat(path+".timestamps")
	if info.Mode().Perm() != 0600 { t.Fatal("capture metadata must be private") }
	// Replacement files must not inherit timestamps from a stale index.
	if err := os.Rename(path, path+".old"); err != nil { t.Fatal(err) }
	if err := os.WriteFile(path, []byte("old untimed log\n"), 0600); err != nil { t.Fatal(err) }
	entries, _, _, _, _, err = readWebLogSource(nil, webLogSource{name:"api", root:root, path:path}, webLogPosition{}, true, 100, 65536, WebLogQuery{}, nil, nil)
	if err != nil || len(entries) != 1 || entries[0].Time != nil { t.Fatalf("stale time used: %#v %v", entries, err) }
}

func TestLogCaptureLongAndPartialOutput(t *testing.T) {
	root := t.TempDir(); path := filepath.Join(root, "service.log")
	log, err := openLog(path); if err != nil { t.Fatal(err) }
	index, err := openLog(filepath.Join(root, "index")); if err != nil { t.Fatal(err) }
	defer log.Close(); defer index.Close()
	raw := strings.Repeat("x", 128*1024)+"\npartial"
	if err := captureLog(strings.NewReader(raw), log, index); err != nil { t.Fatal(err) }
	data, _ := os.ReadFile(path)
	if string(data) != raw { t.Fatal("long or unterminated output lost") }
}

func TestLogLevelExtraction(t *testing.T) {
	for _, item := range []struct{line, level string}{
		{"Start backend log job", "info"},
		{"[conven] local isolation verified", "info"},
		{"2026-10-13T12:00:00Z WARN retrying", "warn"},
		{"10:30AM INF connection ready", "info"},
		{"10:30AM ERR connection failed", "error"},
		{`{"level":"ERROR","message":"failed"}`, "ERROR"},
		{`{"severity":"WARNING","message":"retry"}`, "warn"},
	} {
		parsed, _ := parseWebLogLine(item.line, false)
		if parsed.level != item.level { t.Fatalf("%s: level %q", item.line, parsed.level) }
	}
}

func TestStartupFailureWaitsForCapturedOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	done := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- diagnoseStartupFailure(&processInitializationError{service: "api", exitCode: 29}, ServiceProcess{LogPath: path, captureDone: done}, PlannedService{})
	}()
	select {
	case err := <-result: t.Fatalf("diagnosed before collector drained: %v", err)
	case <-time.After(50*time.Millisecond):
	}
	if err := os.WriteFile(path, []byte("FATAL captured initialization failure\n"), 0600); err != nil { close(done); t.Fatal(err) }
	close(done)
	if err := <-result; !strings.Contains(err.Error(), "FATAL captured initialization failure") { t.Fatalf("missing captured failure: %v", err) }
}
