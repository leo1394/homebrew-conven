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
	"sync"
	"syscall"
	"time"
)

func CommandEnvironment(values ...map[string]string) []string {
	merged := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, found := strings.Cut(entry, "=")
		if found {
			merged[key] = value
		}
	}
	for _, valueSet := range values {
		for key, value := range valueSet {
			merged[key] = value
		}
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+merged[key])
	}
	return result
}

func RunForeground(ctx context.Context, argv []string, directory string, environment []string, output io.Writer, logPath string) error {
	if len(argv) == 0 {
		return errors.New("command is empty")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	logFile, err := openLog(logPath)
	if err != nil {
		return err
	}
	runErr := runForeground(ctx, argv, directory, environment, output, logFile, logPath)
	if err := logFile.Close(); err != nil {
		closeErr := fmt.Errorf("close command log %s: %w", logPath, err)
		if runErr != nil {
			return errors.Join(runErr, closeErr)
		}
		return closeErr
	}
	return runErr
}

func runForeground(ctx context.Context, argv []string, directory string, environment []string, output io.Writer, logOutput io.Writer, logPath string) error {
	logWriter := &commandOutputWriter{output: logOutput}
	writers := []io.Writer{logWriter}
	if output != nil {
		writers = append(writers, &commandOutputWriter{output: output})
	}
	command := exec.Command(argv[0], argv[1:]...)
	command.Dir = directory
	command.Env = environment
	command.Stdout = io.MultiWriter(writers...)
	command.Stderr = command.Stdout
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		return fmt.Errorf("%s failed (log: %s): %w", strings.Join(argv, " "), logPath, err)
	}
	waitErr := waitCommandContext(ctx, command, syscall.SIGTERM, 2*time.Second)
	if logErr := logWriter.Err(); logErr != nil {
		failure := fmt.Errorf("%s failed because Loom could not write log %s: %w", strings.Join(argv, " "), logPath, logErr)
		if waitErr != nil {
			return errors.Join(failure, fmt.Errorf("child process result: %w", waitErr))
		}
		return failure
	}
	if waitErr != nil {
		return fmt.Errorf("%s failed (log: %s): %w", strings.Join(argv, " "), logPath, waitErr)
	}
	return nil
}

type commandOutputWriter struct {
	output io.Writer
	mu     sync.Mutex
	err    error
}

func (writer *commandOutputWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.err == nil {
		written, err := writer.output.Write(data)
		if err == nil && written != len(data) {
			err = io.ErrShortWrite
		}
		if err != nil {
			writer.err = err
		}
	}
	return len(data), nil
}

func (writer *commandOutputWriter) Err() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.err
}

func StartService(name string, argv []string, directory string, environment []string, logPath string) (ServiceProcess, error) {
	if len(argv) == 0 {
		return ServiceProcess{}, errors.New("run command is empty")
	}
	logFile, err := openLog(logPath)
	if err != nil {
		return ServiceProcess{}, err
	}
	command := exec.Command(argv[0], argv[1:]...)
	command.Dir = directory
	command.Env = environment
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		logFile.Close()
		return ServiceProcess{}, fmt.Errorf("start %s: %w", name, err)
	}
	pid := command.Process.Pid
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		syscall.Kill(pid, syscall.SIGTERM)
		_ = command.Wait()
		logFile.Close()
		return ServiceProcess{}, fmt.Errorf("read %s process group: %w", name, err)
	}
	identity, err := processIdentity(pid)
	if err != nil {
		syscall.Kill(-pgid, syscall.SIGTERM)
		_ = command.Wait()
		logFile.Close()
		return ServiceProcess{}, fmt.Errorf("read %s process identity: %w", name, err)
	}
	go func() {
		_ = command.Wait()
	}()
	logFile.Close()
	return ServiceProcess{
		Name:      name,
		PID:       pid,
		PGID:      pgid,
		Command:   append([]string(nil), argv...),
		Identity:  identity,
		LogPath:   logPath,
		StartedAt: time.Now(),
	}, nil
}

func openLog(path string) (*os.File, error) {
	return openLogWithFlags(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY)
}

func openFreshLog(path string) (*os.File, error) {
	return openLogWithFlags(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY)
}

func openLogWithFlags(path string, flags int) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("log path %s must be a regular file, not a symbolic link", path)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect log %s: %w", path, err)
	}
	file, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		return nil, fmt.Errorf("open log %s: %w", path, err)
	}
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return nil, fmt.Errorf("protect log %s: %w", path, err)
	}
	return file, nil
}

