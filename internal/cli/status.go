package cli

import (
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/config"
	"github.com/leo1394/homebrew-conven/internal/model"
	convenruntime "github.com/leo1394/homebrew-conven/internal/runtime"
	"github.com/leo1394/homebrew-conven/internal/terminal"
)

func (app App) runWorkspaceStatus(arguments []string) int {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage:\n  conven status")
		flags.PrintDefaults()
	}
	if ok, code := parseCommandFlags(flags, arguments, app.Output); !ok {
		return code
	}
	if len(flags.Args()) != 0 {
		return app.fail(errors.New("status does not accept arguments"))
	}
	workspace, err := convenruntime.OpenWorkspace(convenruntime.CommonOptions{Cwd: app.Cwd})
	if err != nil {
		return app.fail(err)
	}
	style := terminal.New(app.Output)
	fmt.Fprintln(app.Output, style.Stage("Workspace"))
	fmt.Fprintln(app.Output, style.Detail("Name: "+style.Identifier(workspace.Manifest.Workspace.Name)))
	fmt.Fprintln(app.Output, style.Detail("Root: "+workspace.Root))
	fmt.Fprintln(app.Output, style.Detail("Manifest: "+workspace.ConfigPath))
	for _, key := range []string{"ktctl.kubeconfig", "ktctl.path"} {
		if value := strings.TrimSpace(workspace.Settings[key]); value != "" {
			fmt.Fprintln(app.Output, style.Detail(key+"="+value))
		}
	}

	fmt.Fprintln(app.Output, style.Stage("Available services"))
	serviceNames := config.ServiceNames(workspace.Manifest)
	readinessProblems := make([]string, 0)
	if len(serviceNames) == 0 {
		fmt.Fprintln(app.Output, style.Detail("none"))
	} else {
		rows := make([]statusRow, 0, len(serviceNames))
		for _, name := range serviceNames {
			service := workspace.Manifest.Services[name]
			summary := statusServiceSummary(name, service)
			if err := convenruntime.ValidateServiceRuntimeSource(workspace, name); err != nil {
				summary += " · source=invalid"
				readinessProblems = append(readinessProblems, err.Error())
			}
			rows = append(rows, statusRow{Label: name, Summary: summary})
		}
		printStatusRows(app.Output, style, rows)
	}
	disabled := append([]string(nil), workspace.Manifest.Workspace.DisabledBindings...)
	sort.Strings(disabled)
	fmt.Fprintln(app.Output, style.Stage("Disabled bindings"))
	if len(disabled) == 0 {
		fmt.Fprintln(app.Output, style.Detail("none"))
	} else {
		for _, binding := range disabled {
			fmt.Fprintln(app.Output, style.Detail(style.Identifier(binding)))
		}
	}
	if len(serviceNames) > 0 {
		fmt.Fprintln(app.Output, style.Stage("Start readiness"))
		if len(readinessProblems) == 0 {
			fmt.Fprintln(app.Output, style.Success("✓ Static runtime source checks passed."))
		} else {
			fmt.Fprintln(app.Output, style.Failure(fmt.Sprintf("✗ %d service source check(s) failed.", len(readinessProblems))))
			for _, problem := range readinessProblems {
				lines := strings.Split(problem, "\n")
				fmt.Fprintln(app.Output, style.Detail(lines[0]))
				for _, line := range lines[1:] {
					fmt.Fprintln(app.Output, line)
				}
			}
		}
	}
	printConfiguredRegistries(workspace, app.Output)
	printConfiguredEndpoints(workspace, app.Output)

	if err := convenruntime.WorkspaceStatus(app.Context, workspace, app.Output); err != nil {
		return app.fail(err)
	}
	return 0
}

func printConfiguredRegistries(workspace *convenruntime.WorkspaceData, output interface{ Write([]byte) (int, error) }) {
	style := terminal.New(output)
	fmt.Fprintln(output, style.Stage("Remote dependency registries"))
	environments := make([]string, 0, len(workspace.Manifest.Environments))
	for name := range workspace.Manifest.Environments {
		environments = append(environments, name)
	}
	sort.Strings(environments)
	rows := make([]statusRow, 0)
	for _, environmentName := range environments {
		registryNames := make([]string, 0, len(workspace.Manifest.Environments[environmentName].Registries))
		for name := range workspace.Manifest.Environments[environmentName].Registries {
			registryNames = append(registryNames, name)
		}
		sort.Strings(registryNames)
		for _, name := range registryNames {
			registry := workspace.Manifest.Environments[environmentName].Registries[name]
			rows = append(rows, statusRow{Label: environmentName+"."+name, Summary: statusRegistrySummary(registry)})
		}
	}
	if len(rows) == 0 {
		fmt.Fprintln(output, style.Detail("none"))
	} else {
		printStatusRows(output, style, rows)
	}
}

