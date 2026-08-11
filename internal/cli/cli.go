package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/config"
	convenruntime "github.com/leo1394/homebrew-conven/internal/runtime"
	"github.com/leo1394/homebrew-conven/internal/selector"
	"github.com/leo1394/homebrew-conven/internal/terminal"
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

type logDisplayMode int

const (
	logDisplaySnapshot logDisplayMode = iota
	logDisplayPlain
	logDisplayDashboard
)

func (app App) Run(arguments []string) int {
	app = app.withDefaults()
	if len(arguments) == 0 {
		app.printUsage(app.Error)
		return 2
	}
	switch arguments[0] {
	case "-h", "--help":
		app.printUsage(app.Output)
		return 0
	case "help":
		return app.runHelp(arguments[1:])
	case "-v", "--version", "version":
		fmt.Fprintf(app.Output, "conven %s\n", app.Version)
		return 0
	case "init":
		return app.runInit(arguments[1:])
	case "config":
		return app.runConfig(arguments[1:])
	case "policy":
		return app.runPolicy(arguments[1:])
	case "plugins":
		return app.runPlugins(arguments[1:])
	case "services":
		return app.runServices(arguments[1:])
	case "doctor":
		return app.runDoctor(arguments[1:])
	case "__completion":
		return app.runCompletion(arguments[1:])
	default:
		app.printUnknownCommand(arguments[0])
		return 2
	}
}

func (app App) runHelp(arguments []string) int {
	if len(arguments) == 0 {
		app.printUsage(app.Output)
		return 0
	}
	if len(arguments) > 1 {
		style := terminal.New(app.Error)
		fmt.Fprintln(app.Error, style.Failure("conven: help accepts at most one command"))
		app.printHelpUsage(app.Error)
		return 2
	}
	switch arguments[0] {
	case "-h", "--help", "help":
		app.printHelpUsage(app.Output)
		return 0
	case "version":
		fmt.Fprint(app.Output, "usage:\n  conven version\n  conven --version\n")
		return 0
	case "init":
		return app.runInit([]string{"--help"})
	case "config":
		return app.runConfig([]string{"--help"})
	case "policy":
		return app.runPolicy([]string{"--help"})
	case "plugins":
		return app.runPlugins([]string{"--help"})
	case "services":
		return app.runServices([]string{"--help"})
	case "doctor":
		return app.runDoctor([]string{"--help"})
	default:
		app.printUnknownCommand(arguments[0])
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
	case "--dashboard":
		return app.runDashboard(remaining)
	case "--start":
		return app.runStart(remaining)
	case "--restart":
		return app.runRestart(remaining)
	case "--stop":
		return app.runStop(remaining)
	case "--stop-all":
		return app.runStop(append([]string{"--all"}, remaining...))
	default:
		style := terminal.New(app.Error)
		fmt.Fprintln(app.Error, style.Failure(fmt.Sprintf("conven: unknown services action %q", action)))
		app.printServicesUsage(app.Error)
		return 2
	}
}

