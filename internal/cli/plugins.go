package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/config"
	"github.com/leo1394/homebrew-conven/internal/plugins"
	"github.com/leo1394/homebrew-conven/internal/terminal"
)

func (app App) runPlugins(arguments []string) int {
	if len(arguments) == 0 {
		app.printPluginsUsage(app.Error)
		return 2
	}
	action := arguments[0]
	remaining := arguments[1:]
	switch action {
	case "-h", "--help", "help":
		app.printPluginsUsage(app.Output)
		return 0
	case "--install":
		if len(remaining) != 1 {
			return app.fail(fmt.Errorf("plugins --install requires exactly one Python file"))
		}
		source, err := pluginSourcePath(app.Cwd, remaining[0])
		if err != nil {
			return app.fail(err)
		}
		destination, err := plugins.Install(source)
		if err != nil {
			return app.fail(fmt.Errorf("install plugin: %w", err))
		}
		name := strings.TrimSuffix(filepath.Base(destination), filepath.Ext(destination))
		style := terminal.New(app.Output)
		fmt.Fprintf(app.Output, "%s %s\n", style.Stage("Installed plugin"), style.Identifier(name))
		fmt.Fprintln(app.Output, style.Detail("Path: "+destination))
		return 0
	case "--list":
		if len(remaining) != 0 {
			return app.fail(fmt.Errorf("plugins --list does not accept arguments or another action"))
		}
		names, err := plugins.List()
		if err != nil {
			return app.fail(err)
		}
		for _, name := range names {
			fmt.Fprintln(app.Output, name)
		}
		return 0
	case "--run":
		if len(remaining) == 0 {
			return app.fail(fmt.Errorf("plugins --run requires a plugin name"))
		}
		workspace, err := config.FindWorkspace(app.Cwd)
		if err != nil {
			return app.fail(err)
		}
		if err := plugins.Run(app.Context, remaining[0], workspace, remaining[1:], app.Input, app.Output, app.Error); err != nil {
			return app.fail(err)
		}
		return 0
	default:
		style := terminal.New(app.Error)
		fmt.Fprintln(app.Error, style.Failure(fmt.Sprintf("conven: unknown plugins action %q", action)))
		app.printPluginsUsage(app.Error)
		return 2
	}
}

func (app App) printPluginsUsage(output io.Writer) {
	fmt.Fprint(output, `usage:
  conven plugins --install PYTHON_FILE
  conven plugins --list
  conven plugins --run NAME [plugin args...]

--install copies exactly one Python file into ~/.conven/plugins. Relative source
paths are resolved from the current working directory. Arguments after NAME are
passed unchanged to the selected plugin; --workspace is reserved by Conven.
`)
}

func pluginSourcePath(cwd string, source string) (string, error) {
	if filepath.IsAbs(source) {
		return filepath.Clean(source), nil
	}
	base := cwd
	if base == "" {
		var err error
		base, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve current working directory: %w", err)
		}
	}
	absolute, err := filepath.Abs(filepath.Join(base, source))
	if err != nil {
		return "", fmt.Errorf("resolve plugin source %q: %w", source, err)
	}
	return absolute, nil
}
