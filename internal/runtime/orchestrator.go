package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/leo1394/homebrew-conven/internal/dependency"
	"github.com/leo1394/homebrew-conven/internal/model"
	"github.com/leo1394/homebrew-conven/internal/terminal"
)

type StartOptions struct {
	Common              CommonOptions
	Services            []string
	DryRun              bool
	SkipBuild            bool
	SkipVerify           bool
	HotReloadExecutable string
	Output              io.Writer
}

type RunningServicesError struct {
	Services     []string
	SessionToken string
}

func (err *RunningServicesError) Error() string {
	return fmt.Sprintf("workspace already has running services: %s; use conven services --restart or conven services --stop first", strings.Join(err.Services, ", "))
}

func Start(ctx context.Context, workspace *WorkspaceData, options StartOptions) (*Session, error) {
	return start(ctx, workspace, options, "")
}

func ReplaceStart(ctx context.Context, workspace *WorkspaceData, options StartOptions, expectedSessionToken string) (*Session, error) {
	if strings.TrimSpace(expectedSessionToken) == "" {
		return nil, errors.New("replacement start requires the running session confirmation token")
	}
	return start(ctx, workspace, options, expectedSessionToken)
}

func start(ctx context.Context, workspace *WorkspaceData, options StartOptions, expectedSessionToken string) (*Session, error) {
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
	active, err := inspectSessionServicesForStart(existing)
	if err != nil {
		return nil, err
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
	if len(active) > 0 {
		if err := validateConnectionForReplacement(ctx, existing.Connection, workspace.Store.Root); err != nil {
			return nil, err
		}
		token, err := replacementSessionToken(existing)
		if err != nil {
			return nil, err
		}
		if expectedSessionToken == "" {
			return nil, &RunningServicesError{Services: active, SessionToken: token}
		}
		if expectedSessionToken != token {
			return nil, errors.New("workspace session changed while awaiting replacement confirmation; retry conven services --start")
		}
	} else if expectedSessionToken != "" {
		return nil, errors.New("workspace session changed while awaiting replacement confirmation; retry conven services --start")
	}
	retainedConnection := false
	var retainedConnectionSnapshot *ConnectionProcess
	if expectedSessionToken != "" {
		retainedConnection, err = renewRetainedKtctlConnection(ctx, existing.Connection, plan.Connection, workspace.Store.Root)
		if err != nil {
			return nil, err
		}
		if retainedConnection {
			snapshot := *existing.Connection
			snapshot.Command = append([]string(nil), existing.Connection.Command...)
			retainedConnectionSnapshot = &snapshot
		}
		if err := stopHotReloadWatcherLocked(existing, false, output); err != nil {
			return nil, fmt.Errorf("stop hot reload watcher for fresh start: %w", err)
		}
		if err := stopSessionServicesForReplacement(workspace, existing, output); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if existing != nil {
		if existing.Connection != nil && !retainedConnection {
			if err := releaseConnection(context.Background(), existing.Connection, workspace.Store.Root, false, output); err != nil {
				return nil, fmt.Errorf("release previous workspace connection before replacing stale session: %w", err)
			}
			existing.Connection = nil
		}
		if !retainedConnection {
			if err := workspace.Store.Clear(); err != nil {
				return nil, err
			}
		}
	}
	if err := workspace.Store.ResetCurrent(); err != nil {
		return nil, err
	}
	printPlan(output, plan, false)
	endpointNames := dependency.EndpointNames(plan.Resolutions)
	if len(endpointNames) > 0 {
		fmt.Fprintln(output, style.Stage("Checking endpoints"))
		fmt.Fprintln(output, style.Detail(style.Identifiers(endpointNames, ", ")))
		if err := dependency.CheckEndpoints(ctx, workspace.Root, CommandEnvironment(plan.Environment.Env), plan.Environment, plan.Resolutions); err != nil {
			return nil, err
		}
	}
	connection, err := EnsureConnection(ctx, plan.Connection, ConnectionLogPath(workspace.Store.Root), workspace.Store.Root, output)
	if retainedConnection && !sameConnectionProcess(retainedConnectionSnapshot, connection) {
		retainedConnection = false
	}
	if err != nil {
		if retainedConnection {
			return nil, err
		}
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
	if retainedConnection {
		fmt.Fprintln(output, style.Success("✓ Keeping the usable managed ktctl connection lease for the fresh start."))
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
		if retainedConnection {
			return nil, err
		}
		return nil, errors.Join(err, releaseConnection(context.Background(), connection, workspace.Store.Root, false, output))
	}
	fail := func(failure error) error {
		return failStartup(workspace, session, connection, retainedConnection, output, failure)
	}
	if err := materializeRuntimeConfigs(ctx, plan, plan.Order, output); err != nil {
		return nil, fail(err)
	}
	if err := runRuntimePreflight(ctx, plan, output, true); err != nil {
		return nil, fail(err)
	}
	sourceFingerprints := make(map[string]string, len(plan.Order))
	planFingerprints := make(map[string]string, len(plan.Order))
	for _, name := range plan.Order {
		if err := ctx.Err(); err != nil {
			return nil, fail(err)
		}
		service := plan.Services[name]
		sourceFingerprint, err := SourceFingerprint(service.Directory)
		if err != nil {
			return nil, fail(fmt.Errorf("fingerprint %s source: %w", name, err))
		}
		planFingerprint, err := PlanFingerprint(service)
		if err != nil {
			return nil, fail(fmt.Errorf("fingerprint %s plan: %w", name, err))
		}
		sourceFingerprints[name] = sourceFingerprint
		planFingerprints[name] = planFingerprint
		if len(service.Prepare) > 0 {
			fmt.Fprintf(output, "%s %s\n", style.Stage("Preparing"), style.Identifier(name))
			if _, err := checkBuildDiskSpace(workspace.Root); err != nil {
				return nil, fail(fmt.Errorf("prepare %s: %w", name, err))
			}
			prepareLog := filepath.Join(plan.RunDir, "logs", name+"-prepare.log")
			if err := RunForeground(ctx, service.Prepare, service.Workdir, service.Environment, output, prepareLog); err != nil {
				return nil, fail(fmt.Errorf("prepare %s: %w", name, err))
			}
		}
		if !options.SkipBuild && len(service.Build) > 0 {
			fmt.Fprintf(output, "%s %s\n", style.Stage("Building"), style.Identifier(name))
			if _, err := checkBuildDiskSpace(workspace.Root); err != nil {
				return nil, fail(fmt.Errorf("build %s: %w", name, err))
			}
			buildLog := filepath.Join(plan.RunDir, "logs", name+"-build.log")
			if err := RunForeground(ctx, service.Build, service.Workdir, service.Environment, output, buildLog); err != nil {
				return nil, fail(fmt.Errorf("build %s: %w", name, err))
			}
		}
		if err := inspectRunWorkdir(service); err != nil {
			return nil, fail(err)
		}
	}
	if err := runRuntimePreflight(ctx, plan, output, false); err != nil {
		return nil, fail(err)
	}
	started := make(map[string]ServiceProcess, len(plan.Order))
	observeRuntime := !options.SkipVerify && workspace.Manifest.Version >= 3
	for _, group := range plan.Groups {
		registryBaselines := make(map[string]*RegistrySnapshot, len(group))
		for _, name := range group {
			service := plan.Services[name]
			if err := preflightServicePorts(service); err != nil {
				return nil, fail(err)
			}
			if observeRuntime {
				baseline, err := snapshotServiceRegistry(ctx, workspace.Root, service)
				if err != nil {
					return nil, fail(err)
				}
				if err := rejectLocalRegistryEntries(service, baseline); err != nil {
					return nil, fail(err)
				}
				registryBaselines[name] = baseline
			}
		}
		if len(group) > 1 {
			fmt.Fprintf(output, "%s: %s\n", style.Stage("Starting dependency cycle together"), style.Identifier(strings.Join(group, ", ")))
		}
		for _, name := range group {
			if err := ctx.Err(); err != nil {
				return nil, fail(err)
			}
			service := plan.Services[name]
			if err := appendIsolationEvidence(service, plan.Connection); err != nil {
				return nil, fail(fmt.Errorf("record %s local isolation: %w", name, err))
			}
			fmt.Fprintf(output, "%s %s\n", style.Stage("Starting"), style.Identifier(name))
			process, err := StartService(name, service.Run, service.RunWorkdir, service.Environment, service.LogPath)
			if err != nil {
				return nil, fail(err)
			}
			process.Ports = copyPorts(service.Ports)
			process.ConsumerIsolation = copyConsumerIsolation(service.ConsumerIsolation)
			if options.SkipVerify {
				process.Verification = "unverified(skip-verify)"
			} else {
				process.Verification = "started"
			}
			started[name] = process
			session.Services = append(session.Services, process)
			if err := workspace.Store.Save(session); err != nil {
				return nil, fail(err)
			}
		}
		if !options.SkipVerify {
			for _, name := range group {
				service := plan.Services[name]
				process := started[name]
				if err := WaitHealthyChecks(ctx, process, service.HealthChecks); err != nil {
					fmt.Fprintf(output, "%s %s; last log lines:\n", style.Failure("✗ Health check failed:"), style.Identifier(name))
					ShowLogs(context.Background(), session, []string{name}, false, output)
					return nil, fail(err)
				}
				process.Verification = "healthy"
				replaceSessionProcess(session, process)
				started[name] = process
				if err := workspace.Store.Save(session); err != nil {
					return nil, fail(err)
				}
				fmt.Fprintf(output, "%s %s\n", style.Success("✓ Healthy:"), style.Identifier(name))
				if observeRuntime {
					listeners, err := verifyServiceListeners(ctx, service, process)
					if err != nil {
						return nil, fail(err)
					}
					process.Listeners = listeners
					process.Verification = "listener-verified"
					replaceSessionProcess(session, process)
					started[name] = process
					if err := workspace.Store.Save(session); err != nil {
						return nil, fail(err)
					}
					registration, err := verifyServiceRegistry(ctx, workspace.Root, service, registryBaselines[name])
					if err != nil {
						return nil, fail(err)
					}
					process.Registration = registration
					if registration != nil {
						process.Verification = "registry-verified"
						replaceSessionProcess(session, process)
						started[name] = process
						if err := workspace.Store.Save(session); err != nil {
							return nil, fail(err)
						}
					}
					if service.Runtime == "generic-runner" || len(service.Kinds) == 0 {
						process.Verification = "runner-only"
					} else {
						process.Verification = "verified"
					}
				} else {
					process.Verification = "verified"
				}
				replaceSessionProcess(session, process)
				started[name] = process
				fmt.Fprintf(output, "%s %s\n", style.Success("✓ Runtime contract verified:"), style.Identifier(name))
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, fail(err)
		}
		for _, name := range group {
			process := sessionProcess(session, name)
			process.SourceFingerprint = sourceFingerprints[name]
			process.PlanFingerprint = planFingerprints[name]
			replaceSessionProcess(session, process)
			started[name] = process
		}
		if err := workspace.Store.Save(session); err != nil {
			return nil, fail(err)
		}
	}
	for _, process := range session.Services {
		if !ProcessAlive(process.PID) || VerifyProcess(process) != nil {
			return nil, fail(fmt.Errorf("%s exited or changed identity during startup", process.Name))
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, fail(err)
	}
	if err := workspace.Store.Save(session); err != nil {
		return nil, fail(err)
	}
	if err := ensureHotReloadWatcherLocked(workspace, session, options.HotReloadExecutable, output); err != nil {
		return nil, fail(err)
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
		terminal.PrintWarningBlock(output, "Low disk space for service builds.", []string{
			"Path: " + path,
			"Available: " + formatDiskSpace(available),
			"Recommended minimum: " + formatDiskSpace(warningBuildDiskSpace),
			"Free disk space to avoid dependency download and build failures.",
		}, nil)
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
	if all {
		snapshot, err := workspace.Store.Load()
		if err != nil {
			return err
		}
		if err := stopHotReloadWatcherLocked(snapshot, force, io.Discard); err != nil {
			return fmt.Errorf("stop hot reload watcher before stopping all services: %w", err)
		}
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
		terminal.PrintWarningBlock(output, "No Conven session found.", nil, nil)
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
	if all {
		if err := stopHotReloadWatcherLocked(session, force, output); err != nil {
			return fmt.Errorf("stop hot reload watcher: %w", err)
		}
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
		if err := stopHotReloadWatcherLocked(session, force, output); err != nil {
			fmt.Fprintf(output, "%s: %v\n", style.Failure("✗ Error stopping hot reload watcher"), err)
			failed = append(failed, hotReloadProcessName)
		}
		if session.Connection != nil {
			if session.Connection.Managed {
				fmt.Fprintf(output, "%s %s\n", style.Stage("Releasing connection lease"), style.Identifier(session.Connection.Driver))
			} else if session.Connection.Owned {
				fmt.Fprintf(output, "%s %s\n", style.Stage("Stopping Conven-owned connection"), style.Identifier(session.Connection.Driver))
			} else {
				terminal.PrintWarningBlock(output, "External connection was left running.", []string{
					"Connection: " + session.Connection.Driver,
					"Reason: Conven does not own it.",
				}, nil)
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
	if len(session.Services) == 0 && session.HotReload == nil && session.Connection == nil {
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
	return printStatus(ctx, workspace, output, true)
}

func WorkspaceStatus(ctx context.Context, workspace *WorkspaceData, output io.Writer) error {
	return printStatus(ctx, workspace, output, false)
}

func printStatus(ctx context.Context, workspace *WorkspaceData, output io.Writer, detailed bool) error {
	style := terminal.New(output)
	if detailed {
		fmt.Fprintln(output, style.Stage("Conven status"))
		fmt.Fprintln(output, style.Detail("Runtime: "+workspace.Store.Root))
		fmt.Fprintln(output, style.Detail("Current: "+workspace.Store.CurrentDir))
	}
	session, err := workspace.Store.Load()
	if err != nil {
		return err
	}
	if session == nil {
		terminal.PrintWarningBlock(output, "No Conven session found.", nil, nil)
		_, err := printSharedConnectionStatus(ctx, output)
		return err
	}
	fmt.Fprintln(output, style.Detail("Workspace: "+session.Workspace))
	fmt.Fprintln(output, style.Detail("Environment: "+style.Identifier(session.Environment)))
	if session.HotReload != nil {
		state := "stopped"
		if ProcessAlive(session.HotReload.PID) && VerifyProcess(*session.HotReload) == nil {
			state = "watching"
		} else if ProcessGroupAlive(session.HotReload.PGID) {
			state = "unverified"
		}
		fmt.Fprintln(output, style.Detail(fmt.Sprintf("Hot reload: %s, pid=%d log=%s", styledProcessState(style, state, 0), session.HotReload.PID, session.HotReload.LogPath)))
	}
	fmt.Fprintln(output, style.Stage("Services"))
	for _, process := range session.Services {
		state := "stopped"
		if ProcessAlive(process.PID) && VerifyProcess(process) == nil {
			state = "running"
		} else if ProcessGroupAlive(process.PGID) {
			state = "unverified"
		}
		verification := process.Verification
		if verification == "" {
			verification = "unverified"
		}
		fmt.Fprintln(output, style.Detail(fmt.Sprintf("%s: %s, verification=%s, pid=%d pgid=%d log=%s", style.Identifier(process.Name), styledProcessState(style, state, 0), verification, process.PID, process.PGID, process.LogPath)))
		listenerNames := make([]string, 0, len(process.Listeners))
		for name := range process.Listeners {
			listenerNames = append(listenerNames, name)
		}
		sort.Strings(listenerNames)
		for _, name := range listenerNames {
			listener := process.Listeners[name]
			fmt.Fprintln(output, style.Detail(fmt.Sprintf("  listener %s: verified, address=%s, port=%d, scope=%s, owner-pid=%d, verified-at=%s", style.Identifier(name), listener.Address, listener.Port, listener.Mode, listener.OwnerPID, listener.VerifiedAt.Format(time.RFC3339))))
		}
		service, declared := workspace.Manifest.Services[process.Name]
		if declared {
			kinds := service.EffectiveKinds()
			for _, name := range kinds {
				if _, found := process.Listeners[name]; found {
					continue
				}
				listenerState := process.Verification
				if listenerState == "" || listenerState == "runner-only" {
					listenerState = "unverified"
				}
				fmt.Fprintln(output, style.Detail(fmt.Sprintf("  listener %s: %s, port=%d, scope=%s, verified-at=-", style.Identifier(name), listenerState, service.Ports[name], service.Network.EffectiveListen())))
			}
		}
		if process.Registration != nil {
			registration := process.Registration
			fmt.Fprintln(output, style.Detail(fmt.Sprintf("  registry %s: %s, identity=%s, verified-at=%s", style.Identifier(registration.Registry), registration.Status, registration.Identity, registration.VerifiedAt.Format(time.RFC3339))))
		}
		consumerNames := make([]string, 0, len(process.ConsumerIsolation))
		for name := range process.ConsumerIsolation {
			consumerNames = append(consumerNames, name)
		}
		sort.Strings(consumerNames)
		for _, name := range consumerNames {
			consumer := process.ConsumerIsolation[name]
			fmt.Fprintln(output, style.Detail(fmt.Sprintf("  consumer %s: %s, mode=%s, env=%s", style.Identifier(name), consumer.Status, consumer.Mode, consumer.Env)))
		}
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
	case "running", "watching", "reused", "shared":
		return style.Success(display)
	case "stopped":
		return style.Failure(display)
	case "unverified", "unverified(skip-verify)":
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
	if len(names) == 0 {
		environmentName := options.Environment
		if environmentName == "" {
			var err error
			environmentName, err = defaultEnvironmentName(workspace.Manifest)
			if err != nil {
				return err
			}
		}
		environment, found := workspace.Manifest.Environments[environmentName]
		if !found {
			return fmt.Errorf("environment %q is not declared", environmentName)
		}
		fmt.Fprintln(output, style.Stage("Doctor"))
		fmt.Fprintln(output, style.Detail("Workspace: "+workspace.Root))
		fmt.Fprintln(output, style.Detail("Manifest: "+workspace.ConfigPath))
		fmt.Fprintln(output, style.Detail("Runtime: "+workspace.Store.Root))
		fmt.Fprintln(output, style.Detail("Environment: "+style.Identifier(environmentName)))
		fmt.Fprintln(output, style.Detail("Services: 0"))
		fmt.Fprintln(output, style.Detail("Connection: "+style.Identifier(displayConnection(ConnectionConfig{Driver: environment.Connection.Driver}))))
		fmt.Fprintln(output, style.Success("✓ Doctor checks passed."))
		return nil
	}
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
		for _, health := range service.HealthChecks {
			if health.Type == "command" && len(health.Command) > 0 {
				if err := commandAvailableForRun(health.Command[0], service, plan); err != nil {
					return fmt.Errorf("%s health command: %w", name, err)
				}
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
	owners := make([]string, 0, len(plan.Resolutions))
	for owner := range plan.Resolutions {
		owners = append(owners, owner)
	}
	sort.Strings(owners)
	for _, owner := range owners {
		aliases := make([]string, 0, len(plan.Resolutions[owner]))
		for alias := range plan.Resolutions[owner] {
			aliases = append(aliases, alias)
		}
		sort.Strings(aliases)
		for _, alias := range aliases {
			resolution := plan.Resolutions[owner][alias]
			target := resolution.Target
			if target == "" {
				target = "-"
			}
			fmt.Fprintln(output, style.Detail(fmt.Sprintf("Dependency route: %s.%s -> %s:%s", owner, alias, resolution.Mode, target)))
		}
	}
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
		if isolation := consumerIsolationDescription(service.ConsumerIsolation); isolation != "" {
			fmt.Fprintln(output, style.Detail("Consumer guard: "+isolation))
		}
		if service.Config.Isolation.ListenerMode == model.NetworkListenAllInterfaces {
			fmt.Fprintln(output, style.Warning("Warning: "+name+" listens on 0.0.0.0 across all network interfaces; LAN access is still controlled by the host firewall."))
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

func inspectSessionServicesForStart(session *Session) ([]string, error) {
	if session == nil {
		return nil, nil
	}
	active := make([]string, 0, len(session.Services))
	for _, process := range session.Services {
		if ProcessAlive(process.PID) {
			if err := VerifyProcess(process); err != nil {
				return nil, fmt.Errorf("workspace has an unverified process for %s: %w; use conven services --stop --all before starting a new session", process.Name, err)
			}
			active = append(active, process.Name)
			continue
		}
		if ProcessGroupAlive(process.PGID) {
			return nil, fmt.Errorf("workspace has an unverified process group for %s; use conven services --stop --all before starting a new session", process.Name)
		}
	}
	if session.Connection != nil && session.Connection.Owned && !session.Connection.Managed && !connectionProcessAlive(session.Connection.PID) && ProcessGroupAlive(session.Connection.PGID) {
		return nil, fmt.Errorf("workspace has an active or unverified %s connection; use conven services --stop --all before starting a new session", session.Connection.Driver)
	}
	sort.Strings(active)
	return active, nil
}

func replacementSessionToken(session *Session) (string, error) {
	data, err := json.Marshal(session)
	if err != nil {
		return "", fmt.Errorf("encode running session confirmation token: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func stopSessionServicesForReplacement(workspace *WorkspaceData, session *Session, output io.Writer) error {
	style := terminal.New(output)
	failed := make(map[string]bool)
	for index := len(session.Services) - 1; index >= 0; index-- {
		process := session.Services[index]
		fmt.Fprintf(output, "%s %s for fresh start\n", style.Stage("Stopping"), style.Identifier(process.Name))
		if err := StopProcess(process, 10*time.Second); err != nil {
			fmt.Fprintf(output, "%s %s: %v\n", style.Failure("✗ Error stopping"), style.Identifier(process.Name), err)
			failed[process.Name] = true
		}
	}
	remaining := make([]ServiceProcess, 0, len(failed))
	for _, process := range session.Services {
		if failed[process.Name] {
			remaining = append(remaining, process)
		}
	}
	session.Services = remaining
	session.Selected = filterSelectedServices(session.Selected, remaining)
	if err := workspace.Store.Save(session); err != nil {
		return fmt.Errorf("preserve session after stopping services for fresh start: %w", err)
	}
	if len(failed) > 0 {
		names := make([]string, 0, len(failed))
		for name := range failed {
			names = append(names, name)
		}
		sort.Strings(names)
		return fmt.Errorf("failed to stop running services for fresh start: %s; replacement start was not attempted", strings.Join(names, ", "))
	}
	return nil
}

func filterSelectedServices(selected []string, services []ServiceProcess) []string {
	remaining := make(map[string]bool, len(services))
	for _, process := range services {
		remaining[process.Name] = true
	}
	filtered := make([]string, 0, len(services))
	for _, name := range selected {
		if remaining[name] {
			filtered = append(filtered, name)
			delete(remaining, name)
		}
	}
	for _, process := range services {
		if remaining[process.Name] {
			filtered = append(filtered, process.Name)
			delete(remaining, process.Name)
		}
	}
	return filtered
}

func failStartup(workspace *WorkspaceData, session *Session, connection *ConnectionProcess, retainConnection bool, output io.Writer, failure error) error {
	if err := rollbackSession(workspace, session, connection, retainConnection, output); err != nil {
		return errors.Join(failure, fmt.Errorf("startup rollback incomplete: %w", err))
	}
	return failure
}

func rollbackSession(workspace *WorkspaceData, session *Session, connection *ConnectionProcess, retainConnection bool, output io.Writer) error {
	style := terminal.New(output)
	problems := make([]error, 0)
	if err := stopHotReloadWatcherLocked(session, false, output); err != nil {
		problems = append(problems, fmt.Errorf("stop hot reload watcher: %w", err))
	}
	if len(session.Services) > 0 {
		fmt.Fprintln(output, style.Failure("Startup failed; stopping services started by this command."))
	}
	failedNames := make(map[string]bool)
	for index := len(session.Services) - 1; index >= 0; index-- {
		process := session.Services[index]
		if err := StopProcess(process, 3*time.Second); err != nil {
			terminal.PrintWarningBlock(output, "Startup rollback could not stop a service.", []string{
				"Service: " + process.Name,
				"Error: " + err.Error(),
			}, nil)
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
	session.Selected = filterSelectedServices(session.Selected, remaining)
	if retainConnection {
		session.Connection = connection
	} else {
		if err := releaseConnection(context.Background(), connection, workspace.Store.Root, false, output); err != nil {
			terminal.PrintWarningBlock(output, "Startup rollback could not release the connection.", []string{
				"Error: " + err.Error(),
			}, nil)
			session.Connection = connection
			problems = append(problems, fmt.Errorf("stop connection: %w", err))
		} else {
			session.Connection = nil
		}
	}
	if len(session.Services) == 0 && session.HotReload == nil && session.Connection == nil {
		if err := workspace.Store.Clear(); err != nil {
			problems = append(problems, err)
		}
	} else {
		if err := workspace.Store.Save(session); err != nil {
			problems = append(problems, fmt.Errorf("preserve rollback state: %w", err))
		} else if retainConnection {
			fmt.Fprintln(output, style.Success("✓ Pre-existing managed ktctl connection lease was kept after replacement startup failed."))
		}
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
		terminal.PrintWarningBlock(output, "Kubeconfig permissions are too broad.", []string{
			fmt.Sprintf("Permissions: %o", info.Mode().Perm()),
			"Path: " + path,
		}, []string{
			"Restrict kubeconfig permissions to 600.",
		})
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
