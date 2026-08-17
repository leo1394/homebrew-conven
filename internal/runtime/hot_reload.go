package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/leo1394/homebrew-conven/internal/terminal"
)

const (
	hotReloadProcessName    = "hot-reload"
	hotReloadPollInterval   = 500 * time.Millisecond
	hotReloadDebounce       = 500 * time.Millisecond
	hotReloadRegisterPeriod = 3 * time.Second
)

var errHotReloadSessionEnded = errors.New("hot reload session ended")

type HotReloadOptions struct {
	PollInterval        time.Duration
	Debounce            time.Duration
	Output              io.Writer
	RequireRegistration bool
}

func ensureHotReloadWatcherLocked(workspace *WorkspaceData, session *Session, executable string, output io.Writer) error {
	if output == nil {
		output = io.Discard
	}
	if strings.TrimSpace(executable) == "" || session == nil || len(session.Services) == 0 {
		return nil
	}
	if session.HotReload != nil {
		if ProcessAlive(session.HotReload.PID) {
			if err := VerifyProcess(*session.HotReload); err != nil {
				return fmt.Errorf("inspect hot reload watcher: %w", err)
			}
			return nil
		}
		if ProcessGroupAlive(session.HotReload.PGID) {
			return fmt.Errorf("refusing to replace hot reload watcher: leader pid %d exited while process group %d is still active", session.HotReload.PID, session.HotReload.PGID)
		}
		session.HotReload = nil
	}
	absoluteExecutable, err := filepath.Abs(executable)
	if err != nil {
		return fmt.Errorf("resolve Conven executable for hot reload: %w", err)
	}
	logPath := filepath.Join(workspace.Store.CurrentDir, "logs", "hot-reload.log")
	process, err := StartService(hotReloadProcessName, []string{absoluteExecutable, "-C", workspace.Root, "__hot-reload"}, workspace.Root, CommandEnvironment(map[string]string{
		"CONVEN_HOT_RELOAD_PROCESS": "1",
	}), logPath)
	if err != nil {
		return fmt.Errorf("start hot reload watcher: %w", err)
	}
	session.HotReload = &process
	if err := workspace.Store.Save(session); err != nil {
		_ = StopProcess(process, 3*time.Second)
		session.HotReload = nil
		return fmt.Errorf("save hot reload watcher state: %w", err)
	}
	style := terminal.New(output)
	fmt.Fprintln(output, style.Success("✓ Hot reload is watching selected service source files."))
	fmt.Fprintln(output, style.Detail("Log: "+logPath))
	return nil
}

func stopHotReloadWatcherLocked(session *Session, force bool, output io.Writer) error {
	if session == nil || session.HotReload == nil {
		return nil
	}
	process := *session.HotReload
	style := terminal.New(output)
	fmt.Fprintln(output, style.Stage("Stopping hot reload watcher"))
	err := StopProcess(process, 3*time.Second)
	if err != nil && force && ProcessGroupAlive(process.PGID) {
		err = ForceStopProcessGroup(process, 2*time.Second)
	}
	if err != nil {
		return err
	}
	session.HotReload = nil
	return nil
}

func RunHotReloadWatcher(ctx context.Context, workspace *WorkspaceData, options HotReloadOptions) error {
	options.RequireRegistration = true
	return watchHotReload(ctx, workspace, options)
}

func watchHotReload(ctx context.Context, workspace *WorkspaceData, options HotReloadOptions) error {
	if options.PollInterval <= 0 {
		options.PollInterval = hotReloadPollInterval
	}
	if options.Debounce <= 0 {
		options.Debounce = hotReloadDebounce
	}
	if options.Output == nil {
		options.Output = io.Discard
	}
	if options.RequireRegistration {
		if err := waitForHotReloadRegistration(ctx, workspace.Store); err != nil {
			if errors.Is(err, errHotReloadSessionEnded) || errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
	}
	fmt.Fprintf(options.Output, "--- conven hot reload watcher started %s ---\n", time.Now().Format(time.RFC3339))
	observed := make(map[string]string)
	pending := make(map[string]string)
	var pendingSince time.Time
	initialized := false
	ticker := time.NewTicker(options.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		session, current, err := hotReloadSnapshot(workspace, options.RequireRegistration)
		if err != nil {
			if errors.Is(err, errHotReloadSessionEnded) {
				return nil
			}
			fmt.Fprintf(options.Output, "hot reload scan failed: %v\n", err)
			continue
		}
		if !initialized {
			for _, process := range session.Services {
				observed[process.Name] = process.SourceFingerprint
			}
			initialized = true
		}
		active := make(map[string]bool, len(current))
		changedNow := false
		for name, fingerprint := range current {
			active[name] = true
			if observed[name] == fingerprint {
				delete(pending, name)
				continue
			}
			if pending[name] != fingerprint {
				pending[name] = fingerprint
				changedNow = true
			}
		}
		for name := range observed {
			if !active[name] {
				delete(observed, name)
				delete(pending, name)
			}
		}
		if len(pending) == 0 {
			pendingSince = time.Time{}
			continue
		}
		if changedNow || pendingSince.IsZero() {
			pendingSince = time.Now()
			continue
		}
		if time.Since(pendingSince) < options.Debounce {
			continue
		}
		names := make([]string, 0, len(pending))
		for name := range pending {
			names = append(names, name)
		}
		sort.Strings(names)
		retryPending := false
		for _, name := range names {
			fingerprint := pending[name]
			if err := attemptHotReload(ctx, workspace, session, []string{name}, options.Output); err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				if errors.Is(err, errWorkspaceLocked) {
					retryPending = true
					break
				}
			}
			observed[name] = fingerprint
			delete(pending, name)
		}
		if retryPending {
			pendingSince = time.Now()
			continue
		}
		pendingSince = time.Time{}
	}
}

