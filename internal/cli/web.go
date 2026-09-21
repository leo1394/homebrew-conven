package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os/exec"
	"runtime"
	"time"

	convenruntime "github.com/leo1394/homebrew-conven/internal/runtime"
)

func (app App) terminalDashboardAvailable() bool {
	if app.TerminalDashboardAvailable != nil {
		return app.TerminalDashboardAvailable(app.Input, app.Output)
	}
	return convenruntime.DashboardAvailable(app.Input, app.Output)
}

func (app App) openTerminalDashboard(workspace *convenruntime.WorkspaceData, session *convenruntime.Session, options convenruntime.TailOptions) error {
	opener := app.TerminalDashboardOpener
	if opener == nil {
		opener = convenruntime.TailLogs
	}
	return opener(app.Context, workspace, session, options, app.Input, app.Output)
}

func (app App) runWebDashboard(arguments []string, diagnose bool) int {
	flags := flag.NewFlagSet("services --dashboard --web", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	web := flags.Bool("web", true, "open the built-in Web dashboard")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage:\n  conven services --dashboard --web [service...]\n  conven services --diagnose [service...]")
		fmt.Fprintln(flags.Output(), "\nReuses the saved session and environment; never starts services. With no session, shows the latest diagnostic attempts. Closing the browser does not stop services.")
	}
	if ok, code := parseCommandFlags(flags, arguments, app.Output); !ok {
		return code
	}
	if !*web {
		return app.fail(errors.New("use services --dashboard without --web for the terminal dashboard"))
	}
	workspace, err := convenruntime.OpenWebWorkspace(convenruntime.CommonOptions{Cwd: app.Cwd})
	if err != nil {
		return app.fail(err)
	}
	if err := app.openWebDashboard(workspace, flags.Args(), diagnose); err != nil {
		return app.fail(err)
	}
	return 0
}

func (app App) openWebDashboard(workspace *convenruntime.WorkspaceData, names []string, diagnose bool) error {
	opener := app.WebDashboardOpener
	if opener == nil {
		opener = convenruntime.OpenWebDashboard
	}
	address, err := opener(app.Context, workspace, app.Executable, names, diagnose)
	if err != nil {
		return err
	}
	fmt.Fprintln(app.Output, "Web dashboard:", address)
	fmt.Fprintln(app.Output, "The dashboard runs locally; closing it leaves services running. Keep this private access URL on this machine.")
	browser := app.BrowserOpener
	if browser == nil {
		browser = openBrowser
	}
	ctx, cancel := context.WithTimeout(app.Context, 5*time.Second)
	defer cancel()
	if err := browser(ctx, address); err != nil {
		fmt.Fprintln(app.Output, "Browser could not be opened automatically. Open the URL above in your browser.")
	}
	return nil
}

func openBrowser(ctx context.Context, address string) error {
	command := "xdg-open"
	if runtime.GOOS == "darwin" {
		command = "open"
	}
	return exec.CommandContext(ctx, command, address).Run()
}

func (app App) runWebServer(arguments []string) int {
	if len(arguments) != 0 {
		return app.fail(errors.New("invalid internal Web dashboard arguments"))
	}
	workspace, err := convenruntime.OpenWebWorkspace(convenruntime.CommonOptions{Cwd: app.Cwd})
	if err != nil {
		return app.fail(err)
	}
	if err = convenruntime.ServeWebDashboard(app.Context, workspace, app.Version, app.Executable); err != nil {
		return app.fail(err)
	}
	return 0
}
