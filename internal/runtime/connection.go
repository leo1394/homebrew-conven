package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/leo1394/homebrew-conven/internal/convenhome"
	"github.com/leo1394/homebrew-conven/internal/terminal"
	"golang.org/x/sys/unix"
)

type ConnectionEndpoint struct {
	Name    string
	Address string
}

type ConnectionConfig struct {
	Driver     string
	Command    string
	Args       []string
	Kubeconfig string
	Context    string
	Namespace  string
	Sudo       bool
	Timeout    time.Duration
	Readiness  []ConnectionEndpoint
}

type connectionEndpointDiagnostic struct {
	Name      string
	Address   string
	Reachable bool
	Detail    string
}

type connectionDiagnostics struct {
	Endpoints      []connectionEndpointDiagnostic
	IncludeLogTail bool
	LogLines       []string
	LogError       error
}

type connectionRecord struct {
	Version     int                  `json:"version"`
	Fingerprint string               `json:"fingerprint"`
	Process     ConnectionProcess    `json:"process"`
	Leases      map[string]time.Time `json:"leases"`
}

const connectionLeaseGrace = 5 * time.Minute
const connectionDiagnosticLogLines = 12
const connectionDiagnosticProbeTimeout = 750 * time.Millisecond
const connectionExitReapGrace = 500 * time.Millisecond
const connectionReadinessStableProbes = 2

func EnsureConnection(ctx context.Context, config ConnectionConfig, logPath string, lease string, output io.Writer) (*ConnectionProcess, error) {
	if config.Driver == "" || config.Driver == "none" {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	style := terminal.New(output)
	if len(config.Readiness) == 0 {
		return nil, errors.New("connection readiness must contain at least one TCP endpoint")
	}
	if lease == "" {
		return nil, errors.New("connection lease is empty")
	}
	fingerprint := connectionFingerprint(config)
	unlock, err := acquireConnectionLock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	record, err := loadConnectionRecord(fingerprint)
	if err != nil {
		return nil, err
	}
	if record != nil {
		pruned := pruneConnectionLeases(record, lease)
		current, unverified := connectionRecordState(record)
		if unverified {
			if pruned {
				if err := saveConnectionRecord(record); err != nil {
					return nil, err
				}
			}
			return nil, unverifiedConnectionError(record)
		}
		if !current {
			if err := removeConnectionRecord(fingerprint); err != nil {
				return nil, err
			}
			record = nil
		} else {
			if len(record.Leases) == 0 {
				if err := stopConnection(&record.Process, false); err != nil {
					return nil, fmt.Errorf("retire unleased %s connection: %w", record.Process.Driver, err)
				}
				if err := removeConnectionRecord(fingerprint); err != nil {
					return nil, err
				}
				record = nil
			} else if pruned {
				if err := saveConnectionRecord(record); err != nil {
					return nil, err
				}
			}
		}
	}
	if endpointsStable(ctx, config.Readiness) {
		if record == nil && config.Driver == "ktctl" {
			active, err := activeConnectionRecords(lease, "ktctl")
			if err != nil {
				return nil, err
			}
			for index := range active {
				if active[index].Process.Driver == "ktctl" {
					record = &active[index]
					break
				}
			}
		}
		if record != nil {
			if record.Leases == nil {
				record.Leases = make(map[string]time.Time)
			}
			record.Leases[lease] = time.Now()
			if err := saveConnectionRecord(record); err != nil {
				return nil, err
			}
			process := record.Process
			process.Owned = false
			process.Managed = true
			fmt.Fprintln(output, style.Success("✓ Remote endpoints are reachable through a managed shared connection; lease added."))
			return &process, nil
		}
		fmt.Fprintln(output, style.Success("✓ Remote endpoints are already reachable; reusing the external connection."))
		return &ConnectionProcess{
			Driver:      config.Driver,
			Owned:       false,
			Managed:     false,
			Fingerprint: fingerprint,
		}, nil
	}
	if record != nil {
		return nil, fmt.Errorf("managed %s connection is running but readiness endpoints are unavailable; fingerprint=%s log=%s", record.Process.Driver, fingerprint, record.Process.LogPath)
	}
	if config.Driver == "ktctl" {
		active, err := activeConnectionRecords(lease, "ktctl")
		if err != nil {
			return nil, err
		}
		for _, activeRecord := range active {
			if activeRecord.Process.Driver == "ktctl" {
				return nil, fmt.Errorf("another ktctl connection is active in this Conven state root with fingerprint %s", activeRecord.Fingerprint)
			}
		}
	}
	managedArgv, argv, err := buildConnectionCommands(config)
	if err != nil {
		return nil, err
	}
	if config.Driver == "ktctl" {
		pids, err := runningConnectionCommandPIDs(managedArgv[0])
		if err != nil {
			return nil, err
		}
		if len(pids) > 0 {
			return nil, fmt.Errorf("an unmanaged ktctl connect process is already running with pid %d; wait for it or stop it before Conven starts another", pids[0])
		}
	}
	if config.Timeout <= 0 {
		config.Timeout = 60 * time.Second
	}
	if config.Driver == "ktctl" && config.Timeout <= 60*time.Second {
		terminal.PrintWarningBlock(output, "ktctl connection timeout is 60s or less.", []string{
			"First-time shadow pod creation may use the entire budget.",
			"Suggested config: timeout: 240s with args [--podCreationTimeout, \"120\", --portForwardTimeout, \"30\"].",
		}, nil)
	}
	if config.Sudo {
		if err := authorizeSudo(ctx, output); err != nil {
			return nil, fmt.Errorf("sudo authorization failed: %w", err)
		}
	}
	process, completed, err := startConnectionObserved(ctx, config.Driver, argv, managedArgv, logPath, fingerprint, config.Sudo)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(output, "%s %s\n", style.Stage("Waiting for connection readiness"), style.Identifier(config.Driver))
	waitContext, cancel := context.WithTimeout(ctx, config.Timeout)
	defer cancel()
	var lastEndpointDiagnostics []connectionEndpointDiagnostic
	consecutiveReady := 0
	for {
		if err := waitContext.Err(); err != nil {
			failure := fmt.Errorf("%s connection readiness: %w; log: %s", config.Driver, err, logPath)
			return failConnectionAttempt(ctx, process, config, logPath, output, failure, lastEndpointDiagnostics)
		}
		if exitErr, exited := connectionCommandExit(completed); exited {
			if contextErr := waitContext.Err(); contextErr != nil {
				failure := fmt.Errorf("%s connection readiness: %w; log: %s", config.Driver, contextErr, logPath)
				return failConnectionAttempt(ctx, process, config, logPath, output, failure, lastEndpointDiagnostics)
			}
			failure := connectionExitFailure(config, logPath, exitErr)
			return failConnectionAttempt(ctx, process, config, logPath, output, failure, lastEndpointDiagnostics)
		}
		if !connectionProcessAlive(process.PID) {
			exitErr := waitForConnectionCommandExit(waitContext, completed, connectionExitReapGrace)
			if contextErr := waitContext.Err(); contextErr != nil {
				failure := fmt.Errorf("%s connection readiness: %w; log: %s", config.Driver, contextErr, logPath)
				return failConnectionAttempt(ctx, process, config, logPath, output, failure, lastEndpointDiagnostics)
			}
			failure := connectionExitFailure(config, logPath, exitErr)
			return failConnectionAttempt(ctx, process, config, logPath, output, failure, lastEndpointDiagnostics)
		}
		var ready bool
		lastEndpointDiagnostics, ready = probeConnectionEndpoints(waitContext, config.Readiness)
		if ready {
			consecutiveReady++
		} else {
			consecutiveReady = 0
		}
		if consecutiveReady >= connectionReadinessStableProbes {
			process.Managed = true
			record := &connectionRecord{
				Version:      1,
				Fingerprint:  fingerprint,
				Process:      *process,
				Leases:       map[string]time.Time{lease: time.Now()},
			}
			if err := saveConnectionRecord(record); err != nil {
				stopErr := stopConnection(process, false)
				if stopErr != nil {
					return process, errors.Join(err, stopErr)
				}
				return nil, err
			}
			fmt.Fprintf(output, "%s %s\n", style.Success("✓ Connection ready:"), style.Identifier(config.Driver))
			return process, nil
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-waitContext.Done():
			timer.Stop()
		case exitErr := <-completed:
			timer.Stop()
			if contextErr := waitContext.Err(); contextErr != nil {
				failure := fmt.Errorf("%s connection readiness: %w; log: %s", config.Driver, contextErr, logPath)
				return failConnectionAttempt(ctx, process, config, logPath, output, failure, lastEndpointDiagnostics)
			}
			failure := connectionExitFailure(config, logPath, exitErr)
			return failConnectionAttempt(ctx, process, config, logPath, output, failure, lastEndpointDiagnostics)
		case <-timer.C:
		}
	}
}

func connectionCommandExit(completed <-chan error) (error, bool) {
	select {
	case err := <-completed:
		return normalizeConnectionExit(err), true
	default:
		return nil, false
	}
}

func waitForConnectionCommandExit(ctx context.Context, completed <-chan error, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-completed:
		return normalizeConnectionExit(err)
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("process is no longer running")
	}
}

