package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/leo1394/homebrew-loom/internal/materialize"
	"github.com/leo1394/homebrew-loom/internal/terminal"
)

type StartOptions struct {
	Common     CommonOptions
	Services   []string
	DryRun     bool
	SkipBuild  bool
	SkipVerify bool
	Output     io.Writer
}

func Start(ctx context.Context, workspace *WorkspaceData, options StartOptions) (*Session, error) {
	output := options.Output
	if output == nil {
		output = io.Discard
	}
	style := terminal.New(output)
	if options.DryRun {
		plan, err := BuildPlan(workspace, options.Common, options.Services)
		if err != nil {
			return nil, err
		}
		if options.SkipBuild {
			if err := validateSkipBuild(plan); err != nil {
				return nil, err
			}
		}
		printPlan(output, plan, true)
		return nil, nil
	}
	unlock, err := workspace.Store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	existing, err := workspace.Store.Load()
	if err != nil {
		return nil, err
	}
	if existing != nil {
		active := activeServices(existing)
		if len(active) > 0 {
			return nil, fmt.Errorf("workspace already has running services: %s; use loom services --restart or loom services --stop first", strings.Join(active, ", "))
		}
		for _, process := range existing.Services {
			if ProcessGroupAlive(process.PGID) {
				return nil, fmt.Errorf("workspace has an unverified process group for %s; use loom services --stop --all before starting a new session", process.Name)
			}
		}
		if existing.Connection != nil && existing.Connection.Owned && !existing.Connection.Managed && ProcessGroupAlive(existing.Connection.PGID) {
			return nil, fmt.Errorf("workspace has an active or unverified %s connection; use loom services --stop --all before starting a new session", existing.Connection.Driver)
		}
	}
	plan, err := BuildPlan(workspace, options.Common, options.Services)
	if err != nil {
		return nil, err
	}
	if options.SkipBuild {
		if err := validateSkipBuild(plan); err != nil {
			return nil, err
		}
	}
	if plan.Connection.Driver == "ktctl" {
		if err := validateKubeconfig(plan.Connection.Kubeconfig); err != nil {
			return nil, err
		}
		printKubeconfigPermissionWarning(output, plan.Connection.Kubeconfig)
	}
	if err := validatePlanCommands(workspace, plan); err != nil {
		return nil, err
	}
	if servicesNeedBuildDiskSpace(plan, plan.Order, options.SkipBuild) {
		if err := checkBuildDiskSpaceAndWarn(output, workspace.Root); err != nil {
			return nil, err
		}
	}
	if existing != nil {
		if existing.Connection != nil {
			if err := releaseConnection(context.Background(), existing.Connection, workspace.Store.Root, false, output); err != nil {
				return nil, fmt.Errorf("release previous workspace connection before replacing stale session: %w", err)
			}
		}
		if err := workspace.Store.Clear(); err != nil {
			return nil, err
		}
	}
	if err := workspace.Store.ResetCurrent(); err != nil {
		return nil, err
	}
	printPlan(output, plan, false)
	connection, err := EnsureConnection(ctx, plan.Connection, ConnectionLogPath(workspace.Store.Root), workspace.Store.Root, output)
	if err != nil {
		if connection != nil {
			failedSession := &Session{
				Workspace:   workspace.Root,
				ConfigPath:  workspace.ConfigPath,
				Environment: plan.EnvironmentName,
				CreatedAt:   time.Now(),
				Connection:  connection,
			}
			if saveErr := workspace.Store.Save(failedSession); saveErr != nil {
				return nil, errors.Join(err, fmt.Errorf("preserve failed connection state: %w", saveErr))
			}
		}
		return nil, err
	}
	session := &Session{
		Workspace:   workspace.Root,
		ConfigPath:  workspace.ConfigPath,
		Environment: plan.EnvironmentName,
		CreatedAt:   time.Now(),
		Selected:    append([]string(nil), plan.Selected...),
		Connection:  connection,
	}
	if err := workspace.Store.Save(session); err != nil {
		return nil, errors.Join(err, releaseConnection(context.Background(), connection, workspace.Store.Root, false, output))
	}
	started := make(map[string]ServiceProcess, len(plan.Order))
	sourceFingerprints := make(map[string]string, len(plan.Order))
	planFingerprints := make(map[string]string, len(plan.Order))
	for _, group := range plan.Groups {
		if len(group) > 1 {
			fmt.Fprintf(output, "%s: %s\n", style.Label("Starting dependency cycle together"), style.Identifiers(group, ", "))
		}
		for _, name := range group {
			if err := ctx.Err(); err != nil {
				return nil, failStartup(workspace, session, connection, output, err)
			}
			service := plan.Services[name]
			sourceFingerprint, err := SourceFingerprint(service.Directory)
			if err != nil {
				return nil, failStartup(workspace, session, connection, output, fmt.Errorf("fingerprint %s source: %w", name, err))
			}
			planFingerprint, err := PlanFingerprint(service)
			if err != nil {
				return nil, failStartup(workspace, session, connection, output, fmt.Errorf("fingerprint %s plan: %w", name, err))
			}
			sourceFingerprints[name] = sourceFingerprint
			planFingerprints[name] = planFingerprint
			configDirectory := filepath.Join(plan.RunDir, "configs", name)
			if err := ensurePrivateDirectory(configDirectory); err != nil {
				return nil, failStartup(workspace, session, connection, output, fmt.Errorf("create %s runtime config directory: %w", name, err))
			}
			if service.Config != nil {
				fmt.Fprintf(output, "%s %s config with %s/%s...\n", style.Label("Materializing"), style.Identifier(name), style.Identifier(string(service.Config.Plan.SourceDriver)), style.Identifier(string(service.Config.Plan.Driver)))
				if err := materialize.Materialize(ctx, service.Config.Plan); err != nil {
					return nil, failStartup(workspace, session, connection, output, fmt.Errorf("materialize %s config: %w", name, err))
				}
			}
			if len(service.Prepare) > 0 {
				fmt.Fprintf(output, "%s %s...\n", style.Label("Preparing"), style.Identifier(name))
				if _, err := checkBuildDiskSpace(workspace.Root); err != nil {
					return nil, failStartup(workspace, session, connection, output, fmt.Errorf("prepare %s: %w", name, err))
				}
				prepareLog := filepath.Join(plan.RunDir, "logs", name+"-prepare.log")
				if err := RunForeground(ctx, service.Prepare, service.Workdir, service.Environment, output, prepareLog); err != nil {
					return nil, failStartup(workspace, session, connection, output, fmt.Errorf("prepare %s: %w", name, err))
				}
			}
			if !options.SkipBuild && len(service.Build) > 0 {
				fmt.Fprintf(output, "%s %s...\n", style.Label("Building"), style.Identifier(name))
				if _, err := checkBuildDiskSpace(workspace.Root); err != nil {
					return nil, failStartup(workspace, session, connection, output, fmt.Errorf("build %s: %w", name, err))
				}
				buildLog := filepath.Join(plan.RunDir, "logs", name+"-build.log")
				if err := RunForeground(ctx, service.Build, service.Workdir, service.Environment, output, buildLog); err != nil {
					return nil, failStartup(workspace, session, connection, output, fmt.Errorf("build %s: %w", name, err))
				}
			}
			if err := ctx.Err(); err != nil {
				return nil, failStartup(workspace, session, connection, output, err)
			}
			if err := inspectRunWorkdir(service); err != nil {
				return nil, failStartup(workspace, session, connection, output, err)
			}
			fmt.Fprintf(output, "%s %s...\n", style.Label("Starting"), style.Identifier(name))
			process, err := StartService(name, service.Run, service.RunWorkdir, service.Environment, service.LogPath)
			if err != nil {
				return nil, failStartup(workspace, session, connection, output, err)
			}
			process.Ports = copyPorts(service.Ports)
			started[name] = process
			session.Services = append(session.Services, process)
			if err := workspace.Store.Save(session); err != nil {
				return nil, failStartup(workspace, session, connection, output, err)
			}
		}
		if !options.SkipVerify {
			for _, name := range group {
				service := plan.Services[name]
				process := started[name]
				if err := WaitHealthy(ctx, process, service.Health); err != nil {
					fmt.Fprintf(output, "%s %s log lines:\n", style.Label("Last"), style.Identifier(name))
					ShowLogs(context.Background(), session, []string{name}, false, output)
					return nil, failStartup(workspace, session, connection, output, err)
				}
				fmt.Fprintf(output, "%s %s\n", style.Identifier(name), style.Success("is healthy."))
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, failStartup(workspace, session, connection, output, err)
		}
		for _, name := range group {
			process := sessionProcess(session, name)
			process.SourceFingerprint = sourceFingerprints[name]
			process.PlanFingerprint = planFingerprints[name]
			replaceSessionProcess(session, process)
			started[name] = process
		}
		if err := workspace.Store.Save(session); err != nil {
			return nil, failStartup(workspace, session, connection, output, err)
		}
	}
	for _, process := range session.Services {
		if !ProcessAlive(process.PID) || VerifyProcess(process) != nil {
			return nil, failStartup(workspace, session, connection, output, fmt.Errorf("%s exited or changed identity during startup", process.Name))
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, failStartup(workspace, session, connection, output, err)
	}
	if err := workspace.Store.Save(session); err != nil {
		return nil, failStartup(workspace, session, connection, output, err)
	}
	fmt.Fprintln(output, style.Success("Local services are ready. Use `loom services --logs --tail` to observe them."))
	return session, nil
}

func validateSkipBuild(plan *Plan) error {
	for _, name := range plan.Order {
		service := plan.Services[name]
		if len(service.Build) == 0 || !pathWithinDirectory(plan.RunDir, service.Artifact) {
			continue
		}
		return fmt.Errorf("--skip-build cannot reuse %s artifact %s because Loom resets the current runtime directory for a fresh start; set services.%s.runner.artifact to an absolute persistent path or omit --skip-build", name, service.Artifact, name)
	}
	return nil
}

func servicesNeedBuildDiskSpace(plan *Plan, names []string, skipBuild bool) bool {
	for _, name := range names {
		service := plan.Services[name]
		if len(service.Prepare) > 0 || (!skipBuild && len(service.Build) > 0) {
			return true
		}
	}
	return false
}

func checkBuildDiskSpaceAndWarn(output io.Writer, path string) error {
	available, err := checkBuildDiskSpace(path)
	if err != nil {
		return err
	}
	if warning := buildDiskSpaceWarning(path, available); warning != "" {
		style := terminal.New(output)
		fmt.Fprintln(output, style.Warning("Warning: "+warning))
	}
	return nil
}

func ensurePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("runtime path %q must be a real directory", path)
		}
	} else if os.IsNotExist(err) {
		if err := os.MkdirAll(path, 0700); err != nil {
			return err
		}
	} else {
		return err
	}
	if err := os.Chmod(path, 0700); err != nil {
		return err
	}
	return nil
}

