package cli

import (
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/leo1394/homebrew-conven/examples"
	"github.com/leo1394/homebrew-conven/internal/config"
	"github.com/leo1394/homebrew-conven/internal/plugins"
	convenruntime "github.com/leo1394/homebrew-conven/internal/runtime"
	"github.com/leo1394/homebrew-conven/internal/terminal"
)

func (app App) runInit(arguments []string) int {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	if ok, code := parseCommandFlags(flags, arguments, app.Output); !ok {
		return code
	}
	if len(flags.Args()) != 0 {
		return app.fail(errors.New("init does not accept arguments"))
	}
	result, err := config.InitWorkspaceDetailsWithPolicySpecification(app.Cwd, examples.ApplicationYAML, workspacePolicySpecification())
	if err != nil {
		return app.fail(err)
	}
	style := terminal.New(app.Output)
	printWorkspaceFiles := func() {
		for _, file := range result.Files {
			if file.Created {
				fmt.Fprintln(app.Output, style.Detail(style.Success(file.Name)))
				continue
			}
			fmt.Fprintln(app.Output, style.Detail(file.Name+" "+style.Failure("Skipped")))
		}
	}
	if result.Created {
		fmt.Fprintln(app.Output, style.Stage("Initialized Conven workspace"))
		fmt.Fprintln(app.Output, style.Detail("Manifest: "+style.Identifier(result.Path)))
		printWorkspaceFiles()
		fmt.Fprintln(app.Output, style.Stage("Initial service registry scan complete"))
		if len(result.Discovered) > 0 {
			fmt.Fprintln(app.Output, style.Detail("Discovered services: "+style.Identifiers(result.Discovered, ", ")))
		} else {
			fmt.Fprintln(app.Output, style.Detail("Discovered services: none"))
		}
		if result.UsedExample {
			details := []string{"Manifest source: embedded example"}
			if len(result.Skipped) > 0 {
				details = append(details, "Skipped repositories: "+strings.Join(result.Skipped, ", "))
			}
			printWarningBlock(app.Error, "No supported child repositories were detected.", details, nil)
		} else if len(result.Skipped) > 0 {
			printWarningBlock(app.Error, "Some direct-child repositories were skipped.", []string{
				"Repositories: " + strings.Join(result.Skipped, ", "),
			}, nil)
		}
	} else {
		fmt.Fprintln(app.Output, style.Stage("Reused Conven workspace"))
		fmt.Fprintln(app.Output, style.Detail("Manifest: "+style.Identifier(result.Path)))
		printWorkspaceFiles()
		fmt.Fprintln(app.Output, style.Detail("Existing manifest was not overwritten."))
	}
	if err := plugins.InstallBuiltins(); err != nil {
		return app.fail(fmt.Errorf("install built-in plugins: %w", err))
	}
	return 0
}

func (app App) runDiscover(arguments []string) int {
	flags := flag.NewFlagSet("services --registry", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	prune := flags.Bool("prune", false, "remove services whose direct-child repository no longer exists")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage:\n  conven services --registry [--prune]")
		flags.PrintDefaults()
		fmt.Fprintln(flags.Output(), "\nWithout --prune, manual service configuration is preserved; new services are added and empty discovered facts may be backfilled.")
	}
	if ok, code := parseCommandFlags(flags, arguments, app.Output); !ok {
		return code
	}
	if len(flags.Args()) != 0 {
		return app.fail(errors.New("services --registry does not accept service arguments"))
	}
	manifestPath, workspace, err := config.ResolvePath(app.Cwd)
	if err != nil {
		return app.fail(err)
	}
	result, err := config.DiscoverWorkspace(manifestPath, workspace, *prune)
	if err != nil {
		return app.fail(err)
	}
	style := terminal.New(app.Output)
	fmt.Fprintln(app.Output, style.Stage("Service registry scan complete"))
	if len(result.Discovered) == 0 {
		fmt.Fprintln(app.Output, style.Detail("Discovered services: none"))
	} else {
		fmt.Fprintln(app.Output, style.Detail("Discovered services: "+style.Identifiers(result.Discovered, ", ")))
	}
	if len(result.Added) > 0 {
		fmt.Fprintln(app.Output, style.Detail("Added services: "+style.Identifiers(result.Added, ", ")))
	}
	if len(result.Updated) > 0 {
		fmt.Fprintln(app.Output, style.Detail("Backfilled services: "+style.Identifiers(result.Updated, ", ")))
	}
	if len(result.Pruned) > 0 {
		fmt.Fprintln(app.Output, style.Detail("Pruned services: "+style.Identifiers(result.Pruned, ", ")))
	}
	if len(result.Added) == 0 && len(result.Updated) == 0 && len(result.Pruned) == 0 {
		if len(result.Missing) > 0 {
			fmt.Fprintln(app.Output, style.Detail("Manifest: unchanged; missing repositories kept"))
		} else {
			fmt.Fprintln(app.Output, style.Detail("Manifest: unchanged"))
		}
	} else {
		fmt.Fprintln(app.Output, style.Detail("Manifest: "+style.Identifier(manifestPath)))
	}
	if len(result.Missing) > 0 || len(result.Skipped) > 0 {
		details := make([]string, 0, 2)
		actions := make([]string, 0, 1)
		if len(result.Missing) > 0 {
			details = append(details, "Missing repositories kept: "+strings.Join(result.Missing, ", "))
			actions = append(actions, "conven services --registry --prune")
		}
		if len(result.Skipped) > 0 {
			details = append(details, "Skipped repositories: "+strings.Join(result.Skipped, ", "))
		}
		printWarningBlock(app.Error, "Service registry scan requires review.", details, actions)
	}
	return 0
}