func normalizeConnectionExit(err error) error {
	if err == nil {
		return errors.New("exit status 0")
	}
	return err
}

func connectionExitFailure(config ConnectionConfig, logPath string, exitErr error) error {
	exitErr = normalizeConnectionExit(exitErr)
	if config.Driver != "ktctl" {
		return fmt.Errorf("%s connection exited before endpoints became reachable: %w; log: %s", config.Driver, exitErr, logPath)
	}
	reportedError, podCreateEOF := ktctlReportedExit(logPath)
	if podCreateEOF {
		namespace := "the active Kubernetes namespace"
		if config.Namespace != "" {
			namespace = fmt.Sprintf("Kubernetes namespace %q", config.Namespace)
		}
		return fmt.Errorf("%s connection exited before endpoints became reachable; ktctl reported a Kubernetes Pod CREATE EOF, so the remote shadow pod state is unknown and Conven did not retry automatically; inspect %s for ktctl shadow pods before retrying; log: %s", config.Driver, namespace, logPath)
	}
	if reportedError && exitErr.Error() == "exit status 0" {
		return fmt.Errorf("%s connection exited before endpoints became reachable; ktctl reported an error despite returning success; log: %s", config.Driver, logPath)
	}
	return fmt.Errorf("%s connection exited before endpoints became reachable: %w; log: %s", config.Driver, exitErr, logPath)
}

func ktctlReportedExit(logPath string) (reportedError bool, podCreateEOF bool) {
	lines, err := readLastLines(logPath, connectionDiagnosticLogLines)
	if err != nil {
		return false, false
	}
	for _, line := range lines {
		line = strings.ToLower(sanitizeDashboardText(line))
		if !strings.Contains(line, "err exit:") {
			continue
		}
		reportedError = true
		detail := strings.TrimSpace(line[strings.Index(line, "err exit:")+len("err exit:"):])
		if strings.Contains(detail, "post ") && strings.Contains(detail, "/pods") && strings.HasSuffix(detail, "eof") {
			podCreateEOF = true
		}
	}
	return reportedError, podCreateEOF
}