func Stop(ctx context.Context, workspace *WorkspaceData, names []string, all bool, force bool, output io.Writer) error {
	if output == nil {
		output = io.Discard
	}
	if !all && len(names) == 0 {
		return errors.New("stop requires service names or --all")
	}
	unlock, err := workspace.Store.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	session, err := workspace.Store.Load()
	if err != nil {
		return err
	}
	if session == nil {
		fmt.Fprintln(output, "No loom session found.")
		if all && force {
			recovered, err := recoverUnleasedConnections(ctx, workspace.Store.Root, output)
			if err != nil {
				return err
			}
			if recovered > 0 {
				fmt.Fprintf(output, "Recovered %d unleased shared connection record(s).\n", recovered)
			} else {
				fmt.Fprintln(output, "No unleased shared connection records were recoverable.")
			}
		}
		return nil
	}
	targets := make(map[string]bool)
	if all {
		for _, process := range session.Services {
			targets[process.Name] = true
		}
	} else {
		available := make(map[string]bool)
		for _, process := range session.Services {
			available[process.Name] = true
		}
		for _, name := range names {
			if !available[name] {
				return fmt.Errorf("service %q is not part of the current session", name)
			}
			targets[name] = true
		}
	}
	failed := make([]string, 0)
	for index := len(session.Services) - 1; index >= 0; index-- {
		process := session.Services[index]
		if !targets[process.Name] {
			continue
		}
		fmt.Fprintf(output, "Stopping %s...\n", process.Name)
		stopErr := StopProcess(process, 10*time.Second)
		if stopErr != nil && force && ProcessGroupAlive(process.PGID) {
			fmt.Fprintf(output, "Force stopping unverified process group %d for %s...\n", process.PGID, process.Name)
			stopErr = ForceStopProcessGroup(process, 3*time.Second)
		}
		if stopErr != nil {
			fmt.Fprintf(output, "Error stopping %s: %v\n", process.Name, stopErr)
			failed = append(failed, process.Name)
			delete(targets, process.Name)
		}
	}
	remaining := make([]ServiceProcess, 0)
	for _, process := range session.Services {
		if !targets[process.Name] {
			remaining = append(remaining, process)
		}
	}
	session.Services = remaining
	remainingNames := make(map[string]bool, len(remaining))
	for _, process := range remaining {
		remainingNames[process.Name] = true
	}
	selected := make([]string, 0, len(remaining))
	for _, name := range session.Selected {
		if remainingNames[name] {
			selected = append(selected, name)
			delete(remainingNames, name)
		}
	}
	for _, process := range remaining {
		if remainingNames[process.Name] {
			selected = append(selected, process.Name)
			delete(remainingNames, process.Name)
		}
	}
	session.Selected = selected
	if len(session.Services) == 0 {
		if session.Connection != nil {
			if session.Connection.Managed {
				fmt.Fprintf(output, "Releasing %s connection lease...\n", session.Connection.Driver)
			} else if session.Connection.Owned {
				fmt.Fprintf(output, "Stopping Loom-owned %s connection...\n", session.Connection.Driver)
			} else {
				fmt.Fprintf(output, "Leaving external %s connection running; Loom does not own it.\n", session.Connection.Driver)
			}
			if err := releaseConnection(ctx, session.Connection, workspace.Store.Root, force, output); err != nil {
				fmt.Fprintf(output, "Error releasing connection: %v\n", err)
				failed = append(failed, "connection/"+session.Connection.Driver)
			} else {
				session.Connection = nil
			}
		} else {
			session.Connection = nil
		}
	}
	if len(session.Services) == 0 && session.Connection == nil {
		if err := workspace.Store.Clear(); err != nil {
			return err
		}
	} else if err := workspace.Store.Save(session); err != nil {
		return err
	}
	if len(failed) > 0 {
		sort.Strings(failed)
		return fmt.Errorf("failed to stop: %s", strings.Join(failed, ", "))
	}
	return nil
}

