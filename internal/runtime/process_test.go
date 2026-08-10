package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestStopProcessAcceptsExecAndStopsProcessGroup(t *testing.T) {
	directory := t.TempDir()
	process, err := StartService("exec-service", []string{"sh", "-c", "exec sleep 600"}, directory, CommandEnvironment(), filepath.Join(directory, "service.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = StopProcess(process, 100*time.Millisecond)
	}()

	if err := StopProcess(process, time.Second); err != nil {
		t.Fatalf("stop exec service: %v", err)
	}
	if ProcessGroupAlive(process.PGID) {
		t.Fatalf("process group %d is still active", process.PGID)
	}
}

func TestStopProcessEscalatesForWholeProcessGroup(t *testing.T) {
	directory := t.TempDir()
	process, err := StartService("group-service", []string{"sh", "-c", "trap '' TERM; sleep 600 & wait"}, directory, CommandEnvironment(), filepath.Join(directory, "service.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = StopProcess(process, 100*time.Millisecond)
	}()

	if err := StopProcess(process, 100*time.Millisecond); err != nil {
		t.Fatalf("stop process group: %v", err)
	}
	if ProcessGroupAlive(process.PGID) {
		t.Fatalf("process group %d is still active", process.PGID)
	}
}

func TestStopProcessRejectsChangedStartIdentity(t *testing.T) {
	directory := t.TempDir()
	process, err := StartService("identity-service", []string{"sleep", "600"}, directory, CommandEnvironment(), filepath.Join(directory, "service.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = StopProcess(process, time.Second)
	}()

	changed := process
	changed.Identity = "different start token"
	if err := StopProcess(changed, 100*time.Millisecond); err == nil || !strings.Contains(err.Error(), "start identity changed") {
		t.Fatalf("error = %v, want changed identity refusal", err)
	}
	if !ProcessAlive(process.PID) {
		t.Fatal("identity mismatch stopped the unrelated process")
	}
}

func TestRunForegroundCancellationStopsProcessGroup(t *testing.T) {
	directory := t.TempDir()
	pidPath := filepath.Join(directory, "pid")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunForeground(ctx, []string{"sh", "-c", "echo $$ > \"$1\"; trap '' TERM; sleep 600 & wait", "sh", pidPath}, directory, CommandEnvironment(), nil, filepath.Join(directory, "foreground.log"))
	}()

	var data []byte
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, _ = os.ReadFile(pidPath)
		if len(data) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(data) == 0 {
		cancel()
		t.Fatal("foreground command did not record its pid")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context cancellation", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("foreground cancellation did not return")
	}
	if ProcessGroupAlive(pid) {
		t.Fatalf("cancelled foreground process group %d is still active", pid)
	}
}
