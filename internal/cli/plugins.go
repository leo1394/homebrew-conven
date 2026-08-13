package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/leo1394/homebrew-conven/internal/config"
	"github.com/leo1394/homebrew-conven/internal/plugins"
	"github.com/leo1394/homebrew-conven/internal/terminal"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
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
		global, remaining := consumePluginGlobal(remaining)
		if len(remaining) != 1 {
			return app.fail(fmt.Errorf("plugins --install requires exactly one Python file"))
		}
		store, workspace, err := app.pluginStore(global)
		if err != nil {
			return app.fail(err)
		}
		source, err := pluginSourcePath(app.Cwd, remaining[0])
		if err != nil {
			return app.fail(err)
		}
		destination, err := store.Install(source)
		if errors.Is(err, plugins.ErrAlreadyInstalled) {
			name := strings.TrimSuffix(filepath.Base(source), filepath.Ext(source))
			fmt.Fprintf(app.Error, "%s plugin already exists: %s\n", pluginScopeTitle(store.Scope()), filepath.Join(store.Directory(), name+".py"))
			overwrite, promptErr := confirmPluginOverwrite(app.Context, app.Input, app.Error, store.Scope(), name)
			if promptErr != nil {
				return app.fail(fmt.Errorf("%s plugin %q: %w", store.Scope(), name, promptErr))
			}
			if !overwrite {
				style := terminal.New(app.Error)
				fmt.Fprintln(app.Error, style.Warning("Cancelled; plugin "+name+" was not overwritten."))
				return 0
			}
			destination, err = store.Replace(source)
		}
		if err != nil {
			return app.fail(fmt.Errorf("install plugin: %w", err))
		}
		name := strings.TrimSuffix(filepath.Base(destination), filepath.Ext(destination))
		style := terminal.New(app.Output)
		fmt.Fprintf(app.Output, "%s %s\n", style.Stage("Installed "+string(store.Scope())+" plugin"), style.Identifier(name))
		fmt.Fprintln(app.Output, style.Detail("Path: "+destination))
		if workspace != "" {
			fmt.Fprintln(app.Output, style.Detail("Workspace: "+workspace))
		}
		return 0
	case "--list":
		global, remaining := consumePluginGlobal(remaining)
		if len(remaining) != 0 {
			return app.fail(fmt.Errorf("plugins --list does not accept arguments or another action"))
		}
		globalStore, err := plugins.GlobalStore()
		if err != nil {
			return app.fail(err)
		}
		if global {
			if err := printPluginGroup(app.Output, globalStore); err != nil {
				return app.fail(err)
			}
			return 0
		}
		workspace, err := config.FindWorkspace(app.Cwd)
		if err != nil {
			return app.fail(fmt.Errorf("list workspace plugins: %w; use conven plugins --list --global to list only global plugins", err))
		}
		workspaceStore, err := plugins.WorkspaceStore(workspace)
		if err != nil {
			return app.fail(err)
		}
		if err := printPluginGroups(app.Output, workspaceStore, globalStore); err != nil {
			return app.fail(err)
		}
		return 0
	case "--remove":
		global, remaining := consumePluginGlobal(remaining)
		if len(remaining) != 1 {
			return app.fail(fmt.Errorf("plugins --remove requires exactly one plugin name"))
		}
		store, workspace, err := app.pluginStore(global)
		if err != nil {
			return app.fail(err)
		}
		path, err := store.Remove(remaining[0])
		if err != nil {
			return app.fail(fmt.Errorf("remove plugin: %w", err))
		}
		name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		style := terminal.New(app.Output)
		fmt.Fprintf(app.Output, "%s %s\n", style.Stage("Removed "+string(store.Scope())+" plugin"), style.Identifier(name))
		fmt.Fprintln(app.Output, style.Detail("Path: "+path))
		if workspace != "" {
			fmt.Fprintln(app.Output, style.Detail("Workspace: "+workspace))
		}
		return 0
	case "--run":
		global, remaining := consumePluginGlobal(remaining)
		workspace, err := config.FindWorkspace(app.Cwd)
		if err != nil {
			return app.fail(err)
		}
		return app.runPlugin(workspace, global, remaining)
	default:
		style := terminal.New(app.Error)
		fmt.Fprintln(app.Error, style.Failure(fmt.Sprintf("conven: unknown plugins action %q", action)))
		app.printPluginsUsage(app.Error)
		return 2
	}
}