func (app App) runCleanup(arguments []string) int {
	flags := flag.NewFlagSet("services --cleanup", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage:\n  conven services --cleanup")
		fmt.Fprintln(flags.Output(), "\nRemoves saved build artifacts and service logs after the workspace session has stopped. Runtime configs and the shared connection log are preserved.")
	}
	if ok, code := parseCommandFlags(flags, arguments, app.Output); !ok {
		return code
	}
	if len(flags.Args()) != 0 {
		return app.fail(errors.New("services --cleanup does not accept service arguments"))
	}
	workspace, err := config.FindWorkspace(app.Cwd)
	if err != nil {
		return app.fail(err)
	}
	store, err := convenruntime.NewStore(workspace)
	if err != nil {
		return app.fail(err)
	}
	if err := convenruntime.CleanupRuntime(store, app.Output); err != nil {
		return app.fail(err)
	}
	return 0
}

func (app App) runConfig(arguments []string) int {
	flags := flag.NewFlagSet("config", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	global := flags.Bool("global", false, "use the current user's ~/.conven/config")
	list := flags.Bool("list", false, "list configuration values")
	unset := flags.Bool("unset", false, "remove one configuration value")
	if ok, code := parseCommandFlags(flags, arguments, app.Output); !ok {
		return code
	}
	values := flags.Args()
	if *list {
		if *unset || len(values) != 0 {
			return app.fail(errors.New("config --list does not accept keys, values, or --unset"))
		}
	} else if *unset {
		if len(values) != 1 {
			return app.fail(errors.New("config --unset requires exactly one key"))
		}
	} else if len(values) != 1 && len(values) != 2 {
		return app.fail(errors.New("config requires KEY to read or KEY VALUE to write; use --list to list values"))
	}

	workspace := ""
	var err error
	if !*global {
		workspace, err = config.FindWorkspace(app.Cwd)
		if err != nil {
			return app.fail(err)
		}
	}
	if *list {
		settings, err := config.ScopeSettings(workspace, "", *global)
		if err != nil {
			return app.fail(err)
		}
		for _, key := range config.SortedSettingKeys(settings) {
			fmt.Fprintf(app.Output, "%s=%s\n", key, settings[key])
		}
		return 0
	}
	key := strings.TrimSpace(values[0])
	if *unset {
		if err := config.UnsetSetting(workspace, "", *global, key); err != nil {
			return app.fail(err)
		}
		return 0
	}
	if len(values) == 2 {
		if err := config.SetSetting(workspace, "", *global, key, values[1]); err != nil {
			return app.fail(err)
		}
		return 0
	}
	settings, err := config.ScopeSettings(workspace, "", *global)
	if err != nil {
		return app.fail(err)
	}
	value, found := settings[key]
	if !found {
		return app.fail(fmt.Errorf("config key %q is not set", key))
	}
	fmt.Fprintln(app.Output, value)
	return 0
}

func restartEnvironmentFlagHint(err error) (string, string) {
	switch err.Error() {
	case "flag provided but not defined: -test":
		return "--restart reuses the current session environment;", "switch with conven services --start --test."
	case "flag provided but not defined: -dev":
		return "--restart reuses the current session environment;", "switch with conven services --start --dev."
	case "flag provided but not defined: -env":
		return "--restart reuses the current session environment;", "switch with conven services --start --env NAME."
	default:
		return "", ""
	}
}

func (app App) runRestart(arguments []string) int {
	flags := flag.NewFlagSet("services --restart", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	common := bindCommonFlags(flags, false)
	tail := flags.Bool("tail", false, "stream plain-text logs after restart")
	dashboard := flags.Bool("dashboard", false, "open the interactive log dashboard after restart")
	skipBuild := flags.Bool("skip-build", false, "skip build and reuse artifacts from the fixed current runtime")
	skipVerify := flags.Bool("skip-verify", false, "skip service health checks")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage:\n  conven services --restart [flags] [service...]")
		flags.PrintDefaults()
		fmt.Fprintln(flags.Output(), "\nWithout a mode flag, restart opens the Dashboard on an interactive terminal. When both modes are present, the last --tail or --dashboard flag wins.")
	}
	if ok, code := parseCommandFlagsWithHint(flags, arguments, app.Output, restartEnvironmentFlagHint); !ok {
		return code
	}
	options := common.options(app.Cwd)
	workspace, err := convenruntime.OpenWorkspace(options)
	if err != nil {
		return app.fail(err)
	}
	session, err := convenruntime.Restart(app.Context, workspace, convenruntime.RestartOptions{
		Common:     options,
		Services:   flags.Args(),
		SkipBuild:  *skipBuild,
		SkipVerify: *skipVerify,
		Output:     app.Output,
	})
	if err != nil {
		return app.fail(err)
	}
	if session == nil {
		return 0
	}
	mode := resolveRestartLogDisplayMode(arguments, *tail, *dashboard, convenruntime.DashboardAvailable(app.Input, app.Output))
	if mode == logDisplayPlain {
		if err := convenruntime.ShowLogs(app.Context, session, flags.Args(), true, app.Output); err != nil {
			return app.fail(err)
		}
		return 0
	}
	if mode == logDisplayDashboard {
		if err := convenruntime.TailLogs(app.Context, workspace, session, convenruntime.TailOptions{Names: flags.Args(), Version: app.Version}, app.Input, app.Output); err != nil {
			return app.fail(err)
		}
	}
	return 0
}

func resolveRestartLogDisplayMode(arguments []string, tail bool, dashboard bool, dashboardAvailable bool) logDisplayMode {
	mode := resolveLogDisplayMode(arguments, tail, dashboard)
	if mode == logDisplaySnapshot && dashboardAvailable {
		return logDisplayDashboard
	}
	return mode
}
