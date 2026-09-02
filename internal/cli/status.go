package cli

import (
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/config"
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
	if len(serviceNames) == 0 {
		fmt.Fprintln(app.Output, style.Detail("none"))
	}
	for _, name := range serviceNames {
		service := workspace.Manifest.Services[name]
		kinds := service.EffectiveKinds()
		kind := "runner-only"
		if len(kinds) > 0 {
			kind = strings.Join(kinds, "+")
		}
		contract := service.Discovery.Certifier
		if contract == "" {
			contract = "manual"
		}
		consumerSummary := ""
		if len(service.Discovery.Consumers) > 0 {
			consumerNames := append([]string(nil), service.Discovery.Consumers...)
			sort.Strings(consumerNames)
			consumerSummary = ", consumers=" + strings.Join(consumerNames, "+") + ":disabled"
		}
		fmt.Fprintln(app.Output, style.Detail(fmt.Sprintf("%s: type=%s, %s, listener=%s%s, contract=%s, path=%s", style.Identifier(name), kind, statusPortSummary(service.Ports), service.Network.EffectiveListen(), consumerSummary, contract, service.Path)))
	}
	printConfiguredRegistries(workspace, app.Output)
	printConfiguredEndpoints(workspace, app.Output)

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
	if err := convenruntime.Status(app.Context, workspace, app.Output); err != nil {
		return app.fail(err)
	}
	return 0
}

func printConfiguredRegistries(workspace *convenruntime.WorkspaceData, output interface{ Write([]byte) (int, error) }) {
	style := terminal.New(output)
	fmt.Fprintln(output, style.Stage("Configured registries"))
	environments := make([]string, 0, len(workspace.Manifest.Environments))
	for name := range workspace.Manifest.Environments {
		environments = append(environments, name)
	}
	sort.Strings(environments)
	count := 0
	for _, environmentName := range environments {
		registryNames := make([]string, 0, len(workspace.Manifest.Environments[environmentName].Registries))
		for name := range workspace.Manifest.Environments[environmentName].Registries {
			registryNames = append(registryNames, name)
		}
		sort.Strings(registryNames)
		for _, name := range registryNames {
			registry := workspace.Manifest.Environments[environmentName].Registries[name]
			credentials := "none"
			refs := make([]string, 0, 3)
			for _, reference := range []string{registry.TokenEnv, registry.UsernameEnv, registry.PasswordEnv} {
				if reference != "" {
					refs = append(refs, reference)
				}
			}
			if len(refs) > 0 {
				credentials = "env:" + strings.Join(refs, ",")
			}
			fmt.Fprintln(output, style.Detail(fmt.Sprintf("%s.%s: driver=%s, address=%s, namespace=%s, credentials=%s", style.Identifier(environmentName), style.Identifier(name), registry.Driver, registry.Address, registry.Namespace, credentials)))
			count++
		}
	}
	if count == 0 {
		fmt.Fprintln(output, style.Detail("none"))
	}
}

func statusPorts(ports map[string]int) string {
	if len(ports) == 0 {
		return "-"
	}
	names := make([]string, 0, len(ports))
	for name := range ports {
		names = append(names, name)
	}
	sort.Strings(names)
	values := make([]string, 0, len(names))
	for _, name := range names {
		values = append(values, fmt.Sprintf("%s=%d", name, ports[name]))
	}
	return strings.Join(values, ",")
}

func statusPortSummary(ports map[string]int) string {
	if len(ports) == 1 {
		for _, port := range ports {
			return fmt.Sprintf("port=%d", port)
		}
	}
	return "ports=" + statusPorts(ports)
}

func printConfiguredEndpoints(workspace *convenruntime.WorkspaceData, output interface{ Write([]byte) (int, error) }) {
	style := terminal.New(output)
	fmt.Fprintln(output, style.Stage("Configured endpoints"))
	environmentNames := make([]string, 0, len(workspace.Manifest.Environments))
	for name := range workspace.Manifest.Environments {
		environmentNames = append(environmentNames, name)
	}
	sort.Strings(environmentNames)
	count := 0
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
			count++
		}
	}
	if count == 0 {
		fmt.Fprintln(output, style.Detail("none"))
	}
}

func dashboardTailOptions(workspace *convenruntime.WorkspaceData, names []string, version string) (convenruntime.TailOptions, error) {
	disabled := append([]string(nil), workspace.Manifest.Workspace.DisabledBindings...)
	sort.Strings(disabled)
	return convenruntime.TailOptions{Names: names, Version: version, DisabledBindings: disabled}, nil
}