func Status(ctx context.Context, workspace *WorkspaceData, output io.Writer) error {
	style := terminal.New(output)
	fmt.Fprintf(output, "%s: %s\n", style.Label("Runtime"), style.Identifier(workspace.Store.Root))
	fmt.Fprintf(output, "%s: %s\n", style.Label("Current"), style.Identifier(workspace.Store.CurrentDir))
	session, err := workspace.Store.Load()
	if err != nil {
		return err
	}
	if session == nil {
		fmt.Fprintln(output, style.Warning("No loom session found."))
		_, err := printSharedConnectionStatus(ctx, output)
		return err
	}
	fmt.Fprintf(output, "%s: %s\n", style.Label("Workspace"), style.Identifier(session.Workspace))
	fmt.Fprintf(output, "%s: %s\n", style.Label("Environment"), style.Identifier(session.Environment))
	for _, process := range session.Services {
		state := "stopped"
		if ProcessAlive(process.PID) && VerifyProcess(process) == nil {
			state = "running"
		} else if ProcessGroupAlive(process.PGID) {
			state = "unverified"
		}
		fmt.Fprintf(output, "%s %s pid=%d pgid=%d log=%s\n", style.Identifier(fmt.Sprintf("%-28s", process.Name)), styledProcessState(style, state, 10), process.PID, process.PGID, process.LogPath)
	}
	if session.Connection != nil {
		state := "reused"
		if session.Connection.PID > 0 {
			managed := ServiceProcess{
				Name:     "connection/" + session.Connection.Driver,
				PID:      session.Connection.PID,
				PGID:     session.Connection.PGID,
				Command:  session.Connection.Command,
				Identity: session.Connection.Identity,
			}
			if ProcessAlive(session.Connection.PID) && VerifyProcess(managed) == nil {
				state = "running"
				if session.Connection.Managed && !session.Connection.Owned {
					state = "shared"
				}
			} else if ProcessGroupAlive(session.Connection.PGID) {
				state = "unverified"
			} else {
				state = "stopped"
			}
		}
		if session.Connection.PID > 0 {
			fmt.Fprintf(output, "%s %s pid=%d pgid=%d log=%s\n", style.Identifier(fmt.Sprintf("connection/%-17s", session.Connection.Driver)), styledProcessState(style, state, 10), session.Connection.PID, session.Connection.PGID, session.Connection.LogPath)
		} else {
			fmt.Fprintf(output, "%s %s\n", style.Identifier(fmt.Sprintf("connection/%-17s", session.Connection.Driver)), styledProcessState(style, state, 0))
		}
	}
	return nil
}

