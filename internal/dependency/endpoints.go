package dependency

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/leo1394/homebrew-conven/internal/model"
)

func EndpointNames(resolutions map[string]map[string]Resolution) []string {
	targets := make(map[string]bool)
	for _, dependencies := range resolutions {
		for _, resolution := range dependencies {
			if resolution.Mode == "endpoint" {
				targets[resolution.Target] = true
			}
		}
	}
	names := make([]string, 0, len(targets))
	for name := range targets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func CheckEndpoints(ctx context.Context, workspace string, environment []string, declaration model.Environment, resolutions map[string]map[string]Resolution) error {
	for _, name := range EndpointNames(resolutions) {
		endpoint := declaration.Endpoints[name]
		readiness := endpoint.Readiness
		if readiness.Type == "" {
			readiness.Type = "tcp"
			readiness.Address = endpoint.Address
		}
		if err := waitEndpointReady(ctx, workspace, environment, readiness); err != nil {
			return fmt.Errorf("endpoint %s readiness: %w", name, err)
		}
	}
	return nil
}

func waitEndpointReady(ctx context.Context, workspace string, environment []string, readiness model.Health) error {
	timeout := 60 * time.Second
	if readiness.Timeout != "" {
		parsed, err := time.ParseDuration(readiness.Timeout)
		if err != nil {
			return err
		}
		timeout = parsed
	}
	deadlineContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		switch readiness.Type {
		case "tcp":
			connection, err := (&net.Dialer{Timeout: time.Second}).DialContext(deadlineContext, "tcp", readiness.Address)
			lastErr = err
			if connection != nil {
				connection.Close()
			}
		case "http":
			request, err := http.NewRequestWithContext(deadlineContext, http.MethodGet, readiness.URL, nil)
			if err != nil {
				return err
			}
			response, err := (&http.Client{Timeout: 2 * time.Second}).Do(request)
			lastErr = err
			if response != nil {
				response.Body.Close()
				if response.StatusCode < 200 || response.StatusCode >= 400 {
					lastErr = fmt.Errorf("HTTP status %s", response.Status)
				}
			}
		case "command":
			command := exec.CommandContext(deadlineContext, readiness.Command[0], readiness.Command[1:]...)
			command.Dir = workspace
			command.Env = endpointCommandEnvironment(environment)
			lastErr = command.Run()
		default:
			return fmt.Errorf("unsupported readiness type %q", readiness.Type)
		}
		if lastErr == nil {
			return nil
		}
		select {
		case <-deadlineContext.Done():
			return fmt.Errorf("timed out after %s: %w", timeout, lastErr)
		case <-ticker.C:
		}
	}
}

func endpointCommandEnvironment(values []string) []string {
	merged := make(map[string]string)
	for _, entry := range append(os.Environ(), values...) {
		key, value, found := strings.Cut(entry, "=")
		if found {
			merged[key] = value
		}
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+merged[key])
	}
	return result
}
