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
		if len(remaining) != 1 {
			return app.fail(fmt.Errorf("plugins --install requires exactly one Python file"))
		}
		source, err := pluginSourcePath(app.Cwd, remaining[0])
		if err != nil {
			return app.fail(err)
		}
		destination, err := plugins.Install(source)
		if errors.Is(err, plugins.ErrAlreadyInstalled) {
			name := strings.TrimSuffix(filepath.Base(source), filepath.Ext(source))
			overwrite, promptErr := confirmPluginOverwrite(app.Context, app.Input, app.Error, name)
			if promptErr != nil {
				return app.fail(promptErr)
			}
			if !overwrite {
				style := terminal.New(app.Error)
				fmt.Fprintln(app.Error, style.Warning("Cancelled; plugin "+name+" was not overwritten."))
				return 0
			}
			destination, err = plugins.Replace(source)
		}
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
	case "--remove":
		if len(remaining) != 1 {
			return app.fail(fmt.Errorf("plugins --remove requires exactly one plugin name"))
		}
		path, err := plugins.Remove(remaining[0])
		if err != nil {
			return app.fail(fmt.Errorf("remove plugin: %w", err))
		}
		name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		style := terminal.New(app.Output)
		fmt.Fprintf(app.Output, "%s %s\n", style.Stage("Removed plugin"), style.Identifier(name))
		fmt.Fprintln(app.Output, style.Detail("Path: "+path))
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
  conven plugins --remove NAME
  conven plugins --run NAME [plugin args...]

--install copies exactly one Python file into ~/.conven/plugins. Relative source
paths are resolved from the current working directory. If the name already
exists, an interactive terminal asks whether to overwrite it; non-interactive
installs fail without changing the existing plugin. --remove deletes exactly one
installed plugin. Arguments after NAME are passed unchanged to the selected
plugin; --workspace is reserved by Conven.
`)
}

func confirmPluginOverwrite(ctx context.Context, input *os.File, output io.Writer, name string) (bool, error) {
	if input == nil || !term.IsTerminal(int(input.Fd())) {
		return false, fmt.Errorf("plugin %q is already installed; overwrite confirmation requires an interactive terminal; existing plugin unchanged; use conven plugins --remove %s first", name, name)
	}
	outputFile, ok := output.(*os.File)
	if !ok || !term.IsTerminal(int(outputFile.Fd())) {
		return false, fmt.Errorf("plugin %q is already installed; overwrite confirmation requires an interactive terminal; existing plugin unchanged; use conven plugins --remove %s first", name, name)
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
