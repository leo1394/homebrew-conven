package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/leo1394/homebrew-loom/internal/config"
	loomruntime "github.com/leo1394/homebrew-loom/internal/runtime"
	"github.com/leo1394/homebrew-loom/internal/selector"
)

type App struct {
	Input        *os.File
	Output       io.Writer
	Error        io.Writer
	Context      context.Context
	Cwd          string
	Version      string
	PolicyEditor func(context.Context, string) error
}

type commonFlags struct {
	environment string
	dev         bool
	test        bool
	kubeconfig  string
	context     string
	namespace   string
}

func (app App) Run(arguments []string) int {
	app = app.withDefaults()
	if len(arguments) == 0 {
		app.printUsage(app.Error)
		return 2
	}
	switch arguments[0] {
	case "-h", "--help", "help":
		app.printUsage(app.Output)
		return 0
	case "-v", "--version", "version":
		fmt.Fprintf(app.Output, "loom %s\n", app.Version)
		return 0
	case "init":
		return app.runInit(arguments[1:])
	case "config":
		return app.runConfig(arguments[1:])
	case "policy":
		return app.runPolicy(arguments[1:])
	case "services":
		return app.runServices(arguments[1:])
	case "doctor":
		return app.runDoctor(arguments[1:])
	case "__completion":
		return app.runCompletion(arguments[1:])
	default:
		fmt.Fprintf(app.Error, "loom: unknown command %q\n", arguments[0])
		app.printUsage(app.Error)
		return 2
	}
}

func (app App) runServices(arguments []string) int {
	if len(arguments) == 0 {
		app.printServicesUsage(app.Error)
		return 2
	}
	action := arguments[0]
	remaining := arguments[1:]
	switch action {
	case "-h", "--help", "help":
		app.printServicesUsage(app.Output)
		return 0
	case "--list":
		return app.runList(remaining)
	case "--registry":
		return app.runDiscover(remaining)
	case "--status":
		return app.runStatus(remaining)
	case "--logs":
		return app.runLogs(remaining)
	case "--start":
		return app.runStart(remaining)
	case "--restart":
		return app.runRestart(remaining)
	case "--stop":
		return app.runStop(remaining)
	case "--stop-all":
		return app.runStop(append([]string{"--all"}, remaining...))
	default:
		fmt.Fprintf(app.Error, "loom: unknown services action %q\n", action)
		app.printServicesUsage(app.Error)
		return 2
	}
}

func (app App) runStart(arguments []string) int {
	flags := flag.NewFlagSet("services --start", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	common := bindCommonFlags(flags, true)
	dryRun := flags.Bool("dry-run", false, "show the resolved plan without changing state")
	tail := flags.Bool("tail", false, "tail aggregated logs after startup")
	skipBuild := flags.Bool("skip-build", false, "skip build; artifacts under current runtime cannot be reused after a fresh start")
	skipVerify := flags.Bool("skip-verify", false, "skip service health checks")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage:\n  loom services --start [flags] [service...]")
		flags.PrintDefaults()
	}
	if ok, code := parseCommandFlags(flags, arguments, app.Output); !ok {
		return code
	}
	if err := common.resolveEnvironment(flags, arguments); err != nil {
		return app.fail(err)
	}
	options := common.options(app.Cwd)
	workspace, err := loomruntime.OpenWorkspace(options)
	if err != nil {
		return app.fail(err)
	}
	services := flags.Args()
	if len(services) == 0 {
		candidates := make([]selector.Candidate, 0, len(workspace.Manifest.Services))
		for _, name := range config.ServiceNames(workspace.Manifest) {
			service := workspace.Manifest.Services[name]
			detail := service.Runner.Workdir
			if detail == "" {
				detail = "repository root"
			}
			candidates = append(candidates, selector.Candidate{
				Name:   name,
				Path:   service.Path,
				Detail: detail,
			})
		}
		selected, confirmed, err := selector.Select(app.Context, app.Input, app.Output, candidates)
		if err != nil {
			if errors.Is(err, selector.ErrNotTerminal) {
				return app.fail(errors.New("no services were specified and interactive selection requires a terminal; pass service names explicitly"))
			}
			return app.fail(err)
		}
		if !confirmed {
			fmt.Fprintln(app.Output, "Cancelled; no services were started.")
			return 0
		}
		services = selected
	}
	session, err := loomruntime.Start(app.Context, workspace, loomruntime.StartOptions{
		Common:     options,
		Services:   services,
		DryRun:     *dryRun,
		SkipBuild:  *skipBuild,
		SkipVerify: *skipVerify,
		Output:     app.Output,
	})
	if err != nil {
		return app.fail(err)
	}
	if *tail && session != nil {
		if err := loomruntime.TailLogs(app.Context, workspace, session, nil, app.Input, app.Output); err != nil {
			return app.fail(err)
		}
	}
	return 0
}