func styledProcessState(style terminal.Style, state string, width int) string {
	display := state
	if width > 0 {
		display = fmt.Sprintf("%-*s", width, state)
	}
	switch state {
	case "running", "reused", "shared":
		return style.Success(display)
	case "stopped":
		return style.Failure(display)
	case "unverified":
		return style.Warning(display)
	default:
		return style.Identifier(display)
	}
}

func Doctor(workspace *WorkspaceData, options CommonOptions, output io.Writer) error {
	style := terminal.New(output)
	names := make([]string, 0, len(workspace.Manifest.Services))
	for name := range workspace.Manifest.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	plan, err := BuildPlan(workspace, options, names)
	if err != nil {
		return err
	}
	if plan.Connection.Driver == "ktctl" {
		if err := validateKubeconfig(plan.Connection.Kubeconfig); err != nil {
			return err
		}
		printKubeconfigPermissionWarning(output, plan.Connection.Kubeconfig)
	}
	if err := validatePlanCommands(workspace, plan); err != nil {
		return err
	}
	if servicesNeedBuildDiskSpace(plan, plan.Order, false) {
		if err := checkBuildDiskSpaceAndWarn(output, workspace.Root); err != nil {
			return err
		}
	}
	fmt.Fprintf(output, "%s: %s\n", style.Label("Runtime"), style.Identifier(workspace.Store.Root))
	fmt.Fprintf(output, "%s: %s\n", style.Label("Current"), style.Identifier(workspace.Store.CurrentDir))
	fmt.Fprintf(output, "%s: %s\n", style.Label("Workspace"), style.Identifier(workspace.Root))
	fmt.Fprintf(output, "%s: %s\n", style.Label("Manifest"), style.Identifier(workspace.ConfigPath))
	fmt.Fprintf(output, "%s: %s\n", style.Label("Environment"), style.Identifier(plan.EnvironmentName))
	fmt.Fprintf(output, "%s: %s\n", style.Label("Services"), style.Identifier(fmt.Sprintf("%d", len(plan.Services))))
	fmt.Fprintf(output, "%s: %s\n", style.Label("Connection"), style.Identifier(displayConnection(plan.Connection)))
	fmt.Fprintln(output, style.Success("Doctor checks passed."))
	return nil
}