func (app App) runStart(arguments []string) int {
	flags := flag.NewFlagSet("services --start", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	common := bindCommonFlags(flags, true)
	dryRun := flags.Bool("dry-run", false, "show the resolved plan without changing state")
	tail := flags.Bool("tail", false, "stream plain-text logs after startup")
	skipBuild := flags.Bool("skip-build", false, "skip build; artifacts under current runtime cannot be reused after a fresh start")
	skipVerify := flags.Bool("skip-verify", false, "skip service health checks")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage:\n  conven services --start [flags] [service...]")
		flags.PrintDefaults()
	}
	if ok, code := parseCommandFlags(flags, arguments, app.Output); !ok {
		return code
	}
	if err := common.resolveEnvironment(flags, arguments); err != nil {
		return app.fail(err)
	}
	options := common.options(app.Cwd)
	workspace, err := convenruntime.OpenWorkspace(options)
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
	session, err := convenruntime.Start(app.Context, workspace, convenruntime.StartOptions{
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
	if session == nil {
		return 0
	}
	if *tail {
		if err := convenruntime.ShowLogs(app.Context, session, nil, true, app.Output); err != nil {
			return app.fail(err)
		}
		return 0
	}
	if convenruntime.DashboardAvailable(app.Input, app.Output) {
		if err := convenruntime.TailLogs(app.Context, workspace, session, convenruntime.TailOptions{Version: app.Version}, app.Input, app.Output); err != nil {
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
		fmt.Fprintln(flags.Output(), "Usage:\n  conven services --status")
		flags.PrintDefaults()
	}
	if ok, code := parseCommandFlags(flags, arguments, app.Output); !ok {
		return code
	}
	if len(flags.Args()) != 0 {
		return app.fail(errors.New("services --status does not accept service arguments"))
	}
	workspace, err := convenruntime.OpenWorkspace(common.options(app.Cwd))
	if err != nil {
		return app.fail(err)
	}
	if err := convenruntime.Status(app.Context, workspace, app.Output); err != nil {
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
		fmt.Fprintln(flags.Output(), "Usage:\n  conven services --stop [--force] (<service...>|--all)\n  conven services --stop-all [--force]")
		flags.PrintDefaults()
		fmt.Fprintln(flags.Output(), "\n--force is destructive: verify the saved PGID with `conven services --status` before using it.")
		fmt.Fprintln(flags.Output(), "With no workspace session, --all --force recovers unleased shared connection records.")
	}
	if ok, code := parseCommandFlags(flags, arguments, app.Output); !ok {
		return code
	}
	if *all && len(flags.Args()) > 0 {
		return app.fail(errors.New("services --stop --all cannot be combined with service names"))
	}
	workspace, err := convenruntime.OpenWorkspace(common.options(app.Cwd))
	if err != nil {
		return app.fail(err)
	}
	if err := convenruntime.Stop(app.Context, workspace, flags.Args(), *all, *force, app.Output); err != nil {
		return app.fail(err)
	}
	return 0
}

func (app App) runLogs(arguments []string) int {
	flags := flag.NewFlagSet("services --logs", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	common := bindCommonFlags(flags, false)
	tail := flags.Bool("tail", false, "stream appended log lines as plain text")
	dashboard := flags.Bool("dashboard", false, "open the interactive log dashboard")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage:\n  conven services --logs [--tail] [--dashboard] [service...]")
		flags.PrintDefaults()
		fmt.Fprintln(flags.Output(), "\nWhen both modes are present, the last --tail or --dashboard flag wins.")
	}
	if ok, code := parseCommandFlags(flags, arguments, app.Output); !ok {
		return code
	}
	mode := resolveLogDisplayMode(arguments, *tail, *dashboard)
	workspace, err := convenruntime.OpenWorkspace(common.options(app.Cwd))
	if err != nil {
		return app.fail(err)
	}
	session, err := workspace.Store.Load()
	if err != nil {
		return app.fail(err)
	}
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
		return 0
	}
	if err := convenruntime.ShowLogs(app.Context, session, flags.Args(), false, app.Output); err != nil {
		return app.fail(err)
	}
	return 0
}

func resolveLogDisplayMode(arguments []string, tail bool, dashboard bool) logDisplayMode {
	if !tail && !dashboard {
		return logDisplaySnapshot
	}
	if tail && !dashboard {
		return logDisplayPlain
	}
	if dashboard && !tail {
		return logDisplayDashboard
	}
	mode := logDisplaySnapshot
	for _, argument := range arguments {
		if argument == "--" {
			break
		}
		name := strings.TrimLeft(argument, "-")
		value := "true"
		if separator := strings.IndexByte(name, '='); separator >= 0 {
			value = name[separator+1:]
			name = name[:separator]
		}
		enabled, err := strconv.ParseBool(value)
		if err != nil || !enabled {
			continue
		}
		switch name {
		case "tail":
			mode = logDisplayPlain
		case "dashboard":
			mode = logDisplayDashboard
		}
	}
	return mode
}

func (app App) runDashboard(arguments []string) int {
	flags := flag.NewFlagSet("services --dashboard", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	common := bindCommonFlags(flags, false)
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage:\n  conven services --dashboard [service...]")
		flags.PrintDefaults()
	}
	if ok, code := parseCommandFlags(flags, arguments, app.Output); !ok {
		return code
	}
	workspace, err := convenruntime.OpenWorkspace(common.options(app.Cwd))
	if err != nil {
		return app.fail(err)
	}
	session, err := workspace.Store.Load()
	if err != nil {
		return app.fail(err)
	}
	if err := convenruntime.TailLogs(app.Context, workspace, session, convenruntime.TailOptions{Names: flags.Args(), Version: app.Version}, app.Input, app.Output); err != nil {
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
	workspace, err := convenruntime.OpenWorkspace(options)
	if err != nil {
		return app.fail(err)
	}
	if err := convenruntime.Doctor(workspace, options, app.Output); err != nil {
		return app.fail(err)
	}
	return 0
}

func (app App) runList(arguments []string) int {
	flags := flag.NewFlagSet("services --list", flag.ContinueOnError)
	flags.SetOutput(app.Error)
	common := bindCommonFlags(flags, false)
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage:\n  conven services --list")
		flags.PrintDefaults()
	}
	if ok, code := parseCommandFlags(flags, arguments, app.Output); !ok {
		return code
	}
	if len(flags.Args()) != 0 {
		return app.fail(errors.New("services --list does not accept service arguments"))
	}
	workspace, err := convenruntime.OpenWorkspace(common.options(app.Cwd))
	if err != nil {
		return app.fail(err)
	}
	convenruntime.ListServices(workspace, app.Output)
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

func (common commonFlags) options(cwd string) convenruntime.CommonOptions {
	return convenruntime.CommonOptions{
		Cwd:         cwd,
		Environment: common.environment,
		Kubeconfig:  common.kubeconfig,
		Context:     common.context,
		Namespace:   common.namespace,
	}
}

func (app App) fail(err error) int {
	message := strings.TrimSpace(err.Error())
	style := terminal.New(app.Error)
	fmt.Fprintln(app.Error, style.Failure("conven: "+message))
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
  conven <command> [<args>]
  conven help [<command>]
  conven [--help | --version]

These are common Conven commands:

set up and configure a workspace
   init       Initialize a Conven workspace
   config     View or change Conven settings
   policy     Edit, import, or reset the workspace manifest
   plugins    Install, list, remove, or run plugins

run and inspect local services
   services   List, start, restart, stop, and inspect services
   doctor     Validate workspace and connection configuration

Run 'conven help <command>' or 'conven <command> --help' for detailed help.
`)
}

func (app App) printHelpUsage(output io.Writer) {
	fmt.Fprint(output, `usage:
  conven help [<command>]

With no command, show the root overview. With one command, show its detailed help.
`)
}

func (app App) printUnknownCommand(command string) {
	style := terminal.New(app.Error)
	fmt.Fprintln(app.Error, style.Failure(fmt.Sprintf("conven: %s is not a conven command. See 'conven --help'.", quoteCommand(command))))
	suggestions := similarCommands(command, []string{
		"config",
		"doctor",
		"help",
		"init",
		"plugins",
		"policy",
		"services",
		"version",
	})
	if len(suggestions) == 0 {
		return
	}
	fmt.Fprintln(app.Error)
	if len(suggestions) == 1 {
		fmt.Fprintln(app.Error, "The most similar command is")
	} else {
		fmt.Fprintln(app.Error, "The most similar commands are")
	}
	for _, suggestion := range suggestions {
		fmt.Fprintf(app.Error, "\t%s\n", suggestion)
	}
}

func quoteCommand(command string) string {
	quoted := fmt.Sprintf("%q", command)
	return "'" + strings.ReplaceAll(quoted[1:len(quoted)-1], "'", `\'`) + "'"
}

func similarCommands(command string, candidates []string) []string {
	command = strings.ToLower(command)
	maximumDistance := 1
	if len([]rune(command)) >= 5 {
		maximumDistance = 2
	}
	bestDistance := maximumDistance + 1
	var suggestions []string
	for _, candidate := range candidates {
		distance := commandEditDistance(command, candidate)
		if distance > maximumDistance || distance > bestDistance {
			continue
		}
		if distance < bestDistance {
			bestDistance = distance
			suggestions = suggestions[:0]
		}
		suggestions = append(suggestions, candidate)
	}
	return suggestions
}

func commandEditDistance(left string, right string) int {
	leftRunes := []rune(left)
	rightRunes := []rune(right)
	distances := make([][]int, len(leftRunes)+1)
	for leftIndex := range distances {
		distances[leftIndex] = make([]int, len(rightRunes)+1)
		distances[leftIndex][0] = leftIndex
	}
	for rightIndex := range distances[0] {
		distances[0][rightIndex] = rightIndex
	}
	for leftIndex := 1; leftIndex <= len(leftRunes); leftIndex++ {
		for rightIndex := 1; rightIndex <= len(rightRunes); rightIndex++ {
			replacementCost := 1
			if leftRunes[leftIndex-1] == rightRunes[rightIndex-1] {
				replacementCost = 0
			}
			distance := distances[leftIndex-1][rightIndex] + 1
			if insertionDistance := distances[leftIndex][rightIndex-1] + 1; insertionDistance < distance {
				distance = insertionDistance
			}
			if replacementDistance := distances[leftIndex-1][rightIndex-1] + replacementCost; replacementDistance < distance {
				distance = replacementDistance
			}
			if leftIndex > 1 && rightIndex > 1 &&
				leftRunes[leftIndex-1] == rightRunes[rightIndex-2] &&
				leftRunes[leftIndex-2] == rightRunes[rightIndex-1] {
				if transpositionDistance := distances[leftIndex-2][rightIndex-2] + 1; transpositionDistance < distance {
					distance = transpositionDistance
				}
			}
			distances[leftIndex][rightIndex] = distance
		}
	}
	return distances[len(leftRunes)][len(rightRunes)]
}

func (app App) printServicesUsage(output io.Writer) {
	fmt.Fprint(output, `usage:
  conven services <action> [<args>]

Manage the local service session for the current workspace.

available actions
   --list       List services declared by the workspace
   --registry   Update services from direct-child repositories; --prune missing ones
   --status     Show the current local service state
   --logs       Show logs; --tail streams plain text, --dashboard opens the UI
   --dashboard  Open the interactive log dashboard
   --start      Select and start local services; opens the dashboard on a TTY
   --restart    Restart selected or changed local services
   --stop       Stop selected local services
   --stop-all   Stop all services and release the workspace connection

The action flag must be the first argument after "conven services".
Run 'conven services <action> --help' for action-specific usage and flags.
Without service names, --start opens an interactive selector and asks for
confirmation; --restart restarts only changed services in the current session.
After a successful interactive --start, the dashboard opens by default; pass
--tail to stream plain-text logs instead.
`)
}