func (app App) runStatus(arguments []string) int {
	flags := flag.NewFlagSet("services --status", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	common := bindCommonFlags(flags, false)
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage:\n  loom services --status")
		flags.PrintDefaults()
	}
	if ok, code := parseCommandFlags(flags, arguments, app.Output); !ok {
		return code
	}
	if len(flags.Args()) != 0 {
		return app.fail(errors.New("services --status does not accept service arguments"))
	}
	workspace, err := loomruntime.OpenWorkspace(common.options(app.Cwd))
	if err != nil {
		return app.fail(err)
	}
	if err := loomruntime.Status(app.Context, workspace, app.Output); err != nil {
		return app.fail(err)
	}
	return 0
}

func (app App) runStop(arguments []string) int {
	flags := flag.NewFlagSet("services --stop", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	common := bindCommonFlags(flags, false)
	all := flags.Bool("all", false, "stop every service in the current session")
	force := flags.Bool("force", false, "bypass identity checks and recover saved process groups")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage:\n  loom services --stop [--force] (<service...>|--all)\n  loom services --stop-all [--force]")
		flags.PrintDefaults()
		fmt.Fprintln(flags.Output(), "\n--force is destructive: verify the saved PGID with `loom services --status` before using it.")
		fmt.Fprintln(flags.Output(), "With no workspace session, --all --force recovers unleased shared connection records.")
	}
	if ok, code := parseCommandFlags(flags, arguments, app.Output); !ok {
		return code
	}
	if *all && len(flags.Args()) > 0 {
		return app.fail(errors.New("services --stop --all cannot be combined with service names"))
	}
	workspace, err := loomruntime.OpenWorkspace(common.options(app.Cwd))
	if err != nil {
		return app.fail(err)
	}
	if err := loomruntime.Stop(app.Context, workspace, flags.Args(), *all, *force, app.Output); err != nil {
		return app.fail(err)
	}
	return 0
}

func (app App) runLogs(arguments []string) int {
	flags := flag.NewFlagSet("services --logs", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	common := bindCommonFlags(flags, false)
	tail := flags.Bool("tail", false, "continue tailing appended log lines")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage:\n  loom services --logs [--tail] [service...]")
		flags.PrintDefaults()
	}
	if ok, code := parseCommandFlags(flags, arguments, app.Output); !ok {
		return code
	}
	workspace, err := loomruntime.OpenWorkspace(common.options(app.Cwd))
	if err != nil {
		return app.fail(err)
	}
	session, err := workspace.Store.Load()
	if err != nil {
		return app.fail(err)
	}
	if *tail {
		if err := loomruntime.TailLogs(app.Context, workspace, session, flags.Args(), app.Input, app.Output); err != nil {
			return app.fail(err)
		}
		return 0
	}
	if err := loomruntime.ShowLogs(app.Context, session, flags.Args(), false, app.Output); err != nil {
		return app.fail(err)
	}
	return 0
}

