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
	"github.com/leo1394/homebrew-conven/internal/selector"
	"github.com/leo1394/homebrew-conven/internal/terminal"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

var errPluginOverwriteRequiresTerminal = errors.New("overwrite confirmation requires an interactive terminal")

func (app App) runPlugins(arguments []string) int {
	if len(arguments) == 0 {
		app.printPluginsUsage(app.Error)
		return 2
	}
	globalBeforeRun := false
	if arguments[0] == "--global" {
		if len(arguments) == 1 || arguments[1] != "--run" {
			style := terminal.New(app.Error)
			fmt.Fprintln(app.Error, style.Failure("conven: plugins --global must be followed by --run NAME"))
			app.printPluginsUsage(app.Error)
			return 2
		}
		globalBeforeRun = true
		arguments = arguments[1:]
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
			printWarningBlock(app.Error, pluginScopeTitle(store.Scope())+" plugin already exists.", []string{
				"Plugin: " + name,
				"Path: " + pluginOutputPath(store, filepath.Join(store.Directory(), name+".py")),
			}, nil)
			overwrite, promptErr := confirmPluginOverwrite(app.Context, app.Input, app.Error, name)
			if promptErr != nil {
				if errors.Is(promptErr, errPluginOverwriteRequiresTerminal) {
					style := terminal.New(app.Error)
					fmt.Fprintln(app.Error, style.Detail("Existing plugin was not changed."))
					fmt.Fprintln(app.Error, style.Action(pluginRemoveCommand(store.Scope(), name)))
				}
				return app.fail(fmt.Errorf("%s plugin %q: %w", store.Scope(), name, promptErr))
			}
			if !overwrite {
				style := terminal.New(app.Error)
				fmt.Fprintln(app.Error, style.Detail("Existing plugin was not overwritten."))
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
		fmt.Fprintln(app.Output, style.Detail("Path: "+pluginOutputPath(store, destination)))
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
		fmt.Fprintln(app.Output, style.Detail("Path: "+pluginOutputPath(store, path)))
		if workspace != "" {
			fmt.Fprintln(app.Output, style.Detail("Workspace: "+workspace))
		}
		return 0
	case "--run":
		global, remaining := consumePluginGlobal(remaining)
		global = global || globalBeforeRun
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
  conven plugins --run [NAME] [plugin args...]
  conven plugins --global --run NAME [plugin args...]

Without --global, install and remove use <workspace>/.conven/plugins. --list
shows workspace and global plugins in separate groups; --list --global shows
only ~/.conven/plugins. Workspace and global plugins may have the same name.

An omitted run NAME executes the sole workspace plugin with a warning. Multiple
workspace plugins open a single-choice selector. When no workspace plugin is
installed, installed global plugins open the selector and are marked global.
An explicit NAME prefers the workspace plugin and falls back to a global plugin
with a warning. Explicit --global requires NAME; the older
--run --global NAME order is also accepted. Relative install paths use the
effective working directory.
Arguments after NAME pass unchanged. When NAME is omitted, option arguments
pass unchanged except for an immediate --global, which selects global scope;
use -- before --global to pass it to the plugin. Conven injects its own
--workspace argument first, and plugin arguments may not set that reserved
option. Compatible policy generators accept
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
		if len(names) == 1 {
			name = names[0]
			printWarningBlock(app.Error, "Plugin name omitted; selected the only workspace plugin.", []string{
				"Plugin: " + name,
			}, nil)
			if err := workspaceStore.Run(app.Context, name, workspace, pluginArguments, app.Input, app.Output, app.Error); err != nil {
				return app.fail(err)
			}
			return 0
		}
		store := workspaceStore
		prompt := selector.Prompt{
			Title:                "Select a workspace plugin",
			ConfirmationLabel:    "Running plugin",
			EmptySelectionNotice: "Select one plugin before confirming.",
		}
		if len(names) == 0 {
			names, err = globalStore.List()
			if err != nil {
				return app.fail(err)
			}
			if len(names) == 0 {
				return app.fail(errors.New("no workspace or global plugin is installed"))
			}
			store = globalStore
			prompt.Title = "Select a global plugin"
		}
		candidates := pluginSelectorCandidates(store, names)
		selected, confirmed, err := app.SingleSelector(app.Context, app.Input, app.Output, prompt, candidates)
		if err != nil {
			if errors.Is(err, selector.ErrNotTerminal) {
				return app.fail(errors.New("plugin selection requires an interactive terminal; specify a plugin name explicitly"))
			}
			return app.fail(err)
		}
		if !confirmed {
			style := terminal.New(app.Output)
			fmt.Fprintln(app.Output, style.Stage("Plugin run cancelled"))
			fmt.Fprintln(app.Output, style.Detail("No plugin was run."))
			return 0
		}
		name = selected.Name
		if store.Scope() == plugins.GlobalScope {
			printWarningBlock(app.Error, "Running a global plugin.", []string{
				"Plugin: " + pluginDisplayName(name),
			}, nil)
		}
		if err := store.Run(app.Context, name, workspace, pluginArguments, app.Input, app.Output, app.Error); err != nil {
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
		printWarningBlock(app.Error, "Workspace plugin not found; using the global plugin.", []string{
			"Plugin: " + pluginDisplayName(name),
		}, nil)
	}
	if err := globalStore.Run(app.Context, name, workspace, pluginArguments, app.Input, app.Output, app.Error); err != nil {
		if errors.Is(err, plugins.ErrNotInstalled) {
			code := app.fail(err)
			if listErr := printPluginGroups(app.Error, workspaceStore, globalStore); listErr != nil {
				return app.fail(listErr)
			}
			return code
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
		code := app.fail(errors.New("plugins --global --run requires a plugin name"))
		if err := printPluginGroup(app.Error, store); err != nil {
			return app.fail(err)
		}
		return code
	} else if pluginNameListed(names, name) {
		printWarningBlock(app.Error, "Running a global plugin.", []string{
			"Plugin: " + pluginDisplayName(name),
		}, nil)
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

func pluginDisplayName(name string) string {
	return strings.TrimSuffix(name, ".py")
}

func pluginOutputPath(store plugins.Store, path string) string {
	if store.Scope() == plugins.WorkspaceScope {
		if relative, err := filepath.Rel(filepath.Dir(store.Directory()), path); err == nil {
			return relative
		}
	}
	return path
}

func pluginSelectorCandidates(store plugins.Store, names []string) []selector.Candidate {
	candidates := make([]selector.Candidate, 0, len(names))
	for _, name := range names {
		candidate := selector.Candidate{Name: name}
		if store.Scope() == plugins.GlobalScope {
			candidate.Tag = "global"
		}
		candidates = append(candidates, candidate)
	}
	return candidates
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
	style := terminal.New(output)
	fmt.Fprintln(output, style.Stage(pluginScopeTitle(store.Scope())+" plugins"))
	if len(names) == 0 {
		fmt.Fprintln(output, style.Detail("(none)"))
		return nil
	}
	for _, name := range names {
		fmt.Fprintln(output, style.Detail(name))
	}
	return nil
}

func pluginScopeTitle(scope plugins.Scope) string {
	if scope == plugins.WorkspaceScope {
		return "Workspace"
	}
	return "Global"
}

func confirmPluginOverwrite(ctx context.Context, input *os.File, output io.Writer, name string) (bool, error) {
	if input == nil || !term.IsTerminal(int(input.Fd())) {
		return false, errPluginOverwriteRequiresTerminal
	}
	outputFile, ok := output.(*os.File)
	if !ok || !term.IsTerminal(int(outputFile.Fd())) {
		return false, errPluginOverwriteRequiresTerminal
	}
	return askPluginOverwriteContext(ctx, input, output, name)
}

func pluginRemoveCommand(scope plugins.Scope, name string) string {
	if scope == plugins.GlobalScope {
		return "conven plugins --remove --global " + name
	}
	return "conven plugins --remove " + name
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
	_, err := fmt.Fprint(output, terminal.New(output).Action("Overwrite plugin "+name+"? [y/N]: "))
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