func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func ProcessGroupAlive(pgid int) bool {
	if pgid <= 0 {
		return false
	}
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func processIdentity(pid int) (string, error) {
	command := exec.Command("ps", "-p", fmt.Sprintf("%d", pid), "-o", "lstart=")
	command.Env = CommandEnvironment(map[string]string{"LC_ALL": "C"})
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	identity := strings.TrimSpace(string(output))
	if identity == "" {
		return "", errors.New("process start token is empty")
	}
	return identity, nil
}

func VerifyProcess(process ServiceProcess) error {
	if !ProcessAlive(process.PID) {
		return nil
	}
	if len(process.Command) == 0 {
		return fmt.Errorf("refusing to stop %s: saved command is empty", process.Name)
	}
	if process.Identity == "" {
		return fmt.Errorf("refusing to stop %s: saved process identity is empty", process.Name)
	}
	currentPGID, err := syscall.Getpgid(process.PID)
	if err != nil {
		return fmt.Errorf("inspect %s process group: %w", process.Name, err)
	}
	if process.PGID > 0 && currentPGID != process.PGID {
		return fmt.Errorf("refusing to stop %s: pid %d process group changed from %d to %d", process.Name, process.PID, process.PGID, currentPGID)
	}
	identity, err := processIdentity(process.PID)
	if err != nil {
		return fmt.Errorf("inspect %s process identity: %w", process.Name, err)
	}
	if identity != process.Identity {
		return fmt.Errorf("refusing to stop %s: pid %d start identity changed", process.Name, process.PID)
	}
	return nil
}

func StopProcess(process ServiceProcess, timeout time.Duration) error {
	if !ProcessAlive(process.PID) {
		if ProcessGroupAlive(process.PGID) {
			return fmt.Errorf("refusing to stop %s: leader pid %d exited while process group %d is still active", process.Name, process.PID, process.PGID)
		}
		return nil
	}
	if err := VerifyProcess(process); err != nil {
		return err
	}
	target := process.PID
	if process.PGID > 0 {
		target = -process.PGID
	}
	if err := syscall.Kill(target, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("stop %s: %w", process.Name, err)
	}
	if waitForManagedExit(process.PID, process.PGID, timeout) {
		return nil
	}
	if managedProcessAlive(process.PID, process.PGID) {
		if err := syscall.Kill(target, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("force stop %s: %w", process.Name, err)
		}
	}
	if !waitForManagedExit(process.PID, process.PGID, 2*time.Second) {
		return fmt.Errorf("force stop %s: process group %d is still active", process.Name, process.PGID)
	}
	return nil
}

func ForceStopProcessGroup(process ServiceProcess, timeout time.Duration) error {
	if process.PGID <= 0 {
		return fmt.Errorf("force stop %s: saved process group is invalid", process.Name)
	}
	if !ProcessGroupAlive(process.PGID) {
		return nil
	}
	target := -process.PGID
	if err := syscall.Kill(target, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("force stop %s: %w", process.Name, err)
	}
	if waitForManagedExit(process.PID, process.PGID, timeout) {
		return nil
	}
	if err := syscall.Kill(target, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("force stop %s: %w", process.Name, err)
	}
	if !waitForManagedExit(process.PID, process.PGID, 2*time.Second) {
		return fmt.Errorf("force stop %s: process group %d is still active", process.Name, process.PGID)
	}
	return nil
}

func managedProcessAlive(pid int, pgid int) bool {
	if pgid > 0 {
		return ProcessGroupAlive(pgid)
	}
	return ProcessAlive(pid)
}

func waitForManagedExit(pid int, pgid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for managedProcessAlive(pid, pgid) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	return !managedProcessAlive(pid, pgid)
}

func waitCommandContext(ctx context.Context, command *exec.Cmd, signal syscall.Signal, timeout time.Duration) error {
	pid := command.Process.Pid
	pgid := 0
	if currentPGID, err := syscall.Getpgid(pid); err == nil && currentPGID > 0 {
		pgid = currentPGID
	}
	finished := make(chan error, 1)
	go func() {
		finished <- command.Wait()
	}()
	select {
	case err := <-finished:
		return err
	case <-ctx.Done():
	}

	target := pid
	if pgid > 0 {
		target = -pgid
	}
	if err := syscall.Kill(target, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
		return errors.Join(ctx.Err(), err)
	}
	if waitForManagedExit(pid, pgid, timeout) {
		<-finished
		return ctx.Err()
	}
	if err := syscall.Kill(target, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return errors.Join(ctx.Err(), err)
	}
	<-finished
	if !waitForManagedExit(pid, pgid, 2*time.Second) {
		if pgid > 0 {
			return errors.Join(ctx.Err(), fmt.Errorf("process group %d is still active", pgid))
		}
		return errors.Join(ctx.Err(), fmt.Errorf("process %d is still active", pid))
	}
	return ctx.Err()
}
