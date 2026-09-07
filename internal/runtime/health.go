package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type HealthCheck struct {
	Server      string
	Type        string
	Address     string
	URL         string
	Command     []string
	Directory   string
	Environment []string
	Timeout     time.Duration
}

type processInitializationError struct {
	service  string
	exitCode int
}

func (err *processInitializationError) Error() string {
	if err.exitCode < 0 {
		return fmt.Sprintf("%s initialization failed: process exited (exit code unavailable or terminated by signal)", err.service)
	}
	return fmt.Sprintf("%s initialization failed: process exited with code %d", err.service, err.exitCode)
}

type readinessTimeoutError struct {
	service string
	timeout time.Duration
	cause   error
}

func (err *readinessTimeoutError) Error() string {
	return fmt.Sprintf("%s readiness timed out after %s: %v", err.service, err.timeout, err.cause)
}

func (err *readinessTimeoutError) Unwrap() error {
	return err.cause
}

func WaitHealthy(ctx context.Context, process ServiceProcess, check HealthCheck) error {
	if check.Timeout <= 0 {
		check.Timeout = 60 * time.Second
	}
	if check.Type == "" {
		check.Type = "process"
	}
	healthContext, cancel := context.WithTimeout(ctx, check.Timeout)
	defer cancel()
	var lastError error
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if exitCode, exited := serviceProcessExitCode(process); exited {
			return &processInitializationError{service: process.Name, exitCode: exitCode}
		}
		lastError = checkHealth(healthContext, check)
		if err := ctx.Err(); err != nil {
			return err
		}
		if exitCode, exited := serviceProcessExitCode(process); exited {
			return &processInitializationError{service: process.Name, exitCode: exitCode}
		}
		if lastError == nil {
			return nil
		}
		if healthContext.Err() != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
			return &readinessTimeoutError{service: process.Name, timeout: check.Timeout, cause: lastError}
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-healthContext.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

func serviceProcessExitCode(process ServiceProcess) (int, bool) {
	if process.exit != nil {
		select {
		case <-process.exit.done:
			return process.exit.code, true
		default:
			return 0, false
		}
	}
	if !ProcessAlive(process.PID) {
		return -1, true
	}
	return 0, false
}

func WaitHealthyChecks(ctx context.Context, process ServiceProcess, checks []HealthCheck) error {
	if len(checks) == 0 {
		checks = []HealthCheck{{Type: "process"}}
	}
	for _, check := range checks {
		if err := WaitHealthy(ctx, process, check); err != nil {
			if check.Server != "" {
				return fmt.Errorf("%s listener %s: %w", process.Name, check.Server, err)
			}
			return err
		}
	}
	return nil
}

func checkHealth(ctx context.Context, check HealthCheck) error {
	switch check.Type {
	case "process":
		timer := time.NewTimer(250 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	case "tcp":
		if check.Address == "" {
			return errors.New("tcp health address is empty")
		}
		dialer := &net.Dialer{Timeout: 500 * time.Millisecond}
		connection, err := dialer.DialContext(ctx, "tcp", check.Address)
		if err != nil {
			return err
		}
		return connection.Close()
	case "http":
		if check.URL == "" {
			return errors.New("http health URL is empty")
		}
		client := &http.Client{Timeout: time.Second}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, check.URL, nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err != nil {
			return err
		}
		response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 400 {
			return fmt.Errorf("HTTP status %d", response.StatusCode)
		}
		return nil
	case "command":
		if len(check.Command) == 0 {
			return errors.New("command health check is empty")
		}
		command := exec.Command(check.Command[0], check.Command[1:]...)
		command.Dir = check.Directory
		command.Env = check.Environment
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		var output bytes.Buffer
		command.Stdout = &output
		command.Stderr = &output
		if err := command.Start(); err != nil {
			return err
		}
		if err := waitCommandContext(ctx, command, syscall.SIGTERM, time.Second); err != nil {
			detail := strings.TrimSpace(output.String())
			if detail == "" {
				return err
			}
			return fmt.Errorf("%s: %w", detail, err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported health type %q", check.Type)
	}
}
