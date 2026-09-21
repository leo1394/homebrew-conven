package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/leo1394/homebrew-conven/internal/terminal"
)

type RestartOptions struct {
	ExpectedSessionToken string
	Common              CommonOptions
	Services            []string
	SkipBuild            bool
	SkipVerify           bool
	HotReloadExecutable string
	Output              io.Writer
}

func Restart(ctx context.Context, workspace *WorkspaceData, options RestartOptions) (result *Session, resultErr error) {
	output := options.Output
	if output == nil {
		output = io.Discard
	}
	style := terminal.New(output)
	unlock, err := workspace.Store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	diagnostics, err := beginDiagnostics(workspace, options.Common.Environment, options.Services)
	if err != nil {
		return nil, err
	}
	diagnosticStage := "restart"
	diagnosticService := ""
	var diagnosticPlan *Plan
	var diagnosticSession *Session
	diagnosticsFinalized := false
	if err := diagnostics.startStage("restart", ""); err != nil {
		return nil, fmt.Errorf("record restart stage: %w", err)
	}
	defer func() {
		if resultErr == nil || diagnosticsFinalized {
			return
		}
		if diagnosticErr := diagnostics.failStage(resultErr); diagnosticErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("record failed restart stage: %w", diagnosticErr))
		}
		logTail := diagnosticFailureLog(diagnosticPlan, diagnosticSession, diagnosticStage, diagnosticService)
		if diagnosticErr := diagnostics.fail(diagnosticStage, diagnosticService, resultErr, logTail); diagnosticErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("finish restart diagnostics: %w", diagnosticErr))
		}
		diagnosticsFinalized = true
	}()
	session, err := workspace.Store.Load()
	if err != nil {
		return nil, err
	}
	if session == nil || len(session.Services) == 0 {
		return nil, errors.New("no running Conven session found; use conven services --start first")
	}
	diagnosticSession = session
	previousAttemptID := session.AttemptID
	if options.ExpectedSessionToken != "" {
		token, tokenErr := replacementSessionToken(session)
		if tokenErr != nil || token != options.ExpectedSessionToken {
			return nil, errors.New("workspace session changed; refresh the dashboard before retrying")
		}
	}
	if err := workspace.Store.InspectCurrent(); err != nil {
		return nil, fmt.Errorf("inspect current runtime before restart: %w", err)
	}
	allNames := append([]string(nil), session.Selected...)
	processes := make(map[string]ServiceProcess, len(session.Services))
	for _, process := range session.Services {
		processes[process.Name] = process
	}
	if len(allNames) == 0 {
		for _, process := range session.Services {
			allNames = append(allNames, process.Name)
		}
	}
	options.Common.Environment = session.Environment
	diagnosticStage = "plan"
	plan, err := BuildRestartPlan(workspace, options.Common, allNames)
	if err != nil {
		return nil, err
	}
	diagnosticPlan = plan
	if err := diagnostics.setPlan(plan); err != nil {
		return nil, fmt.Errorf("record restart plan: %w", err)
	}
	if session.Connection != nil {
		plan.Connection = ConnectionConfig{Driver: session.Connection.Driver}
	}
	if err := validateInboundRouting(plan.Connection); err != nil {
		return nil, err
	}
	if err := validatePlanCommands(workspace, plan); err != nil {
		return nil, err
	}
	targets, sourceFingerprints, planFingerprints, err := restartTargets(plan, session, options.Services)
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		if err := ensureHotReloadWatcherLocked(workspace, session, options.HotReloadExecutable, output); err != nil {
			return nil, err
		}
		if err := diagnostics.completeStage(); err != nil {
			return nil, fmt.Errorf("record completed restart stage: %w", err)
		}
		if err := diagnostics.succeed(); err != nil {
			return nil, fmt.Errorf("finish successful restart diagnostics: %w", err)
		}
		diagnosticsFinalized = true
		fmt.Fprintln(output, style.Success("✓ No changed local services to restart."))
		return session, nil
	}
	if servicesNeedBuildDiskSpace(plan, targets, options.SkipBuild) {
		if err := checkBuildDiskSpaceAndWarn(output, workspace.Root); err != nil {
			return nil, err
		}
	}
	fmt.Fprintf(output, "%s: %s\n", style.Stage("Restarting local services"), style.Identifier(strings.Join(targets, ", ")))

	targetSet := make(map[string]bool, len(targets))
	for _, name := range targets {
		targetSet[name] = true
	}
	if err := materializeRuntimeConfigs(ctx, plan, targets, output); err != nil {
		return nil, err
	}
	if err := runRuntimePreflight(ctx, plan, output, true); err != nil {
		return nil, err
	}
	for _, name := range targets {
		diagnosticStage = "prepare"
		diagnosticService = name
		service := plan.Services[name]
		if len(service.Prepare) > 0 {
			fmt.Fprintf(output, "%s %s\n", style.Stage("Preparing"), style.Identifier(name))
			if _, err := checkBuildDiskSpace(workspace.Root); err != nil {
				return nil, fmt.Errorf("prepare %s: %w", name, err)
			}
			prepareLog := filepath.Join(plan.RunDir, "logs", name+"-prepare.log")
			if err := RunForeground(ctx, service.Prepare, service.Workdir, service.Environment, output, prepareLog); err != nil {
				return nil, fmt.Errorf("prepare %s: %w", name, err)
			}
		}
		if !options.SkipBuild && len(service.Build) > 0 {
			fmt.Fprintf(output, "%s %s\n", style.Stage("Building"), style.Identifier(name))
			if _, err := checkBuildDiskSpace(workspace.Root); err != nil {
				return nil, fmt.Errorf("build %s: %w", name, err)
			}
			buildLog := filepath.Join(plan.RunDir, "logs", name+"-build.log")
			if err := RunForeground(ctx, service.Build, service.Workdir, service.Environment, output, buildLog); err != nil {
				return nil, fmt.Errorf("build %s: %w", name, err)
			}
		}
		if err := inspectRunWorkdir(service); err != nil {
			return nil, err
		}
	}
	if err := runRuntimePreflight(ctx, plan, output, false); err != nil {
		return nil, err
	}
	registryBaselines := make(map[string]*RegistrySnapshot, len(targets))
	observeRuntime := !options.SkipVerify && workspace.Manifest.Version >= 3
	if observeRuntime {
		for _, name := range targets {
			service := plan.Services[name]
			baseline, err := snapshotServiceRegistry(ctx, workspace.Root, service)
			if err != nil {
				return nil, err
			}
			if err := rejectLocalRegistryEntries(service, baseline); err != nil {
				return nil, err
			}
			registryBaselines[name] = baseline
		}
	}
	if err := archiveSessionDiagnosticLogs(workspace, session, targets); err != nil {
		return nil, fmt.Errorf("archive previous restart logs: %w", err)
	}

	for index := len(plan.Order) - 1; index >= 0; index-- {
		name := plan.Order[index]
		if !targetSet[name] {
			continue
		}
		diagnosticStage = "stop"
		diagnosticService = name
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fmt.Fprintf(output, "%s %s for restart\n", style.Stage("Stopping"), style.Identifier(name))
		if err := StopProcess(processes[name], 10*time.Second); err != nil {
			return nil, fmt.Errorf("stop %s for restart: %w", name, err)
		}
		if err := preflightServicePorts(plan.Services[name]); err != nil {
			return nil, err
		}
	}

	for _, group := range plan.Groups {
		groupTargets := make([]string, 0, len(group))
		for _, name := range group {
			if targetSet[name] {
				groupTargets = append(groupTargets, name)
			}
		}
		if len(groupTargets) == 0 {
			continue
		}
		if len(groupTargets) > 1 {
			fmt.Fprintf(output, "%s: %s\n", style.Stage("Restarting dependency cycle together"), style.Identifier(strings.Join(groupTargets, ", ")))
		}
		started := make([]string, 0, len(groupTargets))
		for _, name := range groupTargets {
			diagnosticStage = "start"
			diagnosticService = name
			if err := ctx.Err(); err != nil {
				return nil, rollbackRestartGroup(workspace, session, processes, started, output, err)
			}
			service := plan.Services[name]
			service.LogPath = processes[name].LogPath
			if err := appendRestartMarker(service.LogPath); err != nil {
				return nil, rollbackRestartGroup(workspace, session, processes, started, output, err)
			}
			if err := appendIsolationEvidence(service, plan.Connection); err != nil {
				return nil, rollbackRestartGroup(workspace, session, processes, started, output, err)
			}
			fmt.Fprintf(output, "%s %s\n", style.Stage("Starting"), style.Identifier(name))
			process, err := StartService(name, service.Run, service.RunWorkdir, service.Environment, service.LogPath)
			if err != nil {
				return nil, rollbackRestartGroup(workspace, session, processes, started, output, err)
			}
			process.Ports = copyPorts(service.Ports)
			process.LogOffset = process.logOffset
			process.ConsumerIsolation = copyConsumerIsolation(service.ConsumerIsolation)
			if options.SkipVerify {
				process.Verification = "unverified(skip-verify)"
			} else {
				process.Verification = "started"
			}
			process.SourceFingerprint = processes[name].SourceFingerprint
			process.PlanFingerprint = processes[name].PlanFingerprint
			replaceSessionProcess(session, process)
			started = append(started, name)
			if err := workspace.Store.Save(session); err != nil {
				return nil, rollbackRestartGroup(workspace, session, processes, started, output, err)
			}
		}
		if !options.SkipVerify {
			for _, name := range groupTargets {
				diagnosticStage = "verify"
				diagnosticService = name
				service := plan.Services[name]
				process := sessionProcess(session, name)
				if err := WaitHealthyChecks(ctx, process, service.HealthChecks); err != nil {
					fmt.Fprintf(output, "%s %s; last log lines:\n", style.Failure("✗ Health check failed:"), style.Identifier(name))
					ShowLogs(context.Background(), session, []string{name}, false, output)
					return nil, rollbackRestartGroup(workspace, session, processes, started, output, diagnoseStartupFailure(err, process, service))
				}
				process.Verification = "healthy"
				replaceSessionProcess(session, process)
				if err := workspace.Store.Save(session); err != nil {
					return nil, rollbackRestartGroup(workspace, session, processes, started, output, err)
				}
				fmt.Fprintf(output, "%s %s\n", style.Success("✓ Healthy:"), style.Identifier(name))
				if observeRuntime {
					listeners, err := verifyServiceListeners(ctx, service, process)
					if err != nil {
						return nil, rollbackRestartGroup(workspace, session, processes, started, output, err)
					}
					process.Listeners = listeners
					process.Verification = "listener-verified"
					replaceSessionProcess(session, process)
					if err := workspace.Store.Save(session); err != nil {
						return nil, rollbackRestartGroup(workspace, session, processes, started, output, err)
					}
					registration, err := verifyServiceRegistry(ctx, workspace.Root, service, registryBaselines[name])
					if err != nil {
						return nil, rollbackRestartGroup(workspace, session, processes, started, output, err)
					}
					process.Registration = registration
					if registration != nil {
						process.Verification = "registry-verified"
						replaceSessionProcess(session, process)
						if err := workspace.Store.Save(session); err != nil {
							return nil, rollbackRestartGroup(workspace, session, processes, started, output, err)
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
				fmt.Fprintf(output, "%s %s\n", style.Success("✓ Runtime contract verified:"), style.Identifier(name))
			}
		}
		for _, name := range groupTargets {
			process := sessionProcess(session, name)
			process.SourceFingerprint = sourceFingerprints[name]
			process.PlanFingerprint = planFingerprints[name]
			replaceSessionProcess(session, process)
		}
		if err := workspace.Store.Save(session); err != nil {
			return nil, err
		}
	}
	if err := ensureHotReloadWatcherLocked(workspace, session, options.HotReloadExecutable, output); err != nil {
		return nil, err
	}
	diagnosticStage = "finalize"
	diagnosticService = ""
	session.AttemptID = diagnostics.attempt.ID
	session.HealthChecks = mergeSessionHealthChecks(session.HealthChecks, sessionHealthChecks(plan), targets)
	session.RuntimeRoutes = mergeSessionRuntimeRoutes(session.RuntimeRoutes, diagnosticRoutes(plan), targets)
	if err := workspace.Store.Save(session); err != nil {
		return nil, errors.Join(err, cleanupRestartAfterDiagnosticsFailure(workspace, session, targets, previousAttemptID, output))
	}
	if err := diagnostics.completeStage(); err != nil {
		failure := fmt.Errorf("record completed restart stage: %w", err)
		return nil, errors.Join(failure, cleanupRestartAfterDiagnosticsFailure(workspace, session, targets, previousAttemptID, output))
	}
	if err := diagnostics.captureLogs(session, targets); err != nil {
		failure := fmt.Errorf("archive successful restart logs: %w", err)
		return nil, errors.Join(failure, cleanupRestartAfterDiagnosticsFailure(workspace, session, targets, previousAttemptID, output))
	}
	if err := diagnostics.succeed(); err != nil {
		failure := fmt.Errorf("finish successful restart diagnostics: %w", err)
		return nil, errors.Join(failure, cleanupRestartAfterDiagnosticsFailure(workspace, session, targets, previousAttemptID, output))
	}
	diagnosticsFinalized = true
	fmt.Fprintln(output, style.Success("✓ Changed local services were restarted."))
	return session, nil
}

func cleanupRestartAfterDiagnosticsFailure(workspace *WorkspaceData, session *Session, targets []string, previousAttemptID string, output io.Writer) error {
	problems := make([]error, 0)
	if err := stopHotReloadWatcherLocked(session, false, output); err != nil {
		problems = append(problems, fmt.Errorf("stop hot reload watcher after diagnostics failure: %w", err))
	}
	targetSet := make(map[string]bool, len(targets))
	for _, name := range targets {
		targetSet[name] = true
	}
	remaining := make([]ServiceProcess, 0, len(session.Services))
	for _, process := range session.Services {
		if !targetSet[process.Name] {
			remaining = append(remaining, process)
			continue
		}
		if err := StopProcess(process, 3*time.Second); err != nil {
			remaining = append(remaining, process)
			problems = append(problems, fmt.Errorf("stop restarted %s after diagnostics failure: %w", process.Name, err))
		}
	}
	session.Services = remaining
	session.Selected = filterSelectedServices(session.Selected, remaining)
	session.AttemptID = previousAttemptID
	session.HealthChecks = filterSessionHealthChecks(session.HealthChecks, targetSet)
	session.RuntimeRoutes = filterSessionRuntimeRoutes(session.RuntimeRoutes, targetSet)
	if len(session.Services) == 0 && session.HotReload == nil && session.Connection == nil {
		if err := workspace.Store.Clear(); err != nil {
			problems = append(problems, err)
		}
	} else if err := workspace.Store.Save(session); err != nil {
		problems = append(problems, fmt.Errorf("preserve restart cleanup state: %w", err))
	}
	return errors.Join(problems...)
}

func restartTargets(plan *Plan, session *Session, requested []string) ([]string, map[string]string, map[string]string, error) {
	requestedSet := make(map[string]bool, len(requested))
	for _, name := range requested {
		if requestedSet[name] {
			return nil, nil, nil, fmt.Errorf("duplicate service %q", name)
		}
		if _, found := plan.Services[name]; !found {
			return nil, nil, nil, fmt.Errorf("service %q is not part of the current session", name)
		}
		requestedSet[name] = true
	}
	processes := make(map[string]ServiceProcess, len(session.Services))
	for _, process := range session.Services {
		processes[process.Name] = process
	}
	targets := make([]string, 0)
	sourceFingerprints := make(map[string]string)
	planFingerprints := make(map[string]string)
	for _, name := range plan.Order {
		process := processes[name]
		if len(requestedSet) == 0 {
			if ProcessAlive(process.PID) {
				if err := VerifyProcess(process); err != nil {
					return nil, nil, nil, fmt.Errorf("inspect %s before restart: %w", name, err)
				}
			} else if ProcessGroupAlive(process.PGID) {
				return nil, nil, nil, fmt.Errorf("refusing to restart %s: leader pid %d exited while process group %d is still active; inspect conven services --status and recover with conven services --stop --force", name, process.PID, process.PGID)
			}
		}
		if len(requestedSet) > 0 && !requestedSet[name] {
			continue
		}
		service := plan.Services[name]
		sourceFingerprint, err := SourceFingerprint(service.Directory)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("fingerprint %s source: %w", name, err)
		}
		planFingerprint, err := PlanFingerprint(service)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("fingerprint %s plan: %w", name, err)
		}
		sourceFingerprints[name] = sourceFingerprint
		planFingerprints[name] = planFingerprint
		changed := len(requestedSet) > 0 || !ProcessAlive(process.PID) ||
			process.SourceFingerprint == "" || process.SourceFingerprint != sourceFingerprint ||
			process.PlanFingerprint == "" || process.PlanFingerprint != planFingerprint
		if changed {
			targets = append(targets, name)
		}
	}
	return targets, sourceFingerprints, planFingerprints, nil
}

