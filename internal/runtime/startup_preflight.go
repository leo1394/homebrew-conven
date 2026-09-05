package runtime

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/leo1394/homebrew-conven/internal/materialize"
	"github.com/leo1394/homebrew-conven/internal/terminal"
)

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
			if err := materialize.Materialize(ctx, service.Config.Plan); err != nil {
				return fmt.Errorf("materialize %s config: %w", name, err)
			}
		}
		if err := verifyServiceIsolation(service); err != nil {
			return err
		}
	}
	return nil
}

func runRuntimePreflight(ctx context.Context, plan *Plan, output io.Writer, announce bool) error {
	style := terminal.New(output)
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
