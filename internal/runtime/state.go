package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const stateVersion = 3

const runtimeIgnoreRule = "/runtime/"

var errWorkspaceLocked = errors.New("another Conven command is active for this workspace")

type Session struct {
	Version     int                 `json:"version"`
	Workspace   string              `json:"workspace"`
	ConfigPath  string              `json:"configPath"`
	Environment string              `json:"environment"`
	Cluster     string              `json:"cluster,omitempty"`
	CreatedAt   time.Time           `json:"createdAt"`
	Selected    []string            `json:"selected,omitempty"`
	Services    []ServiceProcess    `json:"services"`
	HotReload   *ServiceProcess     `json:"hotReload,omitempty"`
	Connection  *ConnectionProcess  `json:"connection,omitempty"`
}

type ServiceProcess struct {
	Name              string         `json:"name"`
	PID               int            `json:"pid"`
	PGID              int            `json:"pgid"`
	Command           []string       `json:"command"`
	Identity          string         `json:"identity"`
	LogPath           string         `json:"logPath"`
	StartedAt         time.Time      `json:"startedAt"`
	Ports              map[string]int `json:"ports"`
	SourceFingerprint string         `json:"sourceFingerprint,omitempty"`
	PlanFingerprint   string         `json:"planFingerprint,omitempty"`
}

type ConnectionProcess struct {
	Driver     string    `json:"driver"`
	PID        int       `json:"pid"`
	PGID       int       `json:"pgid"`
	Command    []string  `json:"command"`
	Identity   string    `json:"identity"`
	LogPath    string    `json:"logPath"`
	StartedAt  time.Time `json:"startedAt"`
	Owned      bool      `json:"owned"`
	Managed    bool      `json:"managed"`
	Elevated   bool      `json:"elevated"`
	Fingerprint string   `json:"fingerprint"`
}

type Store struct {
	Root       string
	CurrentDir string
	SessionFile string
	lockFile   string
	boundary   string
}

func NewStore(workspace string) (*Store, error) {
	absolute, err := filepath.Abs(workspace)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace for state: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("canonicalize workspace for state: %w", err)
	}
	canonical = filepath.Clean(canonical)
	boundary := filepath.Join(canonical, ".conven")
	workspaceRoot := filepath.Join(boundary, "runtime")
	return &Store{
		Root:        workspaceRoot,
		CurrentDir:  filepath.Join(workspaceRoot, "current"),
		SessionFile: filepath.Join(workspaceRoot, "session.json"),
		lockFile:    filepath.Join(workspaceRoot, ".lock"),
		boundary:    boundary,
	}, nil
}

func (store *Store) ensureRoot() error {
	boundaryInfo, err := os.Lstat(store.boundary)
	if err != nil {
		return fmt.Errorf("inspect Conven workspace boundary %q: %w", store.boundary, err)
	}
	if boundaryInfo.Mode()&os.ModeSymlink != 0 || !boundaryInfo.IsDir() {
		return fmt.Errorf("Conven workspace boundary %q must be a real directory", store.boundary)
	}
	rootInfo, err := os.Lstat(store.Root)
	if os.IsNotExist(err) {
		if err := os.Mkdir(store.Root, 0700); err != nil {
			return fmt.Errorf("create runtime directory: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("inspect runtime directory: %w", err)
	} else if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return fmt.Errorf("runtime path %q must be a real directory", store.Root)
	}
	if err := os.Chmod(store.Root, 0700); err != nil {
		return fmt.Errorf("protect runtime directory: %w", err)
	}
	return nil
}

func (store *Store) ensureIgnored() error {
	path := filepath.Join(store.boundary, ".gitignore")
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("Conven gitignore %q must be a regular file, not a symbolic link", path)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect Conven gitignore: %w", err)
	}
	var data []byte
	if err == nil {
		data, err = os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read Conven gitignore: %w", err)
		}
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSuffix(line, "\r") == runtimeIgnoreRule {
			return nil
		}
	}
	prefix := ""
	if len(data) > 0 && data[len(data)-1] != '\n' {
		prefix = "\n"
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return fmt.Errorf("open Conven gitignore: %w", err)
	}
	if _, err := file.WriteString(prefix + runtimeIgnoreRule + "\n"); err != nil {
		file.Close()
		return fmt.Errorf("update Conven gitignore: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close Conven gitignore: %w", err)
	}
	return nil
}

func (store *Store) Lock() (func(), error) {
	if err := store.ensureRoot(); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(store.lockFile); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("workspace state lock %q must be a regular file, not a symbolic link", store.lockFile)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect workspace state lock: %w", err)
	}
	file, err := os.OpenFile(store.lockFile, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open workspace state lock: %w", err)
	}
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return nil, fmt.Errorf("protect workspace state lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, errWorkspaceLocked
		}
		return nil, fmt.Errorf("lock workspace state: %w", err)
	}
	if err := file.Truncate(0); err != nil {
		unix.Flock(int(file.Fd()), unix.LOCK_UN)
		file.Close()
		return nil, fmt.Errorf("clear workspace state lock owner: %w", err)
	}
	owner := []byte(fmt.Sprintf("pid=%d\n", os.Getpid()))
	if _, err := file.WriteAt(owner, 0); err != nil {
		unix.Flock(int(file.Fd()), unix.LOCK_UN)
		file.Close()
		return nil, fmt.Errorf("record state lock owner: %w", err)
	}
	if err := store.ensureIgnored(); err != nil {
		unix.Flock(int(file.Fd()), unix.LOCK_UN)
		file.Close()
		return nil, err
	}
	return func() {
		unix.Flock(int(file.Fd()), unix.LOCK_UN)
		file.Close()
	}, nil
}

