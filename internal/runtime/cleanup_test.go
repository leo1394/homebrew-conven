package runtime

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanupRuntimeClearsArtifactsAndLogsOnly(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	if err := store.ResetCurrent(); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(store.CurrentDir, "artifacts", "api", "api")
	log := filepath.Join(store.CurrentDir, "logs", "api.log")
	config := filepath.Join(store.CurrentDir, "configs", "api", "application.yaml")
	connectionLog := ConnectionLogPath(store.Root)
	for _, path := range []string{artifact, log, config, connectionLog} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("keep or clear\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	var output bytes.Buffer
	if err := CleanupRuntime(store, &output); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{artifact, log} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("cleanup target %q still exists: %v", path, err)
		}
	}
	for _, path := range []string{config, connectionLog} {
		if data, err := os.ReadFile(path); err != nil || string(data) != "keep or clear\n" {
			t.Fatalf("preserved runtime file %q changed: data=%q err=%v", path, data, err)
		}
	}
	for _, directory := range []string{
		filepath.Join(store.CurrentDir, "artifacts"),
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
	if !strings.Contains(output.String(), "Cleared build artifacts and service logs") || !strings.Contains(output.String(), store.CurrentDir) {
		t.Fatalf("cleanup output = %q", output.String())
	}
}

func TestCleanupRuntimeRejectsSavedSessionWithoutChangingFiles(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	if err := store.ResetCurrent(); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(store.CurrentDir, "artifacts", "api")
	log := filepath.Join(store.CurrentDir, "logs", "api.log")
	for _, path := range []string{artifact, log} {
		if err := os.WriteFile(path, []byte("keep\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Save(&Session{Workspace: "workspace"}); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	err := CleanupRuntime(store, &output)
	if err == nil || !strings.Contains(err.Error(), "conven services --stop-all") {
		t.Fatalf("CleanupRuntime error = %v, want stop-all guidance", err)
	}
	if output.Len() != 0 {
		t.Fatalf("cleanup output = %q", output.String())
	}
	for _, path := range []string{artifact, log, store.SessionFile} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("saved session cleanup changed %q: %v", path, err)
		}
	}
}

func TestCleanupRuntimeIsIdempotentWithoutCurrentDirectory(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	for index := 0; index < 2; index++ {
		if err := CleanupRuntime(store, nil); err != nil {
			t.Fatalf("cleanup %d: %v", index+1, err)
		}
	}
}

func TestCleanupRuntimeHonorsWorkspaceLock(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	if err := store.ResetCurrent(); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(store.CurrentDir, "artifacts", "api")
	if err := os.WriteFile(artifact, []byte("keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	unlock, err := store.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	if err := CleanupRuntime(store, nil); err == nil || !strings.Contains(err.Error(), "another Conven command") {
		t.Fatalf("CleanupRuntime error = %v, want lock conflict", err)
	}
	if _, err := os.Stat(artifact); err != nil {
		t.Fatalf("lock conflict changed artifact: %v", err)
	}
}

func TestStoreCleanupCurrentOutputsRejectsNestedSymlinkBeforeDeleting(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	if err := store.ResetCurrent(); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(store.CurrentDir, "artifacts", "api")
	if err := os.WriteFile(artifact, []byte("keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	logs := filepath.Join(store.CurrentDir, "logs")
	if err := os.Remove(logs); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	marker := filepath.Join(target, "marker")
	if err := os.WriteFile(marker, []byte("external\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, logs); err != nil {
		t.Fatal(err)
	}

	err := store.CleanupCurrentOutputs()
	if err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("CleanupCurrentOutputs error = %v, want symlink rejection", err)
	}
	if data, err := os.ReadFile(artifact); err != nil || string(data) != "keep\n" {
		t.Fatalf("artifact changed before nested symlink rejection: data=%q err=%v", data, err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "external\n" {
		t.Fatalf("external symlink target changed: data=%q err=%v", data, err)
	}
}

func TestStoreCleanupCurrentOutputsRejectsUnexpectedPath(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	outside := t.TempDir()
	marker := filepath.Join(outside, "marker")
	if err := os.WriteFile(marker, []byte("keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store.CurrentDir = outside

	err := store.CleanupCurrentOutputs()
	if err == nil || !strings.Contains(err.Error(), "unexpected current runtime path") {
		t.Fatalf("CleanupCurrentOutputs error = %v, want path boundary rejection", err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "keep\n" {
		t.Fatalf("unexpected path changed: data=%q err=%v", data, err)
	}
}
