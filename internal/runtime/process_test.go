package runtime

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
		done <- RunForeground(ctx, []string{"sh", "-c", "trap '' TERM; sleep 600 & echo $$ > \"$1\"; wait", "sh", pidPath}, directory, CommandEnvironment(), nil, filepath.Join(directory, "foreground.log"))
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
	case <-time.After(6 * time.Second):
		t.Fatal("foreground cancellation did not return")
	}
	if ProcessGroupAlive(pid) {
		t.Fatalf("cancelled foreground process group %d is still active", pid)
	}
}

func TestRunForegroundDoesNotFailWhenDisplayOutputCloses(t *testing.T) {
	directory := t.TempDir()
	markerPath := filepath.Join(directory, "complete")
	logPath := filepath.Join(directory, "foreground.log")
	err := RunForeground(
		context.Background(),
		[]string{"sh", "-c", `set -e; dd if=/dev/zero bs=4096 count=256; printf complete > "$1"`, "sh", markerPath},
		directory,
		CommandEnvironment(),
		failingCommandOutput{},
		logPath,
	)
	if err != nil {
		t.Fatalf("display output failure stopped foreground command: %v", err)
	}
	if data, err := os.ReadFile(markerPath); err != nil || string(data) != "complete" {
		t.Fatalf("foreground command did not complete: data=%q err=%v", data, err)
	}
	if info, err := os.Stat(logPath); err != nil || info.Size() == 0 {
		t.Fatalf("foreground log was not retained: info=%v err=%v", info, err)
	}
}

func TestRunForegroundReportsLogWriteFailureInsteadOfBrokenPipe(t *testing.T) {
	directory := t.TempDir()
	markerPath := filepath.Join(directory, "complete")
	logPath := filepath.Join(directory, "foreground.log")
	err := runForeground(
		context.Background(),
		[]string{"sh", "-c", `set -e; dd if=/dev/zero bs=4096 count=256; printf complete > "$1"`, "sh", markerPath},
		directory,
		CommandEnvironment(),
		io.Discard,
		failingLogOutput{},
		logPath,
	)
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("error = %v, want no-space log failure", err)
	}
	if !strings.Contains(err.Error(), logPath) || !strings.Contains(err.Error(), "no space left") {
		t.Fatalf("error does not identify the failed log: %v", err)
	}
	if strings.Contains(err.Error(), "broken pipe") {
		t.Fatalf("log write failure was masked as a broken pipe: %v", err)
	}
	if data, err := os.ReadFile(markerPath); err != nil || string(data) != "complete" {
		t.Fatalf("foreground command did not complete after log failure: data=%q err=%v", data, err)
	}
}

type failingCommandOutput struct{}

func (failingCommandOutput) Write([]byte) (int, error) {
	return 0, syscall.EPIPE
}

type failingLogOutput struct{}

func (failingLogOutput) Write([]byte) (int, error) {
	return 0, syscall.ENOSPC
}
