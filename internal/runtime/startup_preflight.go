package runtime

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/materialize"
	"github.com/leo1394/homebrew-conven/internal/terminal"
)

func sessionHealthChecks(plan *Plan) []SessionHealthCheck {
	checks := make([]SessionHealthCheck, 0)
	for _, name := range plan.Order {
		for _, check := range plan.Services[name].HealthChecks {
			if check.Type != "http" && check.Type != "tcp" {
				continue
			}
			snapshot := SessionHealthCheck{
				Name:    name,
				Server:  check.Server,
				Type:    check.Type,
				Address: check.Address,
			}
			if check.Type == "http" {
				snapshot.URL = safeSessionHealthURL(check.URL)
				if snapshot.URL == "" {
					continue
				}
			}
			checks = append(checks, snapshot)
		}
	}
	return checks
}

func safeSessionHealthURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	if parsed.User != nil {
		return ""
	}
	query := parsed.Query()
	for key := range query {
		if diagnosticSensitiveKey(key) {
			return ""
		}
	}
	parsed.RawQuery = query.Encode()
	parsed.Fragment = ""
	if strings.Contains(RedactDiagnosticText(parsed.String()), "[REDACTED]") {
		return ""
	}
	return parsed.String()
}

func mergeSessionHealthChecks(existing []SessionHealthCheck, planned []SessionHealthCheck, targets []string) []SessionHealthCheck {
	targetSet := make(map[string]bool, len(targets))
	for _, name := range targets {
		targetSet[name] = true
	}
	merged := make([]SessionHealthCheck, 0, len(existing)+len(planned))
	for _, check := range existing {
		if !targetSet[check.Name] {
			merged = append(merged, check)
		}
	}
	for _, check := range planned {
		if targetSet[check.Name] {
			merged = append(merged, check)
		}
	}
	return merged
}

func mergeSessionRuntimeRoutes(existing []DiagnosticRoute, planned []DiagnosticRoute, targets []string) []DiagnosticRoute {
	targetSet := make(map[string]bool, len(targets))
	for _, name := range targets {
		targetSet[name] = true
	}
	merged := make([]DiagnosticRoute, 0, len(existing)+len(planned))
	for _, route := range existing {
		if !targetSet[route.Service] {
			merged = append(merged, route)
		}
	}
	for _, route := range planned {
		if targetSet[route.Service] {
			merged = append(merged, route)
		}
	}
	return merged
}

func filterSessionHealthChecks(existing []SessionHealthCheck, excluded map[string]bool) []SessionHealthCheck {
	filtered := make([]SessionHealthCheck, 0, len(existing))
	for _, check := range existing {
		if !excluded[check.Name] {
			filtered = append(filtered, check)
		}
	}
	return filtered
}

func filterSessionRuntimeRoutes(existing []DiagnosticRoute, excluded map[string]bool) []DiagnosticRoute {
	filtered := make([]DiagnosticRoute, 0, len(existing))
	for _, route := range existing {
		if !excluded[route.Service] {
			filtered = append(filtered, route)
		}
	}
	return filtered
}

func materializeRuntimeConfigs(ctx context.Context, plan *Plan, names []string, output io.Writer) error {
	style := terminal.New(output)
	for _, name := range names {
		service := plan.Services[name]
		if err := ensurePrivateDirectory(filepath.Join(plan.RunDir, "configs", name)); err != nil {
			return fmt.Errorf("create %s runtime config directory: %w", name, err)
		}
		if service.Config == nil {
			continue
		}
		if service.Config.Plan.Driver != materialize.DriverEnvironment {
			fmt.Fprintf(output, "%s %s config\n", style.Stage("Materializing"), style.Identifier(name))
			fmt.Fprintln(output, style.Detail(fmt.Sprintf("Drivers: %s -> %s", service.Config.Plan.SourceDriver, service.Config.Plan.Driver)))
			origins, err := materialize.MaterializeWithReport(ctx, service.Config.Plan)
			if err != nil {
				return fmt.Errorf("materialize %s config: %w", name, err)
			}
			for _, origin := range origins { fmt.Fprintln(output, style.Detail(fmt.Sprintf("Remote route: %s via %s", origin.Binding, origin.Source))) }
		}
		if err := verifyServiceIsolation(service); err != nil {
			return err
		}
	}
	return nil
}

func runRuntimePreflight(ctx context.Context, plan *Plan, output io.Writer, announce bool) error {
	style := terminal.New(output)
	if err := preflightDisabledBindings(plan, output, announce); err != nil {
		return err
	}
	if err := preflightRPCClientConfigs(plan); err != nil {
		return err
	}
	preflightEnabled := false
	for _, name := range plan.Order {
		service := plan.Services[name]
		if err := verifyServiceIsolation(service); err != nil {
			if announce {
				fmt.Fprintln(output, style.Stage("Local isolation preflight"))
				fmt.Fprintln(output, style.Failure("✗ Local isolation preflight failed."))
			} else {
				fmt.Fprintln(output, style.Failure("✗ Final runtime isolation recheck failed."))
			}
			return err
		}
	}
	dependencies := make([]ExternalConsulDependency, 0)
	for _, name := range plan.Order {
		service := plan.Services[name]
		detected, enabled, err := inspectRuntimeContractExternalDependencies(service, service.Kind)
		if err != nil {
			if announce {
				fmt.Fprintln(output, style.Stage("Local isolation preflight"))
				fmt.Fprintln(output, style.Failure("✗ Final config isolation and dependency inspection failed."))
			} else {
				fmt.Fprintln(output, style.Failure("✗ Final runtime config and dependency recheck failed."))
			}
			return err
		}
		if enabled {
			preflightEnabled = true
		}
		dependencies = append(dependencies, detected...)
	}
	if announce {
		fmt.Fprintln(output, style.Stage("Local isolation preflight"))
		for _, name := range plan.Order {
			printVerifiedIsolation(output, plan.Services[name])
		}
		connection := "none configured by Conven"
		if plan.Connection.Driver == "ktctl" {
			connection = "ktctl connect only"
		}
		fmt.Fprintln(output, style.Success("✓ Conven inbound routing contract: "+connection+"."))
		if preflightEnabled {
			fmt.Fprintln(output, style.Stage("External Consul dependency preflight"))
		}
	}
	if err := preflightExternalConsulDependencies(ctx, dependencies); err != nil {
		if preflightEnabled {
			fmt.Fprintln(output, style.Detail(err.Error()))
			if announce {
				fmt.Fprintln(output, style.Failure("✗ External Consul dependency preflight failed."))
			} else {
				fmt.Fprintln(output, style.Failure("✗ External Consul dependency recheck failed."))
			}
		}
		return err
	}
	if announce && preflightEnabled {
		if len(dependencies) == 0 {
			fmt.Fprintln(output, style.Success("✓ No active external Consul dependencies detected in materialized configs."))
		} else {
			fmt.Fprintln(output, style.Success(fmt.Sprintf("✓ External Consul dependencies healthy: %d binding(s).", len(dependencies))))
		}
	}
	return nil
}
