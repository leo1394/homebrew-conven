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
		fmt.Fprintln(app.Output, style.Detail(fmt.Sprintf("%s: type=%s, ports=%s, path=%s", style.Identifier(name), kind, statusPorts(service.Ports), service.Path)))
	}

	disabled := append([]string(nil), catalog.DisabledRPCBindings...)
	sort.Strings(disabled)
	fmt.Fprintln(app.Output, style.Stage("Disabled RPC bindings"))
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

func dashboardTailOptions(workspace *convenruntime.WorkspaceData, names []string, version string) (convenruntime.TailOptions, error) {
	catalog, err := config.LoadCatalog(config.CatalogPath(workspace.Root))
	if err != nil {
		return convenruntime.TailOptions{}, err
	}
	disabled := append([]string(nil), catalog.DisabledRPCBindings...)
	sort.Strings(disabled)
	return convenruntime.TailOptions{Names: names, Version: version, DisabledRPCBindings: disabled}, nil
}