type statusRow struct {
	Label   string
	Summary string
}

func printStatusRows(output interface{ Write([]byte) (int, error) }, style terminal.Style, rows []statusRow) {
	labelWidth := 0
	primaryWidth := 0
	for _, row := range rows {
		if len(row.Label) > labelWidth {
			labelWidth = len(row.Label)
		}
		primary, _, _ := strings.Cut(row.Summary, " · ")
		if len(primary) > primaryWidth {
			primaryWidth = len(primary)
		}
	}
	for _, row := range rows {
		primary, remainder, found := strings.Cut(row.Summary, " · ")
		line := style.Identifier(row.Label) + ":" + strings.Repeat(" ", labelWidth-len(row.Label)+4) + primary
		if found {
			line += strings.Repeat(" ", primaryWidth-len(primary)) + " · " + remainder
		}
		fmt.Fprintln(output, style.Detail(line))
	}
}

func statusServiceSummary(name string, service model.Service) string {
	parts := make([]string, 0, 5)
	if len(service.EffectiveKinds()) == 0 {
		parts = append(parts, "runner-only")
	} else {
		portNames := make([]string, 0, len(service.Ports))
		for portName := range service.Ports {
			portNames = append(portNames, portName)
		}
		sort.Strings(portNames)
		endpoints := make([]string, 0, len(portNames))
		for _, portName := range portNames {
			endpoints = append(endpoints, fmt.Sprintf("%s:%d", portName, service.Ports[portName]))
		}
		parts = append(parts, strings.Join(endpoints, " + "))
	}
	contract := service.Discovery.Certifier
	if contract == "" {
		contract = "manual"
	}
	parts = append(parts, contract)
	if service.Network.EffectiveListen() != model.NetworkListenLoopback {
		parts = append(parts, "listen="+service.Network.EffectiveListen())
	}
	consumerNames := append([]string(nil), service.Discovery.Consumers...)
	sort.Strings(consumerNames)
	for _, consumerName := range consumerNames {
		parts = append(parts, consumerName+":enabled(default)")
	}
	if service.Path != name {
		parts = append(parts, "path="+service.Path)
	}
	return strings.Join(parts, " · ")
}

func statusRegistrySummary(registry model.Registry) string {
	parts := []string{registry.Driver, registry.Address}
	if registry.Namespace != "" {
		parts = append(parts, "namespace="+registry.Namespace)
	}
	credentials := make([]string, 0, 3)
	for _, reference := range []string{registry.TokenEnv, registry.UsernameEnv, registry.PasswordEnv} {
		if reference != "" {
			credentials = append(credentials, reference)
		}
	}
	if len(credentials) > 0 {
		parts = append(parts, "credentials=env:"+strings.Join(credentials, ","))
	}
	return strings.Join(parts, " · ")
}

func printConfiguredEndpoints(workspace *convenruntime.WorkspaceData, output interface{ Write([]byte) (int, error) }) {
	environmentNames := make([]string, 0, len(workspace.Manifest.Environments))
	count := 0
	for name := range workspace.Manifest.Environments {
		environmentNames = append(environmentNames, name)
		count += len(workspace.Manifest.Environments[name].Endpoints)
	}
	if count == 0 {
		return
	}
	style := terminal.New(output)
	fmt.Fprintln(output, style.Stage("Configured endpoints"))
	sort.Strings(environmentNames)
	for _, environmentName := range environmentNames {
		environment := workspace.Manifest.Environments[environmentName]
		endpointNames := make([]string, 0, len(environment.Endpoints))
		for name := range environment.Endpoints {
			endpointNames = append(endpointNames, name)
		}
		sort.Strings(endpointNames)
		for _, name := range endpointNames {
			endpoint := environment.Endpoints[name]
			protocol := endpoint.Protocol
			if protocol == "" {
				protocol = "tcp"
			}
			readiness := endpoint.Readiness.Type
			if readiness == "" {
				readiness = "tcp"
			}
			fmt.Fprintln(output, style.Detail(fmt.Sprintf("%s.%s: protocol=%s, address=%s, readiness=%s", style.Identifier(environmentName), style.Identifier(name), protocol, endpoint.Address, readiness)))
		}
	}
}

func dashboardTailOptions(workspace *convenruntime.WorkspaceData, names []string, version string) (convenruntime.TailOptions, error) {
	disabled := append([]string(nil), workspace.Manifest.Workspace.DisabledBindings...)
	sort.Strings(disabled)
	return convenruntime.TailOptions{Names: names, Version: version, DisabledBindings: disabled}, nil
}