func connectionProcessAlive(pid int) bool {
	if !ProcessAlive(pid) {
		return false
	}
	command := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "stat=")
	command.Env = CommandEnvironment(map[string]string{"LC_ALL": "C"})
	output, err := command.Output()
	if err != nil {
		return ProcessAlive(pid)
	}
	return connectionProcessStateAlive(string(output))
}

func connectionProcessStateAlive(state string) bool {
	fields := strings.Fields(state)
	return len(fields) > 0 && !strings.HasPrefix(fields[0], "Z")
}

func failConnectionAttempt(ctx context.Context, process *ConnectionProcess, config ConnectionConfig, logPath string, output io.Writer, failure error, endpoints []connectionEndpointDiagnostic) (*ConnectionProcess, error) {
	diagnostics := captureConnectionDiagnostics(ctx, config, logPath, endpoints)
	stopErr := stopConnection(process, false)
	printConnectionDiagnostics(config, logPath, output, diagnostics)
	if stopErr != nil {
		return process, errors.Join(failure, stopErr)
	}
	return nil, failure
}

func captureConnectionDiagnostics(ctx context.Context, config ConnectionConfig, logPath string, endpoints []connectionEndpointDiagnostic) connectionDiagnostics {
	diagnostics := connectionDiagnostics{Endpoints: append([]connectionEndpointDiagnostic(nil), endpoints...)}
	if ctx.Err() != nil {
		diagnostics.Endpoints = nil
		return diagnostics
	}
	if len(diagnostics.Endpoints) == 0 {
		probeContext, cancel := context.WithTimeout(ctx, connectionDiagnosticProbeTimeout)
		diagnostics.Endpoints, _ = probeConnectionEndpoints(probeContext, config.Readiness)
		cancel()
	}
	if config.Driver != "ktctl" {
		return diagnostics
	}
	diagnostics.IncludeLogTail = true
	diagnostics.LogLines, diagnostics.LogError = readLastLines(logPath, connectionDiagnosticLogLines)
	return diagnostics
}

func printConnectionDiagnostics(config ConnectionConfig, logPath string, output io.Writer, diagnostics connectionDiagnostics) {
	style := terminal.New(output)
	fmt.Fprintf(output, "%s %s; diagnostics:\n", style.Failure("✗ Connection failed:"), style.Identifier(config.Driver))
	for _, endpoint := range diagnostics.Endpoints {
		name := sanitizeDashboardText(endpoint.Name)
		address := sanitizeDashboardText(endpoint.Address)
		if endpoint.Reachable {
			fmt.Fprintln(output, style.Detail(fmt.Sprintf("Readiness %s (%s): reachable", style.Identifier(name), address)))
			continue
		}
		detail := strings.TrimSpace(sanitizeDashboardText(endpoint.Detail))
		fmt.Fprintln(output, style.Detail(fmt.Sprintf("Readiness %s (%s): unreachable: %s", style.Identifier(name), address, detail)))
	}
	if config.Driver != "ktctl" || !diagnostics.IncludeLogTail {
		fmt.Fprintln(output, style.Detail("Connection log: "+logPath))
		return
	}
	if diagnostics.LogError != nil {
		fmt.Fprintln(output, style.Detail(fmt.Sprintf("Connection log: %s (unavailable: %s)", logPath, sanitizeDashboardText(diagnostics.LogError.Error()))))
		return
	}
	fmt.Fprintln(output, style.Detail("Connection log tail (control characters removed; secrets are not redacted): "+logPath))
	for _, line := range diagnostics.LogLines {
		line = strings.TrimSpace(sanitizeDashboardText(line))
		if line == "" {
			continue
		}
		fmt.Fprintf(output, "[%s] %s\n", style.Identifier("connection/"+config.Driver), line)
	}
}

func BuildConnectionCommand(config ConnectionConfig) ([]string, error) {
	switch config.Driver {
	case "ktctl":
		command := config.Command
		if command == "" {
			command = "ktctl"
		}
		argv := []string{command}
		if config.Kubeconfig != "" {
			argv = append(argv, "--kubeconfig", config.Kubeconfig)
		}
		if config.Context != "" {
			argv = append(argv, "--context", config.Context)
		}
		if config.Namespace != "" {
			argv = append(argv, "--namespace", config.Namespace)
		}
		argv = append(argv, "connect")
		argv = append(argv, config.Args...)
		return argv, nil
	case "command":
		if config.Command == "" {
			return nil, errors.New("connection.command is required for command driver")
		}
		return append([]string{config.Command}, config.Args...), nil
	default:
		return nil, fmt.Errorf("unsupported connection driver %q", config.Driver)
	}
}

func buildConnectionCommands(config ConnectionConfig) (managed []string, launch []string, err error) {
	managed, err = BuildConnectionCommand(config)
	if err != nil {
		return nil, nil, err
	}
	requestedCommand := managed[0]
	resolvedCommand, err := exec.LookPath(requestedCommand)
	if err != nil {
		return nil, nil, fmt.Errorf("connection command %q not found", requestedCommand)
	}
	resolvedCommand, err = filepath.Abs(resolvedCommand)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve connection command %q: %w", requestedCommand, err)
	}
	managed[0] = resolvedCommand
	launch = append([]string(nil), managed...)
	if config.Sudo {
		launch = append([]string{"sudo", "-n"}, launch...)
	}
	return managed, launch, nil
}

func startConnection(ctx context.Context, driver string, launchArgv []string, managedArgv []string, logPath string, fingerprint string, elevated bool) (*ConnectionProcess, error) {
	process, _, err := startConnectionObserved(ctx, driver, launchArgv, managedArgv, logPath, fingerprint, elevated)
	return process, err
}

