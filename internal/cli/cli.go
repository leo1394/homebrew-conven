package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/config"
	convenruntime "github.com/leo1394/homebrew-conven/internal/runtime"
	"github.com/leo1394/homebrew-conven/internal/selector"
	"github.com/leo1394/homebrew-conven/internal/terminal"
)

type App struct {
	Input                     *os.File
	Output                    io.Writer
	Error                     io.Writer
	Context                   context.Context
	Cwd                       string
	Executable                string
	Version                   string
	VersionDate               string
	WorkspaceEditor           func(context.Context, string) error
	StartReplacementConfirmer func(context.Context, []string) (bool, error)
	SingleSelector            func(context.Context, *os.File, io.Writer, selector.Prompt, []selector.Candidate) (selector.Candidate, bool, error)
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

const projectHomepage = "https://github.com/leo1394/homebrew-conven"

const (
	logDisplaySnapshot logDisplayMode = iota
	logDisplayPlain
	logDisplayDashboard
)

func (app App) Run(arguments []string) int {
	app = app.withDefaults()
	var code int
	app, arguments, code = app.consumeWorkingDirectories(arguments)
	if code != 0 {
		return code
	}
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
		app.printVersion()
		return 0
	case "init":
		return app.runInit(arguments[1:])
	case "config":
		return app.runConfig(arguments[1:])
	case "workspace":
		return app.runWorkspaceManifest(arguments[1:])
	case "plugins":
		return app.runPlugins(arguments[1:])
	case "services":
		return app.runServices(arguments[1:])
	case "doctor":
		return app.runDoctor(arguments[1:])
	case "status":
		return app.runWorkspaceStatus(arguments[1:])
	case "__hot-reload":
		return app.runHotReloadWatcher(arguments[1:])
	case "__completion":
		return app.runCompletion(arguments[1:])
	default:
		app.printUnknownCommand(arguments[0])
		return 2
	}
}