func (store *Store) ResetCurrent() error {
	if err := store.ensureRoot(); err != nil {
		return err
	}
	relative, err := filepath.Rel(store.Root, store.CurrentDir)
	if err != nil || relative != "current" {
		return fmt.Errorf("refuse to reset unexpected current runtime path %q", store.CurrentDir)
	}
	info, err := os.Lstat(store.CurrentDir)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("current runtime path %q must be a real directory", store.CurrentDir)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect current runtime directory: %w", err)
	}
	if err := os.RemoveAll(store.CurrentDir); err != nil {
		return fmt.Errorf("clear current runtime directory: %w", err)
	}
	for _, directory := range []string{
		store.CurrentDir,
		filepath.Join(store.CurrentDir, "artifacts"),
		filepath.Join(store.CurrentDir, "configs"),
		filepath.Join(store.CurrentDir, "logs"),
	} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			return fmt.Errorf("create current runtime directory: %w", err)
		}
		if err := os.Chmod(directory, 0700); err != nil {
			return fmt.Errorf("protect current runtime directory: %w", err)
		}
	}
	return nil
}

func (store *Store) CleanupCurrentOutputs() error {
	if err := store.ensureRoot(); err != nil {
		return err
	}
	relative, err := filepath.Rel(store.Root, store.CurrentDir)
	if err != nil || relative != "current" {
		return fmt.Errorf("refuse to clean unexpected current runtime path %q", store.CurrentDir)
	}
	info, err := os.Lstat(store.CurrentDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect current runtime directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("current runtime path %q must be a real directory", store.CurrentDir)
	}
	targets := []string{
		filepath.Join(store.CurrentDir, "artifacts"),
		filepath.Join(store.CurrentDir, "logs"),
	}
	for _, target := range targets {
		info, err := os.Lstat(target)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect cleanup target %q: %w", target, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("cleanup target %q must be a real directory", target)
		}
	}
	if err := os.Chmod(store.CurrentDir, 0700); err != nil {
		return fmt.Errorf("protect current runtime directory: %w", err)
	}
	for _, target := range targets {
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("clear runtime output %q: %w", target, err)
		}
		if err := os.Mkdir(target, 0700); err != nil {
			return fmt.Errorf("recreate runtime output %q: %w", target, err)
		}
		if err := os.Chmod(target, 0700); err != nil {
			return fmt.Errorf("protect runtime output %q: %w", target, err)
		}
	}
	return nil
}

func (store *Store) InspectCurrent() error {
	if err := store.ensureRoot(); err != nil {
		return err
	}
	for _, directory := range []string{
		store.CurrentDir,
		filepath.Join(store.CurrentDir, "artifacts"),
		filepath.Join(store.CurrentDir, "configs"),
		filepath.Join(store.CurrentDir, "logs"),
	} {
		info, err := os.Lstat(directory)
		if err != nil {
			return fmt.Errorf("inspect current runtime directory %q: %w", directory, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("current runtime path %q must be a real directory", directory)
		}
		if err := os.Chmod(directory, 0700); err != nil {
			return fmt.Errorf("protect current runtime directory %q: %w", directory, err)
		}
	}
	return nil
}

func (store *Store) Save(session *Session) error {
	if session.Version == 0 {
		session.Version = stateVersion
	}
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session state: %w", err)
	}
	data = append(data, '\n')
	if err := store.ensureRoot(); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(store.Root, ".session-*.json")
	if err != nil {
		return fmt.Errorf("create temporary session state: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return fmt.Errorf("protect temporary session state: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write session state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close session state: %w", err)
	}
	if err := os.Rename(temporaryName, store.SessionFile); err != nil {
		return fmt.Errorf("publish session state: %w", err)
	}
	return nil
}

func (store *Store) Load() (*Session, error) {
	info, err := os.Lstat(store.SessionFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("inspect session state: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("session state %q must be a regular file, not a symbolic link", store.SessionFile)
	}
	if err := os.Chmod(store.SessionFile, 0600); err != nil {
		return nil, fmt.Errorf("protect session state: %w", err)
	}
	data, err := os.ReadFile(store.SessionFile)
	if err != nil {
		return nil, fmt.Errorf("read session state: %w", err)
	}
	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("decode session state: %w", err)
	}
	if session.Version != stateVersion {
		return nil, fmt.Errorf("unsupported session state version %d", session.Version)
	}
	return &session, nil
}

func (store *Store) Clear() error {
	if err := os.Remove(store.SessionFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove session state: %w", err)
	}
	return nil
}
