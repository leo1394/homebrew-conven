package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreRoundTrip(t *testing.T) {
	workspace := t.TempDir()
	store := newTestStore(t, workspace)
	canonical, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if store.Root != filepath.Join(canonical, ".conven", "runtime") {
		t.Fatalf("store root = %q", store.Root)
	}
	if store.CurrentDir != filepath.Join(canonical, ".conven", "runtime", "current") {
		t.Fatalf("current directory = %q", store.CurrentDir)
	}
	session := &Session{
		Workspace:   workspace,
		ConfigPath:  filepath.Join(workspace, ".conven", "conven.yaml"),
		Environment: "dev",
		CreatedAt:   time.Now(),
		Services: []ServiceProcess{{
			Name:    "user-svc",
			PID:     123,
			PGID:    123,
			Command: []string{"user-svc"},
			Ports:   map[string]int{"http": 8080, "metrics": 9090},
		}},
	}
	if err := store.Save(session); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.SessionFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("session permissions = %o, want 600", info.Mode().Perm())
	}
	data, err := os.ReadFile(store.SessionFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"id":`) || strings.Contains(string(data), `"runDir":`) {
		t.Fatalf("session retained removed run identity fields: %s", data)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil || loaded.Workspace != workspace || len(loaded.Services) != 1 || loaded.Services[0].Name != "user-svc" {
		t.Fatalf("unexpected loaded session: %#v", loaded)
	}
	if loaded.Version != stateVersion {
		t.Fatalf("session version = %d, want %d", loaded.Version, stateVersion)
	}
	if loaded.Services[0].Ports["http"] != 8080 || loaded.Services[0].Ports["metrics"] != 9090 {
		t.Fatalf("unexpected loaded service ports: %#v", loaded.Services[0].Ports)
	}
}

func TestStorePreservesEmptyPortSnapshot(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	session := &Session{Services: []ServiceProcess{{
		Name:  "api",
		Ports: map[string]int{},
	}}}
	if err := store.Save(session); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.SessionFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"ports": {}`) {
		t.Fatalf("empty port snapshot was not persisted: %s", data)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Services[0].Ports == nil || len(loaded.Services[0].Ports) != 0 {
		t.Fatalf("empty port snapshot = %#v", loaded.Services[0].Ports)
	}
}

func TestStoreLockRejectsSecondOwner(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	unlock, err := store.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := store.Lock(); err == nil {
		t.Fatal("second lock unexpectedly succeeded")
	}
}

func TestNewStoreCanonicalizesSymlinkedWorkspace(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, ".conven"), 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(workspace, link); err != nil {
		t.Fatal(err)
	}
	realStore, err := NewStore(workspace)
	if err != nil {
		t.Fatal(err)
	}
	linkedStore, err := NewStore(link)
	if err != nil {
		t.Fatal(err)
	}
	if realStore.Root != linkedStore.Root {
		t.Fatalf("store roots differ for the same workspace: %q != %q", realStore.Root, linkedStore.Root)
	}
}

