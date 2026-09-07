package runtime

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckHealthTCP(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := checkHealth(context.Background(), HealthCheck{Type: "tcp", Address: listener.Addr().String()}); err != nil {
		t.Fatal(err)
	}
}

func TestCheckHealthRejectsUnknownType(t *testing.T) {
	err := checkHealth(context.Background(), HealthCheck{Type: "grpc", Timeout: time.Second})
	if err == nil {
		t.Fatal("unknown health type unexpectedly passed")
	}
}

func TestWaitHealthyCommandHonorsTimeout(t *testing.T) {
	directory := t.TempDir()
	process, err := StartService("health-service", []string{"sleep", "600"}, directory, CommandEnvironment(), filepath.Join(directory, "service.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = StopProcess(process, time.Second)
	}()

	started := time.Now()
	err = WaitHealthy(context.Background(), process, HealthCheck{
		Type:      "command",
		Command:   []string{"sleep", "600"},
		Directory: directory,
		Timeout:   100 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %v, want timeout", err)
	}
	var timeout *readinessTimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("error = %T, want readiness timeout", err)
	}
	var initialization *processInitializationError
	if errors.As(err, &initialization) {
		t.Fatalf("readiness timeout was classified as process initialization failure: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("health timeout took %s", elapsed)
	}
}

func TestWaitHealthyReportsEarlyProcessExitCode(t *testing.T) {
	directory := t.TempDir()
	process, err := StartService("health-service", []string{"sh", "-c", "exit 23"}, directory, CommandEnvironment(), filepath.Join(directory, "service.log"))
	if err != nil {
		t.Fatal(err)
	}
	err = WaitHealthy(context.Background(), process, HealthCheck{Type: "tcp", Address: "127.0.0.1:1", Timeout: time.Second})
	var initialization *processInitializationError
	if !errors.As(err, &initialization) {
		t.Fatalf("error = %v, want process initialization failure", err)
	}
	if initialization.exitCode != 23 {
		t.Fatalf("exit code = %d, want 23", initialization.exitCode)
	}
}