func rollbackRestartGroup(workspace *WorkspaceData, session *Session, originals map[string]ServiceProcess, started []string, output io.Writer, failure error) error {
	problems := []error{failure}
	for index := len(started) - 1; index >= 0; index-- {
		name := started[index]
		process := sessionProcess(session, name)
		if err := StopProcess(process, 3*time.Second); err != nil {
			terminal.PrintWarningBlock(output, "Restart rollback could not stop a service.", []string{
				"Service: " + name,
				"Error: " + err.Error(),
			}, nil)
			problems = append(problems, fmt.Errorf("stop restarted %s: %w", name, err))
			continue
		}
		replaceSessionProcess(session, originals[name])
	}
	if err := workspace.Store.Save(session); err != nil {
		problems = append(problems, fmt.Errorf("preserve restart rollback state: %w", err))
	}
	return errors.Join(problems...)
}

func replaceSessionProcess(session *Session, replacement ServiceProcess) {
	for index := range session.Services {
		if session.Services[index].Name == replacement.Name {
			session.Services[index] = replacement
			return
		}
	}
}

func sessionProcess(session *Session, name string) ServiceProcess {
	for _, process := range session.Services {
		if process.Name == name {
			return process
		}
	}
	return ServiceProcess{Name: name}
}

func appendRestartMarker(path string) error {
	file, err := openLog(path)
	if err != nil {
		return err
	}
	_, writeErr := fmt.Fprintf(file, "\n--- conven services --restart %s ---\n", time.Now().Format(time.RFC3339))
	closeErr := file.Close()
	return errors.Join(writeErr, closeErr)
}
