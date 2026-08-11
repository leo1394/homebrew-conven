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

	"github.com/leo1394/homebrew-conven/internal/terminal"
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
			return nil, fmt.Errorf("workspace already has running services: %s; use conven services --restart or conven services --stop first", strings.Join(active, ", "))
		}
		for _, process := range existing.Services {
			if ProcessGroupAlive(process.PGID) {
				return nil, fmt.Errorf("workspace has an unverified process group for %s; use conven services --stop --all before starting a new session", process.Name)
			}
		}
		if existing.Connection != nil && existing.Connection.Owned && !existing.Connection.Managed && ProcessGroupAlive(existing.Connection.PGID) {
			return nil, fmt.Errorf("workspace has an active or unverified %s connection; use conven services --stop --all before starting a new session", existing.Connection.Driver)
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
				Cluster:     kubeconfigClusterName(plan.Connection.Kubeconfig),
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
		Cluster:     kubeconfigClusterName(plan.Connection.Kubeconfig),
		CreatedAt:   time.Now(),
		Selected:    append([]string(nil), plan.Selected...),
		Connection:  connection,
	}
	if err := workspace.Store.Save(session); err != nil {
		return nil, errors.Join(err, releaseConnection(context.Background(), connection, workspace.Store.Root, false, output))
	}
	if err := materializeRuntimeConfigs(ctx, plan, plan.Order, output); err != nil {
		return nil, failStartup(workspace, session, connection, output, err)
	}
	if err := runRuntimePreflight(ctx, plan, output, true); err != nil {
		return nil, failStartup(workspace, session, connection, output, err)
	}
	sourceFingerprints := make(map[string]string, len(plan.Order))
	planFingerprints := make(map[string]string, len(plan.Order))
	for _, name := range plan.Order {
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
		if len(service.Prepare) > 0 {
			fmt.Fprintf(output, "%s %s\n", style.Stage("Preparing"), style.Identifier(name))
			if _, err := checkBuildDiskSpace(workspace.Root); err != nil {
				return nil, failStartup(workspace, session, connection, output, fmt.Errorf("prepare %s: %w", name, err))
			}
			prepareLog := filepath.Join(plan.RunDir, "logs", name+"-prepare.log")
			if err := RunForeground(ctx, service.Prepare, service.Workdir, service.Environment, output, prepareLog); err != nil {
				return nil, failStartup(workspace, session, connection, output, fmt.Errorf("prepare %s: %w", name, err))
			}
		}
		if !options.SkipBuild && len(service.Build) > 0 {
			fmt.Fprintf(output, "%s %s\n", style.Stage("Building"), style.Identifier(name))
			if _, err := checkBuildDiskSpace(workspace.Root); err != nil {
				return nil, failStartup(workspace, session, connection, output, fmt.Errorf("build %s: %w", name, err))
			}
			buildLog := filepath.Join(plan.RunDir, "logs", name+"-build.log")
			if err := RunForeground(ctx, service.Build, service.Workdir, service.Environment, output, buildLog); err != nil {
				return nil, failStartup(workspace, session, connection, output, fmt.Errorf("build %s: %w", name, err))
			}
		}
		if err := inspectRunWorkdir(service); err != nil {
			return nil, failStartup(workspace, session, connection, output, err)
		}
	}
	if err := runRuntimePreflight(ctx, plan, output, false); err != nil {
		return nil, failStartup(workspace, session, connection, output, err)
	}
	started := make(map[string]ServiceProcess, len(plan.Order))
	for _, group := range plan.Groups {
		if len(group) > 1 {
			fmt.Fprintf(output, "%s: %s\n", style.Stage("Starting dependency cycle together"), style.Identifier(strings.Join(group, ", ")))
		}
		for _, name := range group {
			if err := ctx.Err(); err != nil {
				return nil, failStartup(workspace, session, connection, output, err)
			}
			service := plan.Services[name]
			if err := appendIsolationEvidence(service, plan.Connection); err != nil {
				return nil, failStartup(workspace, session, connection, output, fmt.Errorf("record %s local isolation: %w", name, err))
			}
			fmt.Fprintf(output, "%s %s\n", style.Stage("Starting"), style.Identifier(name))
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
					fmt.Fprintf(output, "%s %s; last log lines:\n", style.Failure("✗ Health check failed:"), style.Identifier(name))
					ShowLogs(context.Background(), session, []string{name}, false, output)
					return nil, failStartup(workspace, session, connection, output, err)
				}
				fmt.Fprintf(output, "%s %s\n", style.Success("✓ Healthy:"), style.Identifier(name))
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
	fmt.Fprintln(output, style.Success("✓ Local services are ready. Use `conven services --dashboard` or `conven services --logs --tail` to observe them."))
	return session, nil
}