func TestNewStoreNaturallyIsolatesSameNamedWorkspaces(t *testing.T) {
	firstWorkspace := filepath.Join(t.TempDir(), "checkout")
	secondWorkspace := filepath.Join(t.TempDir(), "checkout")
	for _, workspace := range []string{firstWorkspace, secondWorkspace} {
		if err := os.MkdirAll(filepath.Join(workspace, ".conven"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	first, err := NewStore(firstWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewStore(secondWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if first.Root == second.Root {
		t.Fatalf("same-named workspaces shared runtime root %q", first.Root)
	}
}

func TestNewStoreIgnoresUserHome(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, ".conven"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(t.TempDir(), "user-home"))
	store, err := NewStore(workspace)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(canonical, ".conven", "runtime")
	if store.Root != want {
		t.Fatalf("store root = %q, want %q", store.Root, want)
	}
	t.Setenv("HOME", filepath.Join(t.TempDir(), "other-user-home"))
	second, err := NewStore(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if second.Root != store.Root {
		t.Fatalf("state environment changed store root: %q != %q", second.Root, store.Root)
	}
}

func TestStoreResetCurrentClearsOldFilesAndProtectsDirectories(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	unlock, err := store.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	for _, path := range []string{
		filepath.Join(store.CurrentDir, "artifacts", "api", "sentinel"),
		filepath.Join(store.CurrentDir, "configs", "api", "application.yaml"),
		filepath.Join(store.CurrentDir, "logs", "api.log"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("old\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ResetCurrent(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(store.CurrentDir, "artifacts", "api", "sentinel"),
		filepath.Join(store.CurrentDir, "configs", "api", "application.yaml"),
		filepath.Join(store.CurrentDir, "logs", "api.log"),
	} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("old runtime path %q still exists: %v", path, err)
		}
	}
	for _, directory := range []string{
		store.Root,
		store.CurrentDir,
		filepath.Join(store.CurrentDir, "artifacts"),
		filepath.Join(store.CurrentDir, "configs"),
		filepath.Join(store.CurrentDir, "logs"),
	} {
		info, err := os.Stat(directory)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0700 {
			t.Fatalf("directory %q permissions = %o, want 700", directory, info.Mode().Perm())
		}
	}
	lockInfo, err := os.Stat(store.lockFile)
	if err != nil {
		t.Fatal(err)
	}
	if lockInfo.Mode().Perm() != 0600 {
		t.Fatalf("lock permissions = %o, want 600", lockInfo.Mode().Perm())
	}
}

func TestStoreLockMergesRuntimeGitignoreIdempotently(t *testing.T) {
	workspace := t.TempDir()
	store := newTestStore(t, workspace)
	gitignore := filepath.Join(workspace, ".conven", ".gitignore")
	if err := os.WriteFile(gitignore, []byte("custom-rule\nnested/runtime/"), 0644); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		unlock, err := store.Lock()
		if err != nil {
			t.Fatal(err)
		}
		unlock()
	}
	data, err := os.ReadFile(gitignore)
	if err != nil {
		t.Fatal(err)
	}
	want := "custom-rule\nnested/runtime/\n/runtime/\n"
	if string(data) != want {
		t.Fatalf("gitignore = %q, want %q", data, want)
	}
}

func TestStoreRejectsRuntimeSymlink(t *testing.T) {
	workspace := t.TempDir()
	store := newTestStore(t, workspace)
	target := t.TempDir()
	marker := filepath.Join(target, "marker")
	if err := os.WriteFile(marker, []byte("keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, store.Root); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Lock(); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("Lock error = %v, want runtime symlink rejection", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("external symlink target was changed: %v", err)
	}
}

func TestStoreResetCurrentRejectsSymlink(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	if err := os.Mkdir(store.Root, 0700); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	marker := filepath.Join(target, "marker")
	if err := os.WriteFile(marker, []byte("keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, store.CurrentDir); err != nil {
		t.Fatal(err)
	}
	if err := store.ResetCurrent(); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("ResetCurrent error = %v, want current symlink rejection", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("external symlink target was changed: %v", err)
	}
}

func TestStoreResetCurrentRejectsUnexpectedPath(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	outside := t.TempDir()
	marker := filepath.Join(outside, "marker")
	if err := os.WriteFile(marker, []byte("keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store.CurrentDir = outside
	if err := store.ResetCurrent(); err == nil || !strings.Contains(err.Error(), "unexpected current runtime path") {
		t.Fatalf("ResetCurrent error = %v, want path boundary rejection", err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "keep\n" {
		t.Fatalf("unexpected path was changed: data=%q err=%v", data, err)
	}
}

func TestStoreInspectCurrentRejectsNestedSymlink(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	if err := store.ResetCurrent(); err != nil {
		t.Fatal(err)
	}
	logs := filepath.Join(store.CurrentDir, "logs")
	if err := os.Remove(logs); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), logs); err != nil {
		t.Fatal(err)
	}
	if err := store.InspectCurrent(); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("InspectCurrent error = %v, want nested symlink rejection", err)
	}
}

func TestStoreLockRejectsLockSymlink(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	if err := os.Mkdir(store.Root, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, store.lockFile); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Lock(); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("Lock error = %v, want lock symlink rejection", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep\n" {
		t.Fatalf("lock symlink target was changed: %q", data)
	}
}

func TestStoreLoadRejectsSessionSymlink(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	if err := os.Mkdir(store.Root, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "session.json")
	if err := os.WriteFile(target, []byte(`{"version":3}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, store.SessionFile); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("Load error = %v, want session symlink rejection", err)
	}
}

func TestStoreLockAllowsStaleLockFile(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	if err := os.MkdirAll(store.Root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.lockFile, []byte("pid=999999\n"), 0600); err != nil {
		t.Fatal(err)
	}
	unlock, err := store.Lock()
	if err != nil {
		t.Fatalf("stale lock file prevented locking: %v", err)
	}
	unlock()
}

func newTestStore(t *testing.T, workspace string) *Store {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(workspace, ".conven"), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(workspace)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