func (app App) consumeWorkingDirectories(arguments []string) (App, []string, int) {
	for len(arguments) > 0 && arguments[0] == "-C" {
		if len(arguments) < 2 {
			style := terminal.New(app.Error)
			fmt.Fprintln(app.Error, style.Failure("conven: option -C requires a path"))
			return app, nil, 2
		}
		requested := arguments[1]
		directory := requested
		if !filepath.IsAbs(directory) && app.Cwd != "" {
			directory = filepath.Join(app.Cwd, directory)
		}
		resolved, err := config.ResolveDirectory(directory)
		if err != nil {
			return app, nil, app.fail(fmt.Errorf("cannot change to directory %q: %w", requested, err))
		}
		app.Cwd, err = filepath.EvalSymlinks(resolved)
		if err != nil {
			return app, nil, app.fail(fmt.Errorf("cannot change to directory %q: resolve symbolic links: %w", requested, err))
		}
		arguments = arguments[2:]
	}
	return app, arguments, 0
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
		fmt.Fprint(app.Output, "usage:\n  conven version\n  conven -v\n  conven --version\n")
		return 0
	case "init":
		return app.runInit([]string{"--help"})
	case "config":
		return app.runConfig([]string{"--help"})
	case "workspace":
		return app.runWorkspaceManifest([]string{"--help"})
	case "plugins":
		return app.runPlugins([]string{"--help"})
	case "services":
		return app.runServices([]string{"--help"})
	case "doctor":
		return app.runDoctor([]string{"--help"})
	case "status":
		return app.runWorkspaceStatus([]string{"--help"})
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
	case "--listen":
		return app.runServiceListener(remaining)
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
	case "--cleanup":
		return app.runCleanup(remaining)
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
	withDependencies := flags.Bool("with-dependencies", false, "include transitive local service dependencies")
	skipBuild := flags.Bool("skip-build", false, "skip build; artifacts under current runtime cannot be reused after a fresh start")
	skipVerify := flags.Bool("skip-verify", false, "skip health, listener, and registry verification")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage:\n  conven services --start [flags] [service...]")
		flags.PrintDefaults()
		fmt.Fprintln(flags.Output(), "\nIf verified services are already running, an interactive terminal offers Stop then start or Cancel after the replacement plan passes static validation.")
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
			printStartCancelled(app.Output, "No services were started.")
			return 0
		}
		services = selected
	}
	if *withDependencies {
		services, err = convenruntime.ExpandLocalServiceDependencies(workspace.Manifest, services)
		if err != nil {
			return app.fail(err)
		}
	}
	dashboardAvailable := !*dryRun && !*tail && convenruntime.DashboardAvailable(app.Input, app.Output)
	var dashboardOptions convenruntime.TailOptions
	if dashboardAvailable {
		dashboardOptions, err = dashboardTailOptions(workspace, nil, app.Version)
		if err != nil {
			return app.fail(err)
		}
	}
	startOptions := convenruntime.StartOptions{
		Common:              options,
		Services:            services,
		DryRun:              *dryRun,
		SkipBuild:           *skipBuild,
		SkipVerify:          *skipVerify,
		HotReloadExecutable: app.Executable,
		Output:              app.Output,
	}
	session, err := convenruntime.Start(app.Context, workspace, startOptions)
	var running *convenruntime.RunningServicesError
	if errors.As(err, &running) {
		replace, promptErr := app.StartReplacementConfirmer(app.Context, running.Services)
		if promptErr != nil {
			return app.fail(promptErr)
		}
		if !replace {
			printStartCancelled(app.Output, "Running services were left unchanged.")
			return 0
		}
		session, err = convenruntime.ReplaceStart(app.Context, workspace, startOptions, running.SessionToken)
	}
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
	if dashboardAvailable {
		if err := convenruntime.TailLogs(app.Context, workspace, session, dashboardOptions, app.Input, app.Output); err != nil {
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
	var workspace *convenruntime.WorkspaceData
	var err error
	if *all {
		workspace, err = convenruntime.OpenWorkspaceForStopAll(common.options(app.Cwd))
	} else {
		workspace, err = convenruntime.OpenWorkspace(common.options(app.Cwd))
	}
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
		options, err := dashboardTailOptions(workspace, flags.Args(), app.Version)
		if err != nil {
			return app.fail(err)
		}
		if err := convenruntime.TailLogs(app.Context, workspace, session, options, app.Input, app.Output); err != nil {
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
	options, err := dashboardTailOptions(workspace, flags.Args(), app.Version)
	if err != nil {
		return app.fail(err)
	}
	if err := convenruntime.TailLogs(app.Context, workspace, session, options, app.Input, app.Output); err != nil {
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
		flags.StringVar(&common.environment, "env", "", "environment profile name (defaults to dev or the sole profile)")
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
	return parseCommandFlagsWithHint(flags, arguments, helpOutput, nil)
}

func parseCommandFlagsWithHint(flags *flag.FlagSet, arguments []string, helpOutput io.Writer, hint func(error) (string, string)) (bool, int) {
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
		parsedOutput := parseOutput.String()
		if errors.Is(err, flag.ErrHelp) {
			output = helpOutput
		}
		if hint != nil && err != nil && !errors.Is(err, flag.ErrHelp) {
			if summary, action := hint(err); summary != "" {
				actions := []string(nil)
				if action != "" {
					actions = []string{action}
				}
				printWarningBlock(output, summary, nil, actions)
				if usageIndex := strings.Index(parsedOutput, "Usage:\n"); usageIndex >= 0 {
					parsedOutput = parsedOutput[usageIndex:]
				}
			}
		}
		fmt.Fprint(output, canonicalFlagOutput(parsedOutput))
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return false, 0
		}
		return false, 2
	}
	return true, 0
}

func canonicalFlagOutput(value string) string {
	lines := strings.SplitAfter(value, "\n")
	for index, line := range lines {
		lines[index] = canonicalFlagOutputLine(line)
	}
	return strings.Join(lines, "")
}

func canonicalFlagOutputLine(line string) string {
	if strings.HasPrefix(line, "  -") && !strings.HasPrefix(line, "  --") {
		end := flagNameEnd(line, 3)
		if canonicalLongFlagName(line[3:end]) {
			return line[:2] + "--" + line[3:]
		}
	}
	for _, prefix := range []string{
		"flag provided but not defined: -",
		"flag needs an argument: -",
	} {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		return canonicalFlagNameAt(line, len(prefix))
	}
	if strings.HasPrefix(line, "invalid value ") {
		const marker = " for flag -"
		if start := strings.LastIndex(line, marker); start >= 0 {
			return canonicalFlagNameAt(line, start+len(marker))
		}
	}
	if strings.HasPrefix(line, "invalid boolean value ") {
		const marker = " for -"
		if start := strings.LastIndex(line, marker); start >= 0 {
			return canonicalFlagNameAt(line, start+len(marker))
		}
	}
	return line
}

func canonicalFlagNameAt(line string, nameStart int) string {
	if nameStart >= len(line) || line[nameStart] == '-' {
		return line
	}
	nameEnd := flagNameEnd(line, nameStart)
	if !canonicalLongFlagName(line[nameStart:nameEnd]) {
		return line
	}
	return line[:nameStart] + "-" + line[nameStart:]
}

func flagNameEnd(value string, start int) int {
	end := start
	for end < len(value) {
		character := value[end]
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '-' && character != '_' {
			break
		}
		end++
	}
	return end
}

func canonicalLongFlagName(name string) bool {
	if len(name) < 2 {
		return false
	}
	first := name[0]
	return (first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z')
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
	if app.VersionDate == "" {
		app.VersionDate = "unknown"
	}
	if app.WorkspaceEditor == nil {
		input := app.Input
		output := app.Output
		errorOutput := app.Error
		app.WorkspaceEditor = func(ctx context.Context, path string) error {
			return launchWorkspaceEditor(ctx, input, output, errorOutput, path)
		}
	}
	if app.StartReplacementConfirmer == nil {
		input := app.Input
		errorOutput := app.Error
		app.StartReplacementConfirmer = func(ctx context.Context, services []string) (bool, error) {
			return confirmStartReplacement(ctx, input, errorOutput, services)
		}
	}
	if app.SingleSelector == nil {
		app.SingleSelector = selector.SelectOne
	}
	return app
}

func (app App) printUsage(output io.Writer) {
	fmt.Fprint(output, `usage:
  conven [-C <path>]... <command> [<args>]
  conven [-C <path>]... help [<command>]
  conven [-C <path>]... [-h | --help | -v | --version]

Global option:
   -C <path>  Run as if conven was started in <path> instead of the current working directory

These are common Conven commands:

set up and configure a workspace
   init       Initialize a Conven workspace
   config     View or change Conven settings
   workspace  Edit, validate, import, or reset the workspace manifest
   plugins    Install, list, remove, or run plugins

run and inspect local services
   status     Show the complete workspace and runtime status
   services   List, start, restart, stop, and inspect services
   doctor     Validate workspace and connection configuration

Run 'conven help <command>' or 'conven <command> --help' for detailed help.
`)
}

func (app App) printVersion() {
	fmt.Fprintf(app.Output, "       %s       %s\n", "ccc", "/====O")
	fmt.Fprintf(app.Output, "%s %s   %s%s\n", "O===O","cc", "=====O", "====O")
	fmt.Fprintf(app.Output, "       %s       %s\n", "ccc", "\\====O")
	fmt.Fprintf(app.Output, "conven version %s (%s)\n%s\n", app.Version, app.VersionDate, projectHomepage)
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
		"services",
		"status",
		"version",
		"workspace",
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
   --listen     Turn all-interfaces listening on or off for selected services
   --status     Show the current local service state
   --logs       Show logs; --tail streams plain text, --dashboard opens the UI
   --dashboard  Open the interactive log dashboard
   --start      Select and start local services; opens the dashboard on a TTY
   --restart    Restart selected or changed services; opens the dashboard on a TTY
   --stop       Stop selected local services
   --stop-all   Stop all services and release the workspace connection
   --cleanup    Remove saved build artifacts and service logs

The action flag must be the first argument after "conven services".
Run 'conven services <action> --help' for action-specific usage and flags.
Without service names, --start opens an interactive selector and asks for
confirmation; --restart restarts only changed services in the current session.
After a successful interactive --start or --restart, the dashboard opens by
default; pass --tail to stream plain-text logs instead.
Successful starts and restarts keep watching service source. A failed rebuild
is logged without stopping the last-known-good process; a successful rebuild
automatically replaces it.
If --start finds a verified running session, it validates the replacement plan
before offering Stop then start or Cancel (default). Non-interactive conflicts
leave the current session unchanged.
`)
}