func startConnectionObserved(ctx context.Context, driver string, launchArgv []string, managedArgv []string, logPath string, fingerprint string, elevated bool) (*ConnectionProcess, <-chan error, error) {
	logFile, err := openFreshLog(logPath)
	if err != nil {
		return nil, nil, err
	}
	command := exec.Command(launchArgv[0], launchArgv[1:]...)
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		logFile.Close()
		return nil, nil, fmt.Errorf("start %s connection: %w", driver, err)
	}
	launchPID := command.Process.Pid
	pid := launchPID
	if elevated {
		pid, err = waitForElevatedTarget(ctx, launchPID, managedArgv[0], 2*time.Second)
		if err != nil {
			cleanupErr := killElevatedDescendants(launchPID)
			if launchPGID, groupErr := syscall.Getpgid(launchPID); groupErr == nil {
				_ = syscall.Kill(-launchPGID, syscall.SIGTERM)
			}
			_ = command.Process.Kill()
			_ = command.Wait()
			logFile.Close()
			return nil, nil, errors.Join(fmt.Errorf("identify elevated %s connection target: %w", driver, err), cleanupErr)
		}
	}
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		syscall.Kill(launchPID, syscall.SIGTERM)
		_ = command.Wait()
		logFile.Close()
		return nil, nil, fmt.Errorf("read %s connection process group: %w", driver, err)
	}
	identity, err := processIdentity(pid)
	if err != nil {
		if elevated {
			_ = signalConnection(&ConnectionProcess{Elevated: true}, -pgid, syscall.SIGKILL)
		} else {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
		_ = command.Process.Kill()
		_ = command.Wait()
		logFile.Close()
		return nil, nil, fmt.Errorf("read %s connection process identity: %w", driver, err)
	}
	completed := make(chan error, 1)
	go func() {
		completed <- command.Wait()
	}()
	logFile.Close()
	return &ConnectionProcess{
		Driver:      driver,
		PID:         pid,
		PGID:        pgid,
		Command:     append([]string(nil), managedArgv...),
		Identity:    identity,
		LogPath:     logPath,
		StartedAt:   time.Now(),
		Owned:       true,
		Managed:     false,
		Elevated:    elevated,
		Fingerprint: fingerprint,
	}, completed, nil
}

type processTreeEntry struct {
	PID     int
	Parent  int
	Command string
}

func waitForElevatedTarget(ctx context.Context, rootPID int, expectedExecutable string, timeout time.Duration) (int, error) {
	waitContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		pid, found, err := findElevatedTarget(rootPID, expectedExecutable)
		if err != nil {
			return 0, err
		}
		if found {
			return pid, nil
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-waitContext.Done():
			timer.Stop()
			return 0, waitContext.Err()
		case <-timer.C:
		}
	}
}

func findElevatedTarget(rootPID int, expectedExecutable string) (int, bool, error) {
	entries, err := processTreeSnapshot()
	if err != nil {
		return 0, false, err
	}
	depths := descendantDepths(entries, rootPID)
	expectedBase := filepath.Base(expectedExecutable)
	bestPID := 0
	bestDepth := -1
	for _, entry := range entries {
		depth, descendant := depths[entry.PID]
		if !descendant || entry.PID == rootPID {
			continue
		}
		if !commandMatchesExecutable(entry.Command, expectedExecutable, expectedBase) {
			continue
		}
		if depth > bestDepth {
			bestPID = entry.PID
			bestDepth = depth
		}
	}
	return bestPID, bestPID > 0, nil
}

