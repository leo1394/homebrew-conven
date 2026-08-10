package runtime

import (
	"context"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

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
		fmt.Fprintf(output, "%s %s config\n", style.Stage("Materializing"), style.Identifier(name))
		fmt.Fprintln(output, style.Detail(fmt.Sprintf("Drivers: %s -> %s", service.Config.Plan.SourceDriver, service.Config.Plan.Driver)))
		if err := materialize.Materialize(ctx, service.Config.Plan); err != nil {
			return fmt.Errorf("materialize %s config: %w", name, err)
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
		if service.Config != nil && service.Config.Framework == "go-zero" && service.Config.Discovery == "consul" && service.Config.Plan.Driver == materialize.DriverYAMLOverlay {
			preflightEnabled = true
		}
	}
	dependencies := make([]ExternalConsulDependency, 0)
	for _, name := range plan.Order {
		service := plan.Services[name]
		detected, err := detectExternalConsulDependencies(service, service.Kind)
		if err != nil {
			if announce {
				fmt.Fprintln(output, style.Stage("Local isolation preflight"))
				fmt.Fprintln(output, style.Failure("✗ Final config isolation and dependency inspection failed."))
			} else {
				fmt.Fprintln(output, style.Failure("✗ Final runtime config and dependency recheck failed."))
			}
			return err
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
	if announce && preflightEnabled {
		printExternalDependencyTargets(output, dependencies)
	}
	if err := preflightExternalConsulDependencies(ctx, dependencies); err != nil {
		if preflightEnabled {
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

func printExternalDependencyTargets(output io.Writer, dependencies []ExternalConsulDependency) {
	style := terminal.New(output)
	if len(dependencies) == 0 {
		return
	}
	byOwner := make(map[string][]ExternalConsulDependency)
	owners := make([]string, 0)
	for _, dependency := range dependencies {
		if _, found := byOwner[dependency.Owner]; !found {
			owners = append(owners, dependency.Owner)
		}
		byOwner[dependency.Owner] = append(byOwner[dependency.Owner], dependency)
	}
	sort.Strings(owners)
	for _, owner := range owners {
		references := make([]string, 0, len(byOwner[owner]))
		for _, dependency := range byOwner[owner] {
			endpoint := net.JoinHostPort(dependency.Host, strconv.Itoa(dependency.Port))
			references = append(references, fmt.Sprintf("%s -> %s via %s", style.Identifier(externalDependencyPathLabel(dependency.Path)), style.Identifier(dependency.Key), style.Identifier(endpoint)))
		}
		fmt.Fprintln(output, style.Detail(style.Identifier(owner)+": "+strings.Join(references, ", ")))
	}
}