func validateSkipBuild(plan *Plan) error {
	for _, name := range plan.Order {
		service := plan.Services[name]
		if len(service.Build) == 0 || !pathWithinDirectory(plan.RunDir, service.Artifact) {
			continue
		}
		return fmt.Errorf("--skip-build cannot reuse %s artifact %s because Conven resets the current runtime directory for a fresh start; set services.%s.runner.artifact to an absolute persistent path or omit --skip-build", name, service.Artifact, name)
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
	style := terminal.New(output)
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
		fmt.Fprintln(output, style.Warning("No Conven session found."))
		if all && force {
			recovered, err := recoverUnleasedConnections(ctx, workspace.Store.Root, output)
			if err != nil {
				return err
			}
			if recovered > 0 {
				fmt.Fprintln(output, style.Success(fmt.Sprintf("✓ Recovered %d unleased shared connection record(s).", recovered)))
			} else {
				fmt.Fprintln(output, style.Success("✓ No unleased shared connection records were recoverable."))
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
		fmt.Fprintf(output, "%s %s\n", style.Stage("Stopping"), style.Identifier(process.Name))
		stopErr := StopProcess(process, 10*time.Second)
		if stopErr != nil && force && ProcessGroupAlive(process.PGID) {
			fmt.Fprintf(output, "%s %s\n", style.Stage(fmt.Sprintf("Force stopping unverified process group %d for", process.PGID)), style.Identifier(process.Name))
			stopErr = ForceStopProcessGroup(process, 3*time.Second)
		}
		if stopErr != nil {
			fmt.Fprintf(output, "%s %s: %v\n", style.Failure("✗ Error stopping"), style.Identifier(process.Name), stopErr)
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
				fmt.Fprintf(output, "%s %s\n", style.Stage("Releasing connection lease"), style.Identifier(session.Connection.Driver))
			} else if session.Connection.Owned {
				fmt.Fprintf(output, "%s %s\n", style.Stage("Stopping Conven-owned connection"), style.Identifier(session.Connection.Driver))
			} else {
				fmt.Fprintf(output, "%s %s connection running; Conven does not own it.\n", style.Warning("Leaving external"), style.Identifier(session.Connection.Driver))
			}
			if err := releaseConnection(ctx, session.Connection, workspace.Store.Root, force, output); err != nil {
				fmt.Fprintf(output, "%s: %v\n", style.Failure("✗ Error releasing connection"), err)
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
	fmt.Fprintln(output, style.Stage("Conven status"))
	fmt.Fprintln(output, style.Detail("Runtime: "+workspace.Store.Root))
	fmt.Fprintln(output, style.Detail("Current: "+workspace.Store.CurrentDir))
	session, err := workspace.Store.Load()
	if err != nil {
		return err
	}
	if session == nil {
		fmt.Fprintln(output, style.Warning("No Conven session found."))
		_, err := printSharedConnectionStatus(ctx, output)
		return err
	}
	fmt.Fprintln(output, style.Detail("Workspace: "+session.Workspace))
	fmt.Fprintln(output, style.Detail("Environment: "+style.Identifier(session.Environment)))
	fmt.Fprintln(output, style.Stage("Services"))
	for _, process := range session.Services {
		state := "stopped"
		if ProcessAlive(process.PID) && VerifyProcess(process) == nil {
			state = "running"
		} else if ProcessGroupAlive(process.PGID) {
			state = "unverified"
		}
		fmt.Fprintln(output, style.Detail(fmt.Sprintf("%s: %s, pid=%d pgid=%d log=%s", style.Identifier(process.Name), styledProcessState(style, state, 0), process.PID, process.PGID, process.LogPath)))
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
			fmt.Fprintln(output, style.Detail(fmt.Sprintf("%s: %s, pid=%d pgid=%d log=%s", style.Identifier("connection/"+session.Connection.Driver), styledProcessState(style, state, 0), session.Connection.PID, session.Connection.PGID, session.Connection.LogPath)))
		} else {
			fmt.Fprintln(output, style.Detail(fmt.Sprintf("%s: %s", style.Identifier("connection/"+session.Connection.Driver), styledProcessState(style, state, 0))))
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
	fmt.Fprintln(output, style.Stage("Doctor"))
	fmt.Fprintln(output, style.Detail("Workspace: "+workspace.Root))
	fmt.Fprintln(output, style.Detail("Manifest: "+workspace.ConfigPath))
	fmt.Fprintln(output, style.Detail("Runtime: "+workspace.Store.Root))
	fmt.Fprintln(output, style.Detail("Current: "+workspace.Store.CurrentDir))
	fmt.Fprintln(output, style.Detail("Environment: "+style.Identifier(plan.EnvironmentName)))
	fmt.Fprintln(output, style.Detail(fmt.Sprintf("Services: %d", len(plan.Services))))
	fmt.Fprintln(output, style.Detail("Connection: "+style.Identifier(displayConnection(plan.Connection))))
	fmt.Fprintln(output, style.Success("✓ Doctor checks passed."))
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
	fmt.Fprintln(output, style.Stage("Available services"))
	for _, name := range names {
		service := workspace.Manifest.Services[name]
		fmt.Fprintln(output, style.Detail(fmt.Sprintf("%s: %s", style.Identifier(name), service.Path)))
	}
}

func printPlan(output io.Writer, plan *Plan, dryRun bool) {
	style := terminal.New(output)
	fmt.Fprintln(output, style.Stage("Service plan"))
	fmt.Fprintln(output, style.Detail("Workspace: "+plan.Workspace.Root))
	if plan.Workspace.Manifest != nil && plan.Workspace.Manifest.Workspace.Name != "" {
		fmt.Fprintln(output, style.Detail("Project: "+style.Identifier(plan.Workspace.Manifest.Workspace.Name)))
	}
	fmt.Fprintln(output, style.Detail("Runtime: "+plan.Workspace.Store.Root))
	fmt.Fprintln(output, style.Detail("Current: "+plan.RunDir))
	fmt.Fprintln(output, style.Detail("Environment: "+style.Identifier(plan.EnvironmentName)))
	fmt.Fprintln(output, style.Detail("Local services: "+style.Identifiers(plan.Selected, ", ")))
	if len(plan.DeclaredRemote) > 0 {
		fmt.Fprintln(output, style.Detail("Declared remote dependencies: "+style.Identifiers(plan.DeclaredRemote, ", ")))
	} else {
		fmt.Fprintln(output, style.Detail("Declared remote dependencies: none"))
	}
	groups := make([]string, 0, len(plan.Groups))
	for _, group := range plan.Groups {
		groups = append(groups, strings.Join(group, " + "))
	}
	fmt.Fprintln(output, style.Detail("Start groups: "+style.Identifier(strings.Join(groups, " -> "))))
	fmt.Fprintln(output, style.Detail("Connection: "+style.Identifier(displayConnection(plan.Connection))))
	for _, name := range plan.Order {
		service := plan.Services[name]
		if service.Config == nil {
			continue
		}
		fmt.Fprintf(output, "%s %s\n", style.Stage("Config"), style.Identifier(name))
		fmt.Fprintln(output, style.Detail(fmt.Sprintf("Drivers: policy=%s, framework=%s, source=%s, discovery=%s, materializer=%s",
			service.Config.Policy,
			service.Config.Framework,
			service.Config.Plan.SourceDriver,
			service.Config.Discovery,
			service.Config.Plan.Driver,
		)))
		fmt.Fprintln(output, style.Detail("Output: "+service.Config.Plan.TargetDir))
		if isolation := plannedIsolationDescription(service.Config); isolation != "" {
			fmt.Fprintln(output, style.Detail("Local isolation: "+isolation))
		}
		for _, route := range service.Config.Routes {
			location := "Remote"
			if route.Local {
				location = "Local"
			}
			mode := route.Mode
			if mode == "" {
				mode = "unconfigured"
			}
			fmt.Fprintln(output, style.Detail(fmt.Sprintf("%s route: %s via %s (%s)", location, style.Identifier(route.Dependency), style.Identifier(route.Binding), mode)))
		}
	}
	if dryRun {
		fmt.Fprintln(output, style.Success("✓ Dry run complete; no connection, config fetch/materialization, build, process, or state changes were made."))
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