func (app App) printPluginsUsage(output io.Writer) {
	fmt.Fprint(output, `usage:
  conven plugins --install [--global] PYTHON_FILE
  conven plugins --list [--global]
  conven plugins --remove [--global] NAME
  conven plugins --run [--global] [NAME] [plugin args...]

Without --global, install and remove use <workspace>/.conven/plugins. --list
shows workspace and global plugins in separate groups; --list --global shows
only ~/.conven/plugins. Workspace and global plugins may have the same name.

An omitted run NAME executes the sole workspace plugin with a warning. Zero or
multiple workspace plugins stop with a grouped candidate list. An explicit NAME
prefers the workspace plugin and falls back to a global plugin with a warning.
Place --global immediately after the action to force the global scope. Relative
install paths use the effective working directory. Arguments after NAME, or all
arguments beginning with an option when NAME is omitted, pass unchanged to the
plugin; --workspace is reserved by Conven. Compatible policy generators accept
--output [FILE] (no FILE means <workspace>/application.yaml) and
--disable-bindings BINDING...; the plugin owns output overwrite confirmation.
`)
}

func consumePluginGlobal(arguments []string) (bool, []string) {
	if len(arguments) > 0 && arguments[0] == "--global" {
		return true, arguments[1:]
	}
	return false, arguments
}

func (app App) pluginStore(global bool) (plugins.Store, string, error) {
	if global {
		store, err := plugins.GlobalStore()
		return store, "", err
	}
	workspace, err := config.FindWorkspace(app.Cwd)
	if err != nil {
		return plugins.Store{}, "", err
	}
	store, err := plugins.WorkspaceStore(workspace)
	if err != nil {
		return plugins.Store{}, "", err
	}
	workspace = filepath.Dir(filepath.Dir(store.Directory()))
	return store, workspace, nil
}

func (app App) runPlugin(workspace string, globalOnly bool, arguments []string) int {
	workspaceStore, err := plugins.WorkspaceStore(workspace)
	if err != nil {
		return app.fail(err)
	}
	globalStore, err := plugins.GlobalStore()
	if err != nil {
		return app.fail(err)
	}
	name, pluginArguments := pluginRunArguments(arguments)
	if globalOnly {
		return app.runGlobalPlugin(workspace, globalStore, name, pluginArguments)
	}
	if name == "" {
		names, err := workspaceStore.List()
		if err != nil {
			return app.fail(err)
		}
		if len(names) != 1 {
			if err := printPluginGroups(app.Error, workspaceStore, globalStore); err != nil {
				return app.fail(err)
			}
			if len(names) == 0 {
				return app.fail(errors.New("no workspace plugin is installed; install one in this workspace or specify --global NAME"))
			}
			return app.fail(errors.New("plugin name is required because this workspace has multiple plugins"))
		}
		name = names[0]
		style := terminal.New(app.Error)
		fmt.Fprintln(app.Error, style.Warning("No plugin name specified; running workspace plugin "+name+"."))
		if err := workspaceStore.Run(app.Context, name, workspace, pluginArguments, app.Input, app.Output, app.Error); err != nil {
			return app.fail(err)
		}
		return 0
	}
	if err := workspaceStore.Run(app.Context, name, workspace, pluginArguments, app.Input, app.Output, app.Error); err == nil {
		return 0
	} else if !errors.Is(err, plugins.ErrNotInstalled) {
		return app.fail(err)
	}
	globalNames, err := globalStore.List()
	if err != nil {
		return app.fail(err)
	}
	if pluginNameListed(globalNames, name) {
		style := terminal.New(app.Error)
		fmt.Fprintln(app.Error, style.Warning("Workspace plugin "+name+" is not installed; running global plugin "+name+"."))
	}
	if err := globalStore.Run(app.Context, name, workspace, pluginArguments, app.Input, app.Output, app.Error); err != nil {
		if errors.Is(err, plugins.ErrNotInstalled) {
			if listErr := printPluginGroups(app.Error, workspaceStore, globalStore); listErr != nil {
				return app.fail(listErr)
			}
		}
		return app.fail(err)
	}
	return 0
}

func (app App) runGlobalPlugin(workspace string, store plugins.Store, name string, arguments []string) int {
	names, err := store.List()
	if err != nil {
		return app.fail(err)
	}
	if name == "" {
		if len(names) != 1 {
			if err := printPluginGroup(app.Error, store); err != nil {
				return app.fail(err)
			}
			return app.fail(errors.New("global plugin name is required unless exactly one global plugin is installed"))
		}
		name = names[0]
	}
	if pluginNameListed(names, name) {
		style := terminal.New(app.Error)
		fmt.Fprintln(app.Error, style.Warning("Running global plugin "+name+"."))
	}
	if err := store.Run(app.Context, name, workspace, arguments, app.Input, app.Output, app.Error); err != nil {
		return app.fail(err)
	}
	return 0
}