func processTreeSnapshot() ([]processTreeEntry, error) {
	command := exec.Command("ps", "-ax", "-o", "pid=", "-o", "ppid=", "-o", "command=")
	command.Env = CommandEnvironment(map[string]string{"LC_ALL": "C"})
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	entries := make([]processTreeEntry, 0)
	for _, line := range strings.Split(string(output), "\n") {
		entry, valid := parseProcessTreeLine(line)
		if valid {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func parseProcessTreeLine(line string) (processTreeEntry, bool) {
	pidText, remaining, found := takeProcessField(line)
	if !found {
		return processTreeEntry{}, false
	}
	parentText, command, found := takeProcessField(remaining)
	if !found || command == "" {
		return processTreeEntry{}, false
	}
	pid, pidErr := strconv.Atoi(pidText)
	parent, parentErr := strconv.Atoi(parentText)
	if pidErr != nil || parentErr != nil {
		return processTreeEntry{}, false
	}
	return processTreeEntry{PID: pid, Parent: parent, Command: command}, true
}

func takeProcessField(line string) (string, string, bool) {
	line = strings.TrimLeft(line, " \t")
	separator := strings.IndexAny(line, " \t")
	if separator <= 0 {
		return "", "", false
	}
	return line[:separator], strings.TrimLeft(line[separator:], " \t"), true
}

func descendantDepths(entries []processTreeEntry, rootPID int) map[int]int {
	depths := map[int]int{rootPID: 0}
	for changed := true; changed; {
		changed = false
		for _, entry := range entries {
			if _, found := depths[entry.PID]; found {
				continue
			}
			parentDepth, found := depths[entry.Parent]
			if !found {
				continue
			}
			depths[entry.PID] = parentDepth + 1
			changed = true
		}
	}
	return depths
}

func killElevatedDescendants(rootPID int) error {
	entries, err := processTreeSnapshot()
	if err != nil {
		return err
	}
	depths := descendantDepths(entries, rootPID)
	for depth := len(depths); depth > 0; depth-- {
		for pid, pidDepth := range depths {
			if pid == rootPID || pidDepth != depth {
				continue
			}
			command := exec.Command("sudo", "-n", "/bin/kill", "-KILL", fmt.Sprintf("%d", pid))
			if output, err := command.CombinedOutput(); err != nil && ProcessAlive(pid) {
				return fmt.Errorf("kill unidentified elevated descendant %d: %s: %w", pid, strings.TrimSpace(string(output)), err)
			}
		}
	}
	return nil
}

func commandMatchesExecutable(commandLine string, expectedExecutable string, expectedBase string) bool {
	commandLine = strings.TrimSpace(commandLine)
	for _, candidate := range []string{expectedExecutable, expectedBase} {
		if commandLine == candidate || strings.HasPrefix(commandLine, candidate+" ") {
			return true
		}
	}
	return false
}

func runningConnectionCommandPIDs(expectedExecutable string) ([]int, error) {
	command := exec.Command("ps", "-ax", "-o", "pid=", "-o", "command=")
	command.Env = CommandEnvironment(map[string]string{"LC_ALL": "C"})
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	expectedBase := filepath.Base(expectedExecutable)
	pids := make([]int, 0)
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		separator := strings.IndexAny(line, " \t")
		if separator < 1 {
			continue
		}
		pidText := line[:separator]
		commandLine := strings.TrimSpace(line[separator:])
		if !commandMatchesExecutable(commandLine, expectedExecutable, expectedBase) {
			continue
		}
		connect := false
		for _, argument := range strings.Fields(commandLine) {
			if argument == "connect" {
				connect = true
				break
			}
		}
		if !connect {
			continue
		}
		pid, err := strconv.Atoi(pidText)
		if err == nil {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

func endpointsReady(ctx context.Context, endpoints []ConnectionEndpoint) bool {
	_, ready := probeConnectionEndpoints(ctx, endpoints)
	return ready
}

func endpointsStable(ctx context.Context, endpoints []ConnectionEndpoint) bool {
	for attempt := 0; attempt < connectionReadinessStableProbes; attempt++ {
		if !endpointsReady(ctx, endpoints) {
			return false
		}
		if attempt+1 == connectionReadinessStableProbes {
			return true
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
	return false
}

func probeConnectionEndpoints(ctx context.Context, endpoints []ConnectionEndpoint) ([]connectionEndpointDiagnostic, bool) {
	diagnostics := make([]connectionEndpointDiagnostic, len(endpoints))
	var wait sync.WaitGroup
	for index, endpoint := range endpoints {
		wait.Add(1)
		go func(index int, endpoint ConnectionEndpoint) {
			defer wait.Done()
			diagnostic := connectionEndpointDiagnostic{Name: endpoint.Name, Address: endpoint.Address}
			dialer := &net.Dialer{Timeout: 500 * time.Millisecond}
			connection, err := dialer.DialContext(ctx, "tcp", endpoint.Address)
			if err != nil {
				diagnostic.Detail = err.Error()
				diagnostics[index] = diagnostic
				return
			}
			connection.Close()
			diagnostic.Reachable = true
			diagnostics[index] = diagnostic
		}(index, endpoint)
	}
	wait.Wait()
	ready := true
	for _, diagnostic := range diagnostics {
		ready = ready && diagnostic.Reachable
	}
	return diagnostics, ready
}

func connectionFingerprint(config ConnectionConfig) string {
	parts := []string{config.Driver, config.Command, config.Kubeconfig, config.Context, config.Namespace, strconv.FormatBool(config.Sudo)}
	parts = append(parts, config.Args...)
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(digest[:8])
}

func stopConnection(process *ConnectionProcess, force bool) error {
	if process == nil || !process.Owned {
		return nil
	}
	managed := ServiceProcess{
		Name:     "connection/" + process.Driver,
		PID:      process.PID,
		PGID:     process.PGID,
		Command:  process.Command,
		Identity: process.Identity,
	}
	if !connectionProcessAlive(process.PID) {
		if !force && waitForManagedExit(process.PID, process.PGID, connectionExitReapGrace) {
			return nil
		}
		if ProcessGroupAlive(process.PGID) {
			if !force {
				return fmt.Errorf("refusing to stop %s: leader pid %d exited while process group %d is still active", managed.Name, process.PID, process.PGID)
			}
			return forceStopConnectionGroup(process)
		}
		return nil
	}
	if err := VerifyProcess(managed); err != nil {
		if waitForManagedExit(process.PID, process.PGID, connectionExitReapGrace) {
			return nil
		}
		if !force || !ProcessGroupAlive(process.PGID) {
			return err
		}
		return forceStopConnectionGroup(process)
	}
	return forceStopConnectionGroup(process)
}

func forceStopConnectionGroup(process *ConnectionProcess) error {
	if process.Elevated {
		if err := authorizeElevatedConnectionStop(); err != nil {
			return err
		}
	}
	target := process.PID
	if process.PGID > 0 {
		target = -process.PGID
	}
	for _, phase := range []struct {
		signal  syscall.Signal
		timeout time.Duration
	}{
		{signal: syscall.SIGINT, timeout: 2 * time.Second},
		{signal: syscall.SIGTERM, timeout: 2 * time.Second},
		{signal: syscall.SIGKILL, timeout: 2 * time.Second},
	} {
		if err := signalConnection(process, target, phase.signal); err != nil && !errors.Is(err, syscall.ESRCH) {
			if waitForManagedExit(process.PID, process.PGID, connectionExitReapGrace) {
				return nil
			}
			return fmt.Errorf("stop %s connection: %w", process.Driver, err)
		}
		if waitForManagedExit(process.PID, process.PGID, phase.timeout) {
			return nil
		}
	}
	return fmt.Errorf("stop %s connection: process group %d is still active", process.Driver, process.PGID)
}

func authorizeElevatedConnectionStop() error {
	if err := exec.Command("sudo", "-n", "-v").Run(); err == nil {
		return nil
	}
	if err := authorizeSudo(context.Background(), os.Stderr); err != nil {
		return fmt.Errorf("sudo authorization required to stop elevated connection: %w", err)
	}
	return nil
}

func authorizeSudo(ctx context.Context, output io.Writer) error {
	style := terminal.New(output)
	terminal.PrintWarningBlock(output, "Sudo authorization required.", []string{
		"Password input is hidden.",
	}, nil)
	validation := exec.CommandContext(ctx, "sudo", "-v")
	validation.Stdin = os.Stdin
	validation.Stdout = output
	validation.Stderr = output
	if err := validation.Run(); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		return err
	}
	fmt.Fprintln(output, style.Success("✓ Sudo authorization confirmed."))
	return nil
}

func signalConnection(process *ConnectionProcess, target int, signal syscall.Signal) error {
	if !process.Elevated {
		return syscall.Kill(target, signal)
	}
	name := map[syscall.Signal]string{
		syscall.SIGINT:  "INT",
		syscall.SIGTERM: "TERM",
		syscall.SIGKILL: "KILL",
	}[signal]
	command := exec.Command("sudo", "-n", "/bin/kill", "-"+name, "--", fmt.Sprintf("%d", target))
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("sudo kill: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func validateConnectionForReplacement(ctx context.Context, process *ConnectionProcess, lease string) error {
	if process == nil || !process.Managed {
		if process != nil && process.Owned {
			_, unverified := connectionRecordState(&connectionRecord{Process: *process})
			if unverified {
				return fmt.Errorf("workspace has an unverified %s connection; use conven services --stop --all before starting a new session", process.Driver)
			}
		}
		return nil
	}
	unlock, err := acquireConnectionLock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	record, err := loadConnectionRecord(process.Fingerprint)
	if err != nil {
		return err
	}
	if record == nil {
		current, unverified := connectionRecordState(&connectionRecord{Process: *process})
		if current || unverified {
			return fmt.Errorf("managed connection record %s is missing while its saved process group %d is still active", process.Fingerprint, process.PGID)
		}
		return nil
	}
	if !sameConnectionProcess(process, &record.Process) {
		return fmt.Errorf("workspace managed %s connection does not match registry record %s", process.Driver, process.Fingerprint)
	}
	current, unverified := connectionRecordState(record)
	if unverified {
		return unverifiedConnectionError(record)
	}
	if current {
		if _, found := record.Leases[lease]; !found {
			return fmt.Errorf("managed %s connection record %s does not contain this workspace lease", process.Driver, process.Fingerprint)
		}
	}
	return nil
}

func renewRetainedKtctlConnection(ctx context.Context, process *ConnectionProcess, config ConnectionConfig, lease string) (bool, error) {
	if process == nil || !process.Managed || process.Driver != "ktctl" || config.Driver != "ktctl" || process.Fingerprint != connectionFingerprint(config) {
		return false, nil
	}
	unlock, err := acquireConnectionLock(ctx)
	if err != nil {
		return false, err
	}
	defer unlock()
	record, err := loadConnectionRecord(process.Fingerprint)
	if err != nil {
		return false, err
	}
	if record == nil {
		return false, fmt.Errorf("managed connection record %s is missing while preparing the fresh start", process.Fingerprint)
	}
	if !sameConnectionProcess(process, &record.Process) {
		return false, fmt.Errorf("workspace managed ktctl connection does not match registry record %s", process.Fingerprint)
	}
	current, unverified := connectionRecordState(record)
	if unverified {
		return false, unverifiedConnectionError(record)
	}
	if !current {
		return false, nil
	}
	if _, found := record.Leases[lease]; !found {
		return false, fmt.Errorf("managed ktctl connection record %s does not contain this workspace lease", process.Fingerprint)
	}
	if !endpointsReady(ctx, config.Readiness) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		return false, nil
	}
	record.Leases[lease] = time.Now()
	if err := saveConnectionRecord(record); err != nil {
		return false, err
	}
	return true, nil
}

func sameConnectionProcess(left *ConnectionProcess, right *ConnectionProcess) bool {
	if left == nil || right == nil || left.Driver != right.Driver || left.PID != right.PID || left.PGID != right.PGID || left.Identity != right.Identity || left.Fingerprint != right.Fingerprint || len(left.Command) != len(right.Command) {
		return false
	}
	for index := range left.Command {
		if left.Command[index] != right.Command[index] {
			return false
		}
	}
	return true
}

func releaseConnection(ctx context.Context, process *ConnectionProcess, lease string, force bool, output io.Writer) error {
	if process == nil {
		return nil
	}
	style := terminal.New(output)
	if !process.Managed {
		return stopConnection(process, force)
	}
	unlock, err := acquireConnectionLock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	record, err := loadConnectionRecord(process.Fingerprint)
	if err != nil {
		return err
	}
	if record == nil {
		current, unverified := connectionRecordState(&connectionRecord{Process: *process})
		if !current && !unverified {
			fmt.Fprintf(output, "%s %s connection record is already absent and its saved process group has exited.\n", style.Success("✓ Shared"), style.Identifier(process.Driver))
			return nil
		}
		if force {
			recovery := *process
			recovery.Owned = true
			if err := stopConnection(&recovery, true); err != nil {
				return err
			}
			fmt.Fprintf(output, "%s %s connection from its saved process group after the shared record was lost.\n", style.Success("✓ Recovered"), style.Identifier(process.Driver))
			return nil
		}
		return fmt.Errorf("managed connection record %s is missing while its saved process group %d is still active", process.Fingerprint, process.PGID)
	}
	delete(record.Leases, lease)
	pruneConnectionLeases(record, "")
	if len(record.Leases) > 0 {
		if err := saveConnectionRecord(record); err != nil {
			return err
		}
		fmt.Fprintf(output, "%s %s connection kept for %d other workspace lease(s).\n", style.Success("✓ Shared"), style.Identifier(record.Process.Driver), len(record.Leases))
		return nil
	}
	current, unverified := connectionRecordState(record)
	if unverified && !force {
		return fmt.Errorf("managed %s connection state is unverified; pid=%d pgid=%d", record.Process.Driver, record.Process.PID, record.Process.PGID)
	}
	if current || unverified {
		if err := stopConnection(&record.Process, force); err != nil {
			if saveErr := saveConnectionRecord(record); saveErr != nil {
				return errors.Join(err, saveErr)
			}
			return err
		}
	}
	if err := removeConnectionRecord(process.Fingerprint); err != nil {
		return err
	}
	fmt.Fprintf(output, "%s %s connection stopped after its final workspace lease was released.\n", style.Success("✓ Shared"), style.Identifier(record.Process.Driver))
	return nil
}

func connectionRecordState(record *connectionRecord) (current bool, unverified bool) {
	process := record.Process
	if !ProcessAlive(process.PID) {
		return false, ProcessGroupAlive(process.PGID)
	}
	managed := ServiceProcess{
		Name:     "connection/" + process.Driver,
		PID:      process.PID,
		PGID:     process.PGID,
		Command:  process.Command,
		Identity: process.Identity,
	}
	if err := VerifyProcess(managed); err != nil {
		return false, ProcessGroupAlive(process.PGID)
	}
	return true, false
}

func unverifiedConnectionError(record *connectionRecord) error {
	recovery := ""
	if len(record.Leases) == 0 {
		recovery = "; after confirming the saved PGID belongs to Conven, run conven services --stop --all --force from a workspace using the same Conven user state root"
	}
	return fmt.Errorf("managed %s connection state is unverified; fingerprint=%s pid=%d pgid=%d%s", record.Process.Driver, record.Fingerprint, record.Process.PID, record.Process.PGID, recovery)
}

func connectionStateDirectory() (string, error) {
	root, err := convenhome.Root("")
	if err != nil {
		return "", err
	}
	state := filepath.Join(root, "state")
	directory := filepath.Join(root, "state", "connections")
	for _, item := range []struct {
		path  string
		label string
	}{
		{path: root, label: "Conven home"},
		{path: state, label: "Conven state directory"},
		{path: directory, label: "Conven connection state directory"},
	} {
		if err := ensurePrivateConnectionDirectory(item.path, item.label); err != nil {
			return "", err
		}
	}
	return directory, nil
}

func ensurePrivateConnectionDirectory(path string, label string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if mkdirErr := os.Mkdir(path, 0700); mkdirErr != nil && !os.IsExist(mkdirErr) {
			return fmt.Errorf("create %s %q: %w", label, path, mkdirErr)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return fmt.Errorf("inspect %s %q: %w", label, path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s %q must be a directory; symbolic links are not allowed", label, path)
	}
	if err := os.Chmod(path, 0700); err != nil {
		return fmt.Errorf("protect %s %q: %w", label, path, err)
	}
	return nil
}

func connectionRecordPath(fingerprint string) (string, error) {
	directory, err := connectionStateDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, fingerprint+".json"), nil
}

func loadConnectionRecord(fingerprint string) (*connectionRecord, error) {
	path, err := connectionRecordPath(fingerprint)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read connection record: %w", err)
	}
	var record connectionRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("decode connection record: %w", err)
	}
	if record.Version != 1 || record.Fingerprint != fingerprint {
		return nil, fmt.Errorf("invalid connection record %s", path)
	}
	return &record, nil
}

func saveConnectionRecord(record *connectionRecord) error {
	path, err := connectionRecordPath(record.Fingerprint)
	if err != nil {
		return err
	}
	record.Version = 1
	record.Process.Managed = true
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode connection record: %w", err)
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".connection-*.json")
	if err != nil {
		return fmt.Errorf("create temporary connection record: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return fmt.Errorf("protect temporary connection record: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write connection record: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close connection record: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish connection record: %w", err)
	}
	return nil
}

func removeConnectionRecord(fingerprint string) error {
	path, err := connectionRecordPath(fingerprint)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove connection record: %w", err)
	}
	return nil
}

func pruneConnectionLeases(record *connectionRecord, currentLease string) bool {
	changed := false
	now := time.Now()
	for lease, createdAt := range record.Leases {
		if now.Sub(createdAt) < connectionLeaseGrace {
			continue
		}
		if connectionLeaseActive(lease, record.Fingerprint, lease == currentLease) {
			continue
		}
		delete(record.Leases, lease)
		changed = true
	}
	return changed
}

func connectionLeaseActive(workspaceStateRoot string, fingerprint string, ignoreWorkspaceLock bool) bool {
	if !ignoreWorkspaceLock && workspaceStateLocked(workspaceStateRoot) {
		return true
	}
	sessionPath := filepath.Join(workspaceStateRoot, "session.json")
	info, err := os.Lstat(sessionPath)
	if err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return true
	}
	if err != nil {
		return false
	}
	data, err := os.ReadFile(sessionPath)
	if err != nil {
		return false
	}
	var session Session
	if json.Unmarshal(data, &session) != nil || session.Connection == nil || session.Connection.Fingerprint != fingerprint {
		return false
	}
	for _, process := range session.Services {
		if (ProcessAlive(process.PID) && VerifyProcess(process) == nil) || ProcessGroupAlive(process.PGID) {
			return true
		}
	}
	return false
}

func workspaceStateLocked(workspaceStateRoot string) bool {
	path := filepath.Join(workspaceStateRoot, ".lock")
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return true
		}
	} else if !os.IsNotExist(err) {
		return true
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		if os.IsNotExist(err) {
			return false
		}
		return true
	}
	defer file.Close()
	err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		return false
	}
	return true
}

