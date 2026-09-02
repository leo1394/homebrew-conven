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
		if !ProcessAlive(process.PID) {
			return fmt.Errorf("%s exited before becoming healthy", process.Name)
		}
		lastError = checkHealth(healthContext, check)
		if lastError == nil {
			if !ProcessAlive(process.PID) {
				return fmt.Errorf("%s exited before becoming healthy", process.Name)
			}
			return nil
		}
		if healthContext.Err() != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
			return fmt.Errorf("%s health check timed out after %s: %w", process.Name, check.Timeout, lastError)
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-healthContext.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
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