func pluginRunArguments(arguments []string) (string, []string) {
	if len(arguments) == 0 {
		return "", nil
	}
	if arguments[0] == "--" {
		return "", arguments[1:]
	}
	if strings.HasPrefix(arguments[0], "-") {
		return "", arguments
	}
	return arguments[0], arguments[1:]
}

func pluginNameListed(names []string, target string) bool {
	if strings.Count(target, ".py") == 1 && strings.HasSuffix(target, ".py") {
		target = strings.TrimSuffix(target, ".py")
	}
	for _, name := range names {
		if name == target {
			return true
		}
	}
	return false
}

func printPluginGroups(output io.Writer, workspace plugins.Store, global plugins.Store) error {
	if err := printPluginGroup(output, workspace); err != nil {
		return err
	}
	return printPluginGroup(output, global)
}

func printPluginGroup(output io.Writer, store plugins.Store) error {
	names, err := store.List()
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "%s plugins (%s):\n", pluginScopeTitle(store.Scope()), store.Directory())
	if len(names) == 0 {
		fmt.Fprintln(output, "  (none)")
		return nil
	}
	for _, name := range names {
		fmt.Fprintln(output, "  "+name)
	}
	return nil
}

func pluginScopeTitle(scope plugins.Scope) string {
	if scope == plugins.WorkspaceScope {
		return "Workspace"
	}
	return "Global"
}

func confirmPluginOverwrite(ctx context.Context, input *os.File, output io.Writer, scope plugins.Scope, name string) (bool, error) {
	removeCommand := "conven plugins --remove " + name
	if scope == plugins.GlobalScope {
		removeCommand = "conven plugins --remove --global " + name
	}
	if input == nil || !term.IsTerminal(int(input.Fd())) {
		return false, fmt.Errorf("plugin %q is already installed; overwrite confirmation requires an interactive terminal; existing plugin unchanged; use %s first", name, removeCommand)
	}
	outputFile, ok := output.(*os.File)
	if !ok || !term.IsTerminal(int(outputFile.Fd())) {
		return false, fmt.Errorf("plugin %q is already installed; overwrite confirmation requires an interactive terminal; existing plugin unchanged; use %s first", name, removeCommand)
	}
	return askPluginOverwriteContext(ctx, input, output, name)
}

func askPluginOverwrite(input io.Reader, output io.Writer, name string) (bool, error) {
	if err := writePluginOverwritePrompt(output, name); err != nil {
		return false, fmt.Errorf("write plugin overwrite prompt: %w", err)
	}
	answer, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("read plugin overwrite confirmation: %w", err)
	}
	return pluginOverwriteAnswer(answer), nil
}

func askPluginOverwriteContext(ctx context.Context, input *os.File, output io.Writer, name string) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := writePluginOverwritePrompt(output, name); err != nil {
		return false, fmt.Errorf("write plugin overwrite prompt: %w", err)
	}
	reader := bufio.NewReader(input)
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		ready, err := waitForConfirmationInput(reader, int(input.Fd()), 100*time.Millisecond)
		if err != nil {
			return false, fmt.Errorf("wait for plugin overwrite confirmation: %w", err)
		}
		if ready {
			break
		}
	}
	answer, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("read plugin overwrite confirmation: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return pluginOverwriteAnswer(answer), nil
}

func waitForConfirmationInput(reader *bufio.Reader, fd int, timeout time.Duration) (bool, error) {
	if reader.Buffered() > 0 {
		return true, nil
	}
	if fd < 0 {
		return false, nil
	}
	descriptors := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for {
		count, err := unix.Poll(descriptors, int(timeout/time.Millisecond))
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return false, err
		}
		if count == 0 {
			return false, nil
		}
		if descriptors[0].Revents&unix.POLLNVAL != 0 {
			return false, errors.New("confirmation terminal input descriptor is invalid")
		}
		return descriptors[0].Revents&(unix.POLLIN|unix.POLLHUP) != 0, nil
	}
}

func writePluginOverwritePrompt(output io.Writer, name string) error {
	_, err := fmt.Fprintf(output, "Plugin %s is already installed. Overwrite? [y/N]: ", name)
	return err
}

func pluginOverwriteAnswer(answer string) bool {
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
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
