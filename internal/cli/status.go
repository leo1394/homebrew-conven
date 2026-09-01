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
	catalog, catalogPath, err := config.LoadWorkspaceCatalog(workspace.Root)
	if err != nil {
		return app.fail(err)
	}
	style := terminal.New(app.Output)
	fmt.Fprintln(app.Output, style.Stage("Workspace"))
	fmt.Fprintln(app.Output, style.Detail("Name: "+style.Identifier(workspace.Manifest.Workspace.Name)))
	fmt.Fprintln(app.Output, style.Detail("Root: "+workspace.Root))
	fmt.Fprintln(app.Output, style.Detail("Manifest: "+workspace.ConfigPath))
	fmt.Fprintln(app.Output, style.Detail("Catalog: "+catalogPath))
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
		kind := service.Kind
		if kind == "" {
			kind = "-"
		}
		fmt.Fprintln(app.Output, style.Detail(fmt.Sprintf("%s: type=%s, %s, listener=%s, path=%s", style.Identifier(name), kind, statusPortSummary(service.Ports), service.Network.EffectiveListen(), service.Path)))
	}
	printConfiguredEndpoints(workspace, app.Output)

	disabled := append([]string(nil), catalog.DisabledRPCBindings...)
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
	catalog, err := config.LoadCatalog(config.CatalogPath(workspace.Root))
	if err != nil {
		return convenruntime.TailOptions{}, err
	}
	disabled := append([]string(nil), catalog.DisabledRPCBindings...)
	sort.Strings(disabled)
	return convenruntime.TailOptions{Names: names, Version: version, DisabledRPCBindings: disabled}, nil
}