func activeConnectionRecords(currentLease string, driver string) ([]connectionRecord, error) {
	directory, err := connectionStateDirectory()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("list connection records: %w", err)
	}
	active := make([]connectionRecord, 0)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		fingerprint := strings.TrimSuffix(entry.Name(), ".json")
		record, err := loadConnectionRecord(fingerprint)
		if err != nil {
			return nil, err
		}
		if driver != "" && record.Process.Driver != driver {
			continue
		}
		pruned := pruneConnectionLeases(record, currentLease)
		current, unverified := connectionRecordState(record)
		if unverified {
			if pruned {
				if err := saveConnectionRecord(record); err != nil {
					return nil, err
				}
			}
			return nil, unverifiedConnectionError(record)
		}
		if current {
			if len(record.Leases) == 0 {
				if err := stopConnection(&record.Process, false); err != nil {
					return nil, fmt.Errorf("retire unleased %s connection: %w", record.Process.Driver, err)
				}
				if err := removeConnectionRecord(fingerprint); err != nil {
					return nil, err
				}
				continue
			}
			if pruned {
				if err := saveConnectionRecord(record); err != nil {
					return nil, err
				}
			}
			active = append(active, *record)
			continue
		}
		if err := removeConnectionRecord(fingerprint); err != nil {
			return nil, err
		}
	}
	return active, nil
}

