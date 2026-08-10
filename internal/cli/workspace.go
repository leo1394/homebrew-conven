package cli

import (
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/leo1394/homebrew-loom/examples"
	"github.com/leo1394/homebrew-loom/internal/config"
	loomruntime "github.com/leo1394/homebrew-loom/internal/runtime"
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
	result, err := config.InitWorkspaceDetails(app.Cwd, examples.ApplicationYAML)
	if err != nil {
		return app.fail(err)
	}
	if result.Created {
		fmt.Fprintf(app.Output, "Initialized Loom workspace in %s\n", result.Path)
		if len(result.Discovered) > 0 {
			fmt.Fprintf(app.Output, "Discovered supported services: %s\n", strings.Join(result.Discovered, ", "))
		} else if result.UsedExample {
			fmt.Fprintln(app.Output, "No supported child repositories were detected; wrote the embedded example manifest.")
		}
		if len(result.Skipped) > 0 {
			fmt.Fprintf(app.Output, "Skipped by the built-in repository analyzers: %s\n", strings.Join(result.Skipped, ", "))
		}
	} else {
		fmt.Fprintf(app.Output, "Reinitialized existing Loom workspace in %s; manifest was not overwritten.\n", result.Path)
	}
	return 0
}

func (app App) runDiscover(arguments []string) int {
	flags := flag.NewFlagSet("services --registry", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	prune := flags.Bool("prune", false, "remove services whose direct-child repository no longer exists")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage:\n  loom services --registry [--prune]")
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
	if len(result.Discovered) == 0 {
		fmt.Fprintln(app.Output, "Discovered supported services: none")
	} else {
		fmt.Fprintf(app.Output, "Discovered supported services: %s\n", strings.Join(result.Discovered, ", "))
	}
	if len(result.Added) > 0 {
		fmt.Fprintf(app.Output, "Added services: %s\n", strings.Join(result.Added, ", "))
	}
	if len(result.Updated) > 0 {
		fmt.Fprintf(app.Output, "Backfilled discovered facts: %s\n", strings.Join(result.Updated, ", "))
	}
	if len(result.Missing) > 0 {
		fmt.Fprintf(app.Output, "Missing repositories kept in manifest: %s (use loom services --registry --prune to remove)\n", strings.Join(result.Missing, ", "))
	}
	if len(result.Pruned) > 0 {
		fmt.Fprintf(app.Output, "Pruned services: %s\n", strings.Join(result.Pruned, ", "))
	}
	if len(result.Skipped) > 0 {
		fmt.Fprintf(app.Output, "Skipped by the built-in repository analyzers: %s\n", strings.Join(result.Skipped, ", "))
	}
	if len(result.Added) == 0 && len(result.Updated) == 0 && len(result.Pruned) == 0 {
		if len(result.Missing) > 0 {
			fmt.Fprintln(app.Output, "Manifest unchanged; missing repositories were kept.")
		} else {
			fmt.Fprintln(app.Output, "Manifest already matches discovered repositories.")
		}
	} else {
		fmt.Fprintf(app.Output, "Updated Loom manifest: %s\n", manifestPath)
	}
	return 0
}

func (app App) runConfig(arguments []string) int {
	flags := flag.NewFlagSet("config", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	global := flags.Bool("global", false, "use the current user's ~/.loom/config")
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

func (app App) runRestart(arguments []string) int {
	flags := flag.NewFlagSet("services --restart", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	common := bindCommonFlags(flags, false)
	tail := flags.Bool("tail", false, "tail aggregated logs after restart")
	skipBuild := flags.Bool("skip-build", false, "skip build and reuse artifacts from the fixed current runtime")
	skipVerify := flags.Bool("skip-verify", false, "skip service health checks")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage:\n  loom services --restart [flags] [service...]")
		flags.PrintDefaults()
	}
	if ok, code := parseCommandFlags(flags, arguments, app.Output); !ok {
		return code
	}
	options := common.options(app.Cwd)
	workspace, err := loomruntime.OpenWorkspace(options)
	if err != nil {
		return app.fail(err)
	}
	session, err := loomruntime.Restart(app.Context, workspace, loomruntime.RestartOptions{
		Common:     options,
		Services:   flags.Args(),
		SkipBuild:  *skipBuild,
		SkipVerify: *skipVerify,
		Output:     app.Output,
	})
	if err != nil {
		return app.fail(err)
	}
	if *tail && session != nil {
		if err := loomruntime.TailLogs(app.Context, workspace, session, flags.Args(), app.Input, app.Output); err != nil {
			return app.fail(err)
		}
	}
	return 0
}