func validatePlanCommands(workspace *WorkspaceData, plan *Plan) error {
	if plan.Connection.Driver != "" && plan.Connection.Driver != "none" {
		connectionCommand, err := BuildConnectionCommand(plan.Connection)
		if err != nil {
			return err
		}
		if err := commandAvailable(connectionCommand[0], workspace.Root); err != nil {
			return fmt.Errorf("connection command: %w", err)
		}
	}
	for _, name := range plan.Order {
		service := plan.Services[name]
		for label, command := range map[string][]string{
			"prepare": service.Prepare,
			"build":   service.Build,
		} {
			if len(command) == 0 {
				continue
			}
			if err := commandAvailable(command[0], service.Workdir); err != nil {
				return fmt.Errorf("%s %s command: %w", name, label, err)
			}
		}
		if len(service.Run) > 0 && service.Run[0] != service.Artifact {
			if err := commandAvailableForRun(service.Run[0], service, plan); err != nil {
				return fmt.Errorf("%s run command: %w", name, err)
			}
		}
		if service.Health.Type == "command" && len(service.Health.Command) > 0 {
			if err := commandAvailableForRun(service.Health.Command[0], service, plan); err != nil {
				return fmt.Errorf("%s health command: %w", name, err)
			}
		}
	}
	return nil
}