func printSharedConnectionStatus(ctx context.Context, output io.Writer) (int, error) {
	style := terminal.New(output)
	unlock, err := acquireConnectionLock(ctx)
	if err != nil {
		return 0, err
	}
	defer unlock()
	directory, err := connectionStateDirectory()
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return 0, fmt.Errorf("list connection records: %w", err)
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		fingerprint := strings.TrimSuffix(entry.Name(), ".json")
		record, err := loadConnectionRecord(fingerprint)
		if err != nil {
			return count, err
		}
		preview := *record
		preview.Leases = make(map[string]time.Time, len(record.Leases))
		for lease, createdAt := range record.Leases {
			preview.Leases[lease] = createdAt
		}
		pruneConnectionLeases(&preview, "")
		state := "stopped"
		current, unverified := connectionRecordState(record)
		if current {
			state = "running"
		} else if unverified {
			state = "unverified"
		}
		if count == 0 {
			fmt.Fprintln(output, style.Stage("Shared connection records in this Conven state root:"))
		}
		fmt.Fprintln(output, style.Detail(fmt.Sprintf("%s: %s, fingerprint=%s pid=%d pgid=%d effective-leases=%d log=%s", style.Identifier("connection/"+record.Process.Driver), styledProcessState(style, state, 0), fingerprint, record.Process.PID, record.Process.PGID, len(preview.Leases), record.Process.LogPath)))
		count++
	}
	return count, nil
}