func waitForHotReloadRegistration(ctx context.Context, store *Store) error {
	deadline := time.Now().Add(hotReloadRegisterPeriod)
	for {
		session, err := store.Load()
		if err != nil {
			return err
		}
		if session != nil && session.HotReload != nil && session.HotReload.PID == os.Getpid() {
			if err := VerifyProcess(*session.HotReload); err != nil {
				return fmt.Errorf("verify hot reload watcher registration: %w", err)
			}
			return nil
		}
		if session == nil || len(session.Services) == 0 || time.Now().After(deadline) {
			return errHotReloadSessionEnded
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func hotReloadSnapshot(workspace *WorkspaceData, requireRegistration bool) (*Session, map[string]string, error) {
	session, err := workspace.Store.Load()
	if err != nil {
		return nil, nil, err
	}
	if session == nil || len(session.Services) == 0 {
		return nil, nil, errHotReloadSessionEnded
	}
	if requireRegistration && (session.HotReload == nil || session.HotReload.PID != os.Getpid()) {
		return nil, nil, errHotReloadSessionEnded
	}
	current := make(map[string]string, len(session.Services))
	for _, process := range session.Services {
		service, found := workspace.Manifest.Services[process.Name]
		if !found {
			return nil, nil, fmt.Errorf("service %q is no longer declared by the watcher manifest", process.Name)
		}
		directory := service.Path
		if !filepath.IsAbs(directory) {
			directory = filepath.Join(workspace.Root, directory)
		}
		fingerprint, err := SourceFingerprint(filepath.Clean(directory))
		if err != nil {
			return nil, nil, fmt.Errorf("fingerprint %s source: %w", process.Name, err)
		}
		current[process.Name] = fingerprint
	}
	return session, current, nil
}

func attemptHotReload(ctx context.Context, workspace *WorkspaceData, before *Session, names []string, output io.Writer) error {
	writer, closeLogs, err := hotReloadAttemptWriter(output, before, names)
	if err != nil {
		fmt.Fprintf(output, "hot reload could not open service logs: %v\n", err)
		return err
	}
	defer closeLogs()
	fmt.Fprintf(writer, "\n--- conven hot reload %s: %s ---\n", time.Now().Format(time.RFC3339), strings.Join(names, ", "))
	_, err = Restart(ctx, workspace, RestartOptions{Services: names, Output: writer})
	if err == nil {
		fmt.Fprintf(writer, "--- conven hot reload complete: %s ---\n", strings.Join(names, ", "))
		return nil
	}
	fmt.Fprintf(writer, "hot reload rejected: %v\n", err)
	preserved := preservedHotReloadServices(workspace.Store, before, names)
	if len(preserved) > 0 {
		fmt.Fprintf(writer, "last-known-good service remains running: %s\n", strings.Join(preserved, ", "))
	}
	return err
}

func hotReloadAttemptWriter(output io.Writer, session *Session, names []string) (io.Writer, func(), error) {
	writers := []io.Writer{output}
	files := make([]*os.File, 0, len(names))
	for _, name := range names {
		process := sessionProcess(session, name)
		if process.LogPath == "" {
			continue
		}
		file, err := openLog(process.LogPath)
		if err != nil {
			for _, opened := range files {
				opened.Close()
			}
			return nil, func() {}, err
		}
		files = append(files, file)
		writers = append(writers, file)
	}
	return io.MultiWriter(writers...), func() {
		for _, file := range files {
			file.Close()
		}
	}, nil
}

func preservedHotReloadServices(store *Store, before *Session, names []string) []string {
	after, err := store.Load()
	if err != nil || after == nil {
		return nil
	}
	preserved := make([]string, 0, len(names))
	for _, name := range names {
		oldProcess := sessionProcess(before, name)
		current := sessionProcess(after, name)
		if oldProcess.PID > 0 && current.PID == oldProcess.PID && ProcessAlive(current.PID) && VerifyProcess(current) == nil {
			preserved = append(preserved, fmt.Sprintf("%s (pid=%d)", name, current.PID))
		}
	}
	return preserved
}
