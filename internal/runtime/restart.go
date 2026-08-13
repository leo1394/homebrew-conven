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
	Common     CommonOptions
	Services   []string
	SkipBuild  bool
	SkipVerify bool
	Output     io.Writer
}

func Restart(ctx context.Context, workspace *WorkspaceData, options RestartOptions) (*Session, error) {
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
	session, err := workspace.Store.Load()
	if err != nil {
		return nil, err
	}
	if session == nil || len(session.Services) == 0 {
		return nil, errors.New("no running Conven session found; use conven services --start first")
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
	plan, err := BuildRestartPlan(workspace, options.Common, allNames)
	if err != nil {
		return nil, err
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

	for index := len(plan.Order) - 1; index >= 0; index-- {
		name := plan.Order[index]
		if !targetSet[name] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fmt.Fprintf(output, "%s %s for restart\n", style.Stage("Stopping"), style.Identifier(name))
		if err := StopProcess(processes[name], 10*time.Second); err != nil {
			return nil, fmt.Errorf("stop %s for restart: %w", name, err)
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
				service := plan.Services[name]
				process := sessionProcess(session, name)
				if err := WaitHealthy(ctx, process, service.Health); err != nil {
					fmt.Fprintf(output, "%s %s; last log lines:\n", style.Failure("✗ Health check failed:"), style.Identifier(name))
					ShowLogs(context.Background(), session, []string{name}, false, output)
					return nil, rollbackRestartGroup(workspace, session, processes, started, output, err)
				}
				fmt.Fprintf(output, "%s %s\n", style.Success("✓ Healthy:"), style.Identifier(name))
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
	fmt.Fprintln(output, style.Success("✓ Changed local services were restarted."))
	return session, nil
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