func recoverUnleasedConnections(ctx context.Context, currentLease string, output io.Writer) (int, error) {
	style := terminal.New(output)
	unlock, err := acquireConnectionLock(ctx)
	if err != nil {
		return 0, err
	}
	defer unlock()
	directory, err := connectionStateDirectory()
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return 0, fmt.Errorf("list connection records: %w", err)
	}
	recovered := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		fingerprint := strings.TrimSuffix(entry.Name(), ".json")
		record, err := loadConnectionRecord(fingerprint)
		if err != nil {
			return recovered, err
		}
		_, removedCurrentLease := record.Leases[currentLease]
		delete(record.Leases, currentLease)
		pruned := pruneConnectionLeases(record, currentLease) || removedCurrentLease
		if len(record.Leases) > 0 {
			if pruned {
				if err := saveConnectionRecord(record); err != nil {
					return recovered, err
				}
			}
			continue
		}
		current, unverified := connectionRecordState(record)
		if current || unverified {
			fmt.Fprintf(output, "%s %s\n", style.Stage("Force recovering unleased connection"), style.Identifier(record.Process.Driver))
			fmt.Fprintln(output, style.Detail(fmt.Sprintf("Fingerprint: %s, process group: %d", fingerprint, record.Process.PGID)))
			if err := stopConnection(&record.Process, true); err != nil {
				if saveErr := saveConnectionRecord(record); saveErr != nil {
					return recovered, errors.Join(err, saveErr)
				}
				return recovered, err
			}
		}
		if err := removeConnectionRecord(fingerprint); err != nil {
			return recovered, err
		}
		recovered++
	}
	return recovered, nil
}

func acquireConnectionLock(ctx context.Context) (func(), error) {
	directory, err := connectionStateDirectory()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(directory, ".lock")
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("connection lock %q must be a regular file, not a symbolic link", path)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect connection lock: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open connection lock: %w", err)
	}
	for {
		err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() {
				_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
				_ = file.Close()
			}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			file.Close()
			return nil, fmt.Errorf("lock connection: %w", err)
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func ConnectionLogPath(runtimeDir string) string {
	return filepath.Join(runtimeDir, "connection.log")
}