func ListServices(workspace *WorkspaceData, output io.Writer) {
	style := terminal.New(output)
	names := make([]string, 0, len(workspace.Manifest.Services))
	for name := range workspace.Manifest.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		service := workspace.Manifest.Services[name]
		fmt.Fprintf(output, "%s %s\n", style.Identifier(fmt.Sprintf("%-28s", name)), service.Path)
	}
}

func printPlan(output io.Writer, plan *Plan, dryRun bool) {
	style := terminal.New(output)
	fmt.Fprintf(output, "%s: %s\n", style.Label("Workspace"), style.Identifier(plan.Workspace.Root))
	fmt.Fprintf(output, "%s: %s\n", style.Label("Runtime"), style.Identifier(plan.Workspace.Store.Root))
	fmt.Fprintf(output, "%s: %s\n", style.Label("Current"), style.Identifier(plan.RunDir))
	fmt.Fprintf(output, "%s: %s\n", style.Label("Environment"), style.Identifier(plan.EnvironmentName))
	fmt.Fprintf(output, "%s: %s\n", style.Label("Looming local services"), style.Identifiers(plan.Selected, ", "))
	registry := plan.Environment.Registry
	if registry == "" {
		registry = "configured registry"
	}
	if len(plan.Remote) > 0 {
		fmt.Fprintf(output, "%s %s: %s\n", style.Label("Remote via"), style.Identifier(registry), style.Identifiers(plan.Remote, ", "))
	} else {
		fmt.Fprintf(output, "%s: none\n", style.Label("Remote dependencies"))
	}
	groups := make([]string, 0, len(plan.Groups))
	for _, group := range plan.Groups {
		groups = append(groups, style.Identifiers(group, " + "))
	}
	fmt.Fprintf(output, "%s: %s\n", style.Label("Start groups"), strings.Join(groups, " -> "))
	fmt.Fprintf(output, "%s: %s\n", style.Label("Connection"), style.Identifier(displayConnection(plan.Connection)))
	for _, name := range plan.Order {
		service := plan.Services[name]
		if service.Config == nil {
			continue
		}
		fmt.Fprintf(output, "%s %s: %s=%s %s=%s %s=%s %s=%s %s=%s -> %s\n",
			style.Label("Config"),
			style.Identifier(name),
			style.Label("policy"),
			style.Identifier(service.Config.Policy),
			style.Label("framework"),
			style.Identifier(service.Config.Framework),
			style.Label("source"),
			style.Identifier(string(service.Config.Plan.SourceDriver)),
			style.Label("discovery"),
			style.Identifier(service.Config.Discovery),
			style.Label("materializer"),
			style.Identifier(string(service.Config.Plan.Driver)),
			style.Identifier(service.Config.Plan.TargetDir),
		)
		for _, route := range service.Config.Routes {
			location := "remote"
			if route.Local {
				location = "local"
			}
			mode := route.Mode
			if mode == "" {
				mode = "unconfigured"
			}
			fmt.Fprintf(output, "  %s -> %s: %s (%s %s)\n", style.Identifier(name), style.Identifier(route.Dependency), style.Identifier(route.Binding), style.Identifier(location), style.Identifier(mode))
		}
	}
	if dryRun {
		fmt.Fprintln(output, style.Label("Dry run: no connection, config fetch/materialization, build, process, or state changes were made."))
	}
}

func displayConnection(connection ConnectionConfig) string {
	if connection.Driver == "" || connection.Driver == "none" {
		return "none"
	}
	details := connection.Driver
	if connection.Context != "" {
		details += " context=" + connection.Context
	}
	if connection.Namespace != "" {
		details += " namespace=" + connection.Namespace
	}
	return details
}