func (app App) runDoctor(arguments []string) int {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	common := bindCommonFlags(flags, true)
	if ok, code := parseCommandFlags(flags, arguments, app.Output); !ok {
		return code
	}
	if err := common.resolveEnvironment(flags, arguments); err != nil {
		return app.fail(err)
	}
	if len(flags.Args()) != 0 {
		return app.fail(errors.New("doctor does not accept service arguments"))
	}
	options := common.options(app.Cwd)
	workspace, err := loomruntime.OpenWorkspace(options)
	if err != nil {
		return app.fail(err)
	}
	if err := loomruntime.Doctor(workspace, options, app.Output); err != nil {
		return app.fail(err)
	}
	return 0
}

func (app App) runList(arguments []string) int {
	flags := flag.NewFlagSet("services --list", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	common := bindCommonFlags(flags, false)
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage:\n  loom services --list")
		flags.PrintDefaults()
	}
	if ok, code := parseCommandFlags(flags, arguments, app.Output); !ok {
		return code
	}
	if len(flags.Args()) != 0 {
		return app.fail(errors.New("services --list does not accept service arguments"))
	}
	workspace, err := loomruntime.OpenWorkspace(common.options(app.Cwd))
	if err != nil {
		return app.fail(err)
	}
	loomruntime.ListServices(workspace, app.Output)
	return 0
}

func (app App) runCompletion(arguments []string) int {
	if len(arguments) != 1 {
		return app.fail(errors.New("__completion requires bash, zsh, or fish"))
	}
	completion, err := Completion(arguments[0])
	if err != nil {
		return app.fail(err)
	}
	fmt.Fprint(app.Output, completion)
	return 0
}

func bindCommonFlags(flags *flag.FlagSet, includeEnvironment bool) *commonFlags {
	common := &commonFlags{}
	if includeEnvironment {
		flags.StringVar(&common.environment, "env", "dev", "environment profile name")
		flags.BoolVar(&common.dev, "dev", false, "use the dev environment profile (equivalent to --env dev)")
		flags.BoolVar(&common.test, "test", false, "use the test environment profile (equivalent to --env test)")
		flags.StringVar(&common.kubeconfig, "kubeconfig", "", "kubeconfig path override")
		flags.StringVar(&common.context, "context", "", "kubeconfig context override")
		flags.StringVar(&common.namespace, "namespace", "", "Kubernetes namespace override")
	}
	return common
}

func (common *commonFlags) resolveEnvironment(flags *flag.FlagSet, arguments []string) error {
	if !common.dev && !common.test {
		return nil
	}
	if common.dev && common.test {
		return errors.New("--dev and --test cannot be used together")
	}
	shortcut := "dev"
	shortcutFlag := "--dev"
	if common.test {
		shortcut = "test"
		shortcutFlag = "--test"
	}
	for _, configured := range explicitFlagValues(flags, arguments, "env") {
		effectiveEnvironment := configured
		if strings.TrimSpace(effectiveEnvironment) == "" {
			effectiveEnvironment = "dev"
		}
		if effectiveEnvironment != shortcut {
			return fmt.Errorf("%s conflicts with --env %q", shortcutFlag, configured)
		}
	}
	common.environment = shortcut
	return nil
}

func explicitFlagValues(flags *flag.FlagSet, arguments []string, target string) []string {
	values := []string{}
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "--" {
			break
		}
		if argument == "-" || !strings.HasPrefix(argument, "-") {
			continue
		}
		name := strings.TrimLeft(argument, "-")
		if separator := strings.IndexByte(name, '='); separator >= 0 {
			if name[:separator] == target {
				values = append(values, name[separator+1:])
			}
			continue
		}
		definition := flags.Lookup(name)
		if definition == nil {
			continue
		}
		if value, ok := definition.Value.(booleanFlag); ok && value.IsBoolFlag() {
			continue
		}
		if index+1 >= len(arguments) {
			break
		}
		index++
		if name == target {
			values = append(values, arguments[index])
		}
	}
	return values
}

type booleanFlag interface {
	IsBoolFlag() bool
}