func activeServices(session *Session) []string {
	names := make([]string, 0)
	for _, process := range session.Services {
		if ProcessAlive(process.PID) && VerifyProcess(process) == nil {
			names = append(names, process.Name)
		}
	}
	sort.Strings(names)
	return names
}

func failStartup(workspace *WorkspaceData, session *Session, connection *ConnectionProcess, output io.Writer, failure error) error {
	if err := rollbackSession(workspace, session, connection, output); err != nil {
		return errors.Join(failure, fmt.Errorf("startup rollback incomplete: %w", err))
	}
	return failure
}

func rollbackSession(workspace *WorkspaceData, session *Session, connection *ConnectionProcess, output io.Writer) error {
	style := terminal.New(output)
	if len(session.Services) > 0 {
		fmt.Fprintln(output, style.Failure("Startup failed; stopping services started by this command."))
	}
	failedNames := make(map[string]bool)
	problems := make([]error, 0)
	for index := len(session.Services) - 1; index >= 0; index-- {
		process := session.Services[index]
		if err := StopProcess(process, 3*time.Second); err != nil {
			fmt.Fprintf(output, "%s %s: %v\n", style.Warning("Rollback warning for"), style.Identifier(process.Name), err)
			failedNames[process.Name] = true
			problems = append(problems, fmt.Errorf("stop %s: %w", process.Name, err))
		}
	}
	remaining := make([]ServiceProcess, 0, len(failedNames))
	for _, process := range session.Services {
		if failedNames[process.Name] {
			remaining = append(remaining, process)
		}
	}
	session.Services = remaining
	if err := releaseConnection(context.Background(), connection, workspace.Store.Root, false, output); err != nil {
		fmt.Fprintf(output, "%s: %v\n", style.Warning("Rollback warning for connection"), err)
		session.Connection = connection
		problems = append(problems, fmt.Errorf("stop connection: %w", err))
	} else {
		session.Connection = nil
	}
	if len(session.Services) == 0 && session.Connection == nil {
		if err := workspace.Store.Clear(); err != nil {
			problems = append(problems, err)
		}
	} else if err := workspace.Store.Save(session); err != nil {
		problems = append(problems, fmt.Errorf("preserve rollback state: %w", err))
	}
	return errors.Join(problems...)
}

func validateKubeconfig(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect kubeconfig %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("kubeconfig is not a regular file: %s", path)
	}
	return nil
}

func printKubeconfigPermissionWarning(output io.Writer, path string) {
	info, err := os.Stat(path)
	if err == nil && info.Mode().Perm()&0077 != 0 {
		style := terminal.New(output)
		fmt.Fprintln(output, style.Warning(fmt.Sprintf("Warning: kubeconfig permissions are %o; consider chmod 600 %s", info.Mode().Perm(), path)))
	}
}

func commandAvailable(command string, directory string) error {
	if strings.ContainsRune(command, filepath.Separator) {
		path := command
		if !filepath.IsAbs(path) {
			path = filepath.Join(directory, path)
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if info.Mode()&0111 == 0 {
			return fmt.Errorf("%s is not executable", path)
		}
		return nil
	}
	if _, err := exec.LookPath(command); err != nil {
		return err
	}
	return nil
}

func commandAvailableForRun(command string, service PlannedService, plan *Plan) error {
	if len(service.Prepare) > 0 && commandMayBeCreatedInRunWorkdir(command, service.RunWorkdir) {
		return nil
	}
	path := command
	if strings.ContainsRune(path, filepath.Separator) {
		if !filepath.IsAbs(path) {
			path = filepath.Join(service.RunWorkdir, path)
		}
		if !plan.ReuseCurrent && pathWithinDirectory(plan.RunDir, path) {
			return fmt.Errorf("%s will be removed by the fresh runtime reset", filepath.Clean(path))
		}
	}
	return commandAvailable(command, service.RunWorkdir)
}

func commandMayBeCreatedInRunWorkdir(command string, directory string) bool {
	if !strings.ContainsRune(command, filepath.Separator) {
		return false
	}
	path := command
	if !filepath.IsAbs(path) {
		path = filepath.Join(directory, path)
	}
	return filepath.Clean(path) != filepath.Clean(directory) && pathWithinDirectory(directory, path)
}