func parseCommandFlags(flags *flag.FlagSet, arguments []string, helpOutput io.Writer) (bool, int) {
	normalized, err := intersperseFlags(flags, arguments)
	if err != nil {
		fmt.Fprintln(flags.Output(), err)
		return false, 2
	}
	errorOutput := flags.Output()
	var parseOutput strings.Builder
	flags.SetOutput(&parseOutput)
	err = flags.Parse(normalized)
	flags.SetOutput(errorOutput)
	if parseOutput.Len() > 0 {
		output := errorOutput
		if errors.Is(err, flag.ErrHelp) {
			output = helpOutput
		}
		fmt.Fprint(output, parseOutput.String())
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return false, 0
		}
		return false, 2
	}
	return true, 0
}

func intersperseFlags(flags *flag.FlagSet, arguments []string) ([]string, error) {
	options := make([]string, 0, len(arguments))
	positionals := make([]string, 0, len(arguments))
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == "--" {
			positionals = append(positionals, arguments[index+1:]...)
			break
		}
		if argument == "-" || !strings.HasPrefix(argument, "-") {
			positionals = append(positionals, argument)
			continue
		}
		options = append(options, argument)
		name := strings.TrimLeft(argument, "-")
		if separator := strings.IndexByte(name, '='); separator >= 0 {
			name = name[:separator]
			continue
		}
		definition := flags.Lookup(name)
		if definition == nil {
			continue
		}
		if value, ok := definition.Value.(booleanFlag); ok && value.IsBoolFlag() {
			continue
		}
		if index+1 >= len(arguments) {
			return nil, fmt.Errorf("flag needs an argument: --%s", name)
		}
		index++
		options = append(options, arguments[index])
	}
	return append(options, positionals...), nil
}

func (common commonFlags) options(cwd string) loomruntime.CommonOptions {
	return loomruntime.CommonOptions{
		Cwd:         cwd,
		Environment: common.environment,
		Kubeconfig:  common.kubeconfig,
		Context:     common.context,
		Namespace:   common.namespace,
	}
}

func (app App) fail(err error) int {
	message := strings.TrimSpace(err.Error())
	fmt.Fprintf(app.Error, "loom: %s\n", message)
	return 1
}

func (app App) withDefaults() App {
	if app.Input == nil {
		app.Input = os.Stdin
	}
	if app.Output == nil {
		app.Output = os.Stdout
	}
	if app.Error == nil {
		app.Error = os.Stderr
	}
	if app.Context == nil {
		app.Context = context.Background()
	}
	if app.Version == "" {
		app.Version = "dev"
	}
	if app.PolicyEditor == nil {
		input := app.Input
		output := app.Output
		errorOutput := app.Error
		app.PolicyEditor = func(ctx context.Context, path string) error {
			return launchPolicyEditor(ctx, input, output, errorOutput, path)
		}
	}
	return app
}

func (app App) printUsage(output io.Writer) {
	fmt.Fprint(output, `usage:
  loom init
  loom config [--global] [--list|--unset] [key] [value]
  loom policy ACTION
  loom services ACTION [flags] [service...]
  loom doctor [flags]
  loom --version

Run "loom policy --help" for policy actions,
"loom services --help" for service actions, and
"loom services --start --help" for startup flags. Without service arguments,
loom opens an interactive PathPicker-style selector and requires confirmation.
Run "loom services --restart" without service arguments to restart only changed
services from the current session.
`)
}

func (app App) printServicesUsage(output io.Writer) {
	fmt.Fprint(output, `usage:
  loom services --list
  loom services --registry [--prune]
  loom services --status
  loom services --logs [--tail] [service...]
  loom services --start [flags] [service...]
  loom services --restart [flags] [service...]
  loom services --stop [--force] (<service...>|--all)
  loom services --stop-all [--force]

The action flag must be the first argument after "loom services".
Run "loom services --ACTION --help" for action-specific flags.
`)
}
