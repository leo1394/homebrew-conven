package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

const editablePolicyManifest = `# original comment
version: 2
workspace:
  name: test
services:
  api:
    path: api
    runner:
      run: [api]
`

const templatePolicyManifest = `# template comment
version: 2
workspace:
  name: template
services:
  api:
    path: api
    runner:
      run: [api]
`

func TestEditWorkspacePolicyPublishesExactValidatedDraft(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, editablePolicyManifest)
	nested := filepath.Join(workspace, "nested", "directory")
	mustMkdirAll(t, nested)
	candidate := strings.Replace(editablePolicyManifest, "# original comment", "# edited comment", 1)
	candidate = strings.Replace(candidate, "name: test", "name: edited", 1)

	result, err := EditWorkspacePolicy(nested, func(path string) error {
		if filepath.Dir(path) != filepath.Join(workspace, ".conven", "backups") {
			t.Fatalf("draft path = %q", path)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("draft mode = %o", info.Mode().Perm())
		}
		return os.WriteFile(path, []byte(candidate), 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Path != manifestPath || result.DraftPath != "" {
		t.Fatalf("result = %#v", result)
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != candidate {
		t.Fatalf("manifest was re-encoded:\n%s", data)
	}
	backupDirectory := filepath.Join(workspace, ".conven", "backups")
	entries, err := os.ReadDir(backupDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("successful edit retained draft files: %v", entries)
	}
	info, err := os.Stat(backupDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("backup directory mode = %o", info.Mode().Perm())
	}
	ignore, err := os.ReadFile(filepath.Join(workspace, ".conven", ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(ignore) != "/backups/\n" {
		t.Fatalf("gitignore = %q", ignore)
	}
}

func TestEditWorkspacePolicyNormalizesLeadingIndentationTabs(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, editablePolicyManifest)
	candidate := strings.Replace(editablePolicyManifest, "  name: test", "\tname: edited", 1)
	want := strings.Replace(editablePolicyManifest, "  name: test", "  name: edited", 1)

	result, err := EditWorkspacePolicy(workspace, func(path string) error {
		return os.WriteFile(path, []byte(candidate), 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.DraftPath != "" {
		t.Fatalf("result = %#v", result)
	}
	assertFileContents(t, manifestPath, want)
}

func TestNormalizeYAMLIndentationPreservesTabsAfterContent(t *testing.T) {
	input := []byte("\tname: \"left\tright\"\n  value: |\n    text\tvalue\n")
	want := "  name: \"left\tright\"\n  value: |\n    text\tvalue\n"

	got, changed := normalizeYAMLIndentation(input)
	if !changed || string(got) != want {
		t.Fatalf("normalizeYAMLIndentation() = (%q, %v), want (%q, true)", got, changed, want)
	}
}

func TestEditWorkspacePolicyNoChangeDoesNotReplaceManifest(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, editablePolicyManifest)
	before, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatal(err)
	}

	result, err := EditWorkspacePolicy(workspace, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed {
		t.Fatalf("result = %#v", result)
	}
	after, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("unchanged edit replaced the manifest")
	}
}

func TestEditWorkspacePolicyRejectsInvalidDraftAndPreservesIt(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, editablePolicyManifest)

	result, err := EditWorkspacePolicy(workspace, func(path string) error {
		return os.WriteFile(path, []byte("version: 2\nunknown: true\n"), 0600)
	})
	if err == nil || !strings.Contains(err.Error(), "edited policy manifest is invalid") || !strings.Contains(err.Error(), "is kept") {
		t.Fatalf("error = %v", err)
	}
	if result.DraftPath == "" || filepath.Dir(result.DraftPath) != filepath.Join(workspace, ".conven", "backups") {
		t.Fatalf("result = %#v", result)
	}
	draft, readErr := os.ReadFile(result.DraftPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(draft) != "version: 2\nunknown: true\n" {
		t.Fatalf("preserved draft = %q", draft)
	}
	assertFileContents(t, manifestPath, editablePolicyManifest)
	ignore, err := os.ReadFile(filepath.Join(workspace, ".conven", ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(ignore) != "/backups/\n" {
		t.Fatalf("gitignore = %q", ignore)
	}
}

func TestEditWorkspacePolicyRejectsConcurrentManifestEdit(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, editablePolicyManifest)
	concurrent := strings.Replace(editablePolicyManifest, "# original comment", "# concurrent edit", 1)
	candidate := strings.Replace(editablePolicyManifest, "# original comment", "# draft edit", 1)

	result, err := EditWorkspacePolicy(workspace, func(path string) error {
		if err := os.WriteFile(path, []byte(candidate), 0600); err != nil {
			return err
		}
		return os.WriteFile(manifestPath, []byte(concurrent), 0600)
	})
	if err == nil || !strings.Contains(err.Error(), "edited during policy edit") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "canonical manifest is unchanged") {
		t.Fatalf("error makes an unprovable canonical-state claim: %v", err)
	}
	if result.DraftPath == "" {
		t.Fatalf("result = %#v", result)
	}
	assertFileContents(t, manifestPath, concurrent)
	assertFileContents(t, result.DraftPath, candidate)
}

func TestEditWorkspacePolicyNoOpRejectsConcurrentManifestEdit(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, editablePolicyManifest)
	concurrent := strings.Replace(editablePolicyManifest, "# original comment", "# concurrent edit", 1)

	result, err := EditWorkspacePolicy(workspace, func(string) error {
		return os.WriteFile(manifestPath, []byte(concurrent), 0600)
	})
	if err == nil || !strings.Contains(err.Error(), "edited during policy edit") {
		t.Fatalf("error = %v", err)
	}
	if result.Changed || result.DraftPath != "" {
		t.Fatalf("result = %#v", result)
	}
	assertFileContents(t, manifestPath, concurrent)
}

func TestEditWorkspacePolicyCanRepairInvalidManifest(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, "broken: [\n")

	result, err := EditWorkspacePolicy(workspace, func(path string) error {
		return os.WriteFile(path, []byte(editablePolicyManifest), 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Fatalf("result = %#v", result)
	}
	if _, err := Load(manifestPath); err != nil {
		t.Fatal(err)
	}
}

func TestEditWorkspacePolicyMissingManifestSuggestsReset(t *testing.T) {
	workspace := t.TempDir()
	mustMkdirAll(t, filepath.Join(workspace, ".conven"))
	_, err := EditWorkspacePolicy(workspace, func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "policy --reset") {
		t.Fatalf("error = %v", err)
	}
}

func TestImportWorkspacePolicyCopiesLocalFileAndBacksUpManifest(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, editablePolicyManifest)
	importDirectory := t.TempDir()
	importPath := filepath.Join(importDirectory, "reusable-policy.yaml")
	if err := os.WriteFile(importPath, []byte(templatePolicyManifest), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := ImportWorkspacePolicy(workspace, importPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Created || result.SourcePath != importPath || result.BackupPath == "" || result.DraftPath != "" {
		t.Fatalf("result = %#v", result)
	}
	assertFileContents(t, manifestPath, templatePolicyManifest)
	assertFileContents(t, importPath, templatePolicyManifest)
	assertFileContents(t, result.BackupPath, editablePolicyManifest)
	if !strings.HasPrefix(filepath.Base(result.BackupPath), "conven.yaml-before-import-") {
		t.Fatalf("backup path = %q", result.BackupPath)
	}
}

func TestImportWorkspacePolicyNoOpPreservesManifestAndCreatesNoBackup(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, editablePolicyManifest)
	before, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	importPath := filepath.Join(t.TempDir(), "same-policy.yaml")
	if err := os.WriteFile(importPath, []byte(editablePolicyManifest), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := ImportWorkspacePolicy(workspace, importPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed || result.Created || result.BackupPath != "" || result.DraftPath != "" {
		t.Fatalf("result = %#v", result)
	}
	after, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("no-op import replaced the manifest inode")
	}
	if _, err := os.Stat(filepath.Join(workspace, ".conven", "backups")); !os.IsNotExist(err) {
		t.Fatalf("no-op import created backups: %v", err)
	}
}

func TestImportWorkspacePolicyCreatesMissingPrivateManifest(t *testing.T) {
	workspace := t.TempDir()
	boundary := filepath.Join(workspace, ".conven")
	if err := os.Mkdir(boundary, 0700); err != nil {
		t.Fatal(err)
	}
	importPath := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(importPath, []byte(templatePolicyManifest), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := ImportWorkspacePolicy(workspace, importPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || !result.Created || result.BackupPath != "" {
		t.Fatalf("result = %#v", result)
	}
	info, err := os.Stat(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("manifest permissions = %04o, want 0600", info.Mode().Perm())
	}
	assertFileContents(t, result.Path, templatePolicyManifest)
}

func TestImportWorkspacePolicyResolvesRelativePathFromInvocationDirectory(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, editablePolicyManifest)
	nested := filepath.Join(workspace, "nested")
	mustMkdirAll(t, nested)
	importPath := filepath.Join(nested, "policy.yaml")
	if err := os.WriteFile(importPath, []byte(templatePolicyManifest), 0600); err != nil {
		t.Fatal(err)
	}

	result, err := ImportWorkspacePolicy(nested, "policy.yaml", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.SourcePath != importPath || !result.Changed {
		t.Fatalf("result = %#v", result)
	}
	assertFileContents(t, manifestPath, templatePolicyManifest)
}

func TestImportWorkspacePolicyEditsImportSeedBeforePublishing(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, editablePolicyManifest)
	importPath := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(importPath, []byte(templatePolicyManifest), 0600); err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(templatePolicyManifest, "name: template", "name: imported", 1)
	seenDraft := ""

	result, err := ImportWorkspacePolicy(workspace, importPath, func(path string) error {
		seenDraft = path
		assertFileContents(t, path, templatePolicyManifest)
		return os.WriteFile(path, []byte(edited), 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.BackupPath == "" || result.DraftPath != "" || seenDraft == "" {
		t.Fatalf("result = %#v, draft = %q", result, seenDraft)
	}
	assertFileContents(t, manifestPath, edited)
	assertFileContents(t, importPath, templatePolicyManifest)
	if _, err := os.Stat(seenDraft); !os.IsNotExist(err) {
		t.Fatalf("successful import retained draft: %v", err)
	}
}

func TestImportWorkspacePolicyRejectsInvalidFileWithoutWriting(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, editablePolicyManifest)
	importPath := filepath.Join(t.TempDir(), "invalid.yaml")
	if err := os.WriteFile(importPath, []byte("version: 2\nunknown: true\n"), 0600); err != nil {
		t.Fatal(err)
	}

	result, err := ImportWorkspacePolicy(workspace, importPath, nil)
	if err == nil || !strings.Contains(err.Error(), "validate policy import") {
		t.Fatalf("error = %v", err)
	}
	if result.Changed || result.BackupPath != "" || result.DraftPath != "" {
		t.Fatalf("result = %#v", result)
	}
	assertFileContents(t, manifestPath, editablePolicyManifest)
	if _, err := os.Stat(filepath.Join(workspace, ".conven", "backups")); !os.IsNotExist(err) {
		t.Fatalf("invalid import created backups: %v", err)
	}
}

func TestImportWorkspacePolicyRejectsMissingOrDirectorySource(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, editablePolicyManifest)

	if _, err := ImportWorkspacePolicy(workspace, "missing.yaml", nil); err == nil || !strings.Contains(err.Error(), "inspect policy import") {
		t.Fatalf("missing import error = %v", err)
	}
	if _, err := ImportWorkspacePolicy(workspace, workspace, nil); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory import error = %v", err)
	}
	assertFileContents(t, manifestPath, editablePolicyManifest)
}

func TestImportWorkspacePolicyRejectsFIFOAndOversizeSource(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, editablePolicyManifest)
	importDirectory := t.TempDir()
	fifoPath := filepath.Join(importDirectory, "policy.fifo")
	if err := unix.Mkfifo(fifoPath, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportWorkspacePolicy(workspace, fifoPath, nil); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("FIFO import error = %v", err)
	}
	overSizePath := filepath.Join(importDirectory, "oversize.yaml")
	if err := os.WriteFile(overSizePath, make([]byte, maxPolicyImportSize+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportWorkspacePolicy(workspace, overSizePath, nil); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("oversize import error = %v", err)
	}
	assertFileContents(t, manifestPath, editablePolicyManifest)
}

func TestImportWorkspacePolicyEditCanRepairInvalidSource(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, editablePolicyManifest)
	importPath := filepath.Join(t.TempDir(), "invalid.yaml")
	if err := os.WriteFile(importPath, []byte("version: [\n"), 0600); err != nil {
		t.Fatal(err)
	}

	result, err := ImportWorkspacePolicy(workspace, importPath, func(path string) error {
		assertFileContents(t, path, "version: [\n")
		return os.WriteFile(path, []byte(templatePolicyManifest), 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.BackupPath == "" {
		t.Fatalf("result = %#v", result)
	}
	assertFileContents(t, manifestPath, templatePolicyManifest)
	assertFileContents(t, importPath, "version: [\n")
}

func TestImportWorkspacePolicyRejectsSymlinkAndCurrentManifestSource(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, editablePolicyManifest)
	importDirectory := t.TempDir()
	importPath := filepath.Join(importDirectory, "policy.yaml")
	if err := os.WriteFile(importPath, []byte(templatePolicyManifest), 0600); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(importDirectory, "policy-link.yaml")
	if err := os.Symlink(importPath, symlinkPath); err != nil {
		t.Fatal(err)
	}

	if _, err := ImportWorkspacePolicy(workspace, symlinkPath, nil); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlink import error = %v", err)
	}
	if _, err := ImportWorkspacePolicy(workspace, manifestPath, nil); err == nil || !strings.Contains(err.Error(), "current workspace manifest") {
		t.Fatalf("manifest import error = %v", err)
	}
	hardlinkPath := filepath.Join(importDirectory, "manifest-hardlink.yaml")
	if err := os.Link(manifestPath, hardlinkPath); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportWorkspacePolicy(workspace, hardlinkPath, nil); err == nil || !strings.Contains(err.Error(), "current workspace manifest") {
		t.Fatalf("hardlink import error = %v", err)
	}
	assertFileContents(t, manifestPath, editablePolicyManifest)
}

func TestPolicyCommandsRejectSymbolicLinkWorkspaceBoundary(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(t.TempDir(), ".conven")
	mustMkdirAll(t, target)
	writeDiscoveryFile(t, filepath.Join(target, "conven.yaml"), editablePolicyManifest)
	importPath := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(importPath, []byte(templatePolicyManifest), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(workspace, ".conven")); err != nil {
		t.Fatal(err)
	}
	if _, err := EditWorkspacePolicy(workspace, func(string) error { return nil }); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("edit error = %v", err)
	}
	if _, err := ResetWorkspacePolicyFromScan(workspace); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("restore error = %v", err)
	}
	if _, err := ImportWorkspacePolicy(workspace, importPath, nil); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("import error = %v", err)
	}
}

func TestPolicyCommandsRejectSymbolicLinkManifest(t *testing.T) {
	workspace := t.TempDir()
	writeGoServiceRepository(t, workspace, "api-service", false, "example.com/api-service", "main")
	target := filepath.Join(workspace, "manifest-target.yaml")
	writeDiscoveryFile(t, target, editablePolicyManifest)
	boundary := filepath.Join(workspace, ".conven")
	mustMkdirAll(t, boundary)
	manifestPath := filepath.Join(boundary, "conven.yaml")
	if err := os.Symlink(target, manifestPath); err != nil {
		t.Fatal(err)
	}
	importPath := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(importPath, []byte(templatePolicyManifest), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := EditWorkspacePolicy(workspace, func(string) error { return nil }); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("edit error = %v", err)
	}
	if _, err := ResetWorkspacePolicyFromScan(workspace); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("restore error = %v", err)
	}
	if _, err := ImportWorkspacePolicy(workspace, importPath, nil); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("import error = %v", err)
	}
	assertFileContents(t, target, editablePolicyManifest)
	info, err := os.Lstat(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("policy command replaced the manifest symlink")
	}
}

func TestPublishManifestUpdateRejectsConcurrentConvenWriterLock(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, editablePolicyManifest)
	source, sourceInfo, err := readManifestForUpdate(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := openManifestNoFollow(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	if err := unix.Flock(int(locked.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer unix.Flock(int(locked.Fd()), unix.LOCK_UN)
	candidate := []byte(strings.Replace(editablePolicyManifest, "name: test", "name: candidate", 1))

	err = publishManifestUpdate(manifestPath, candidate, source, sourceInfo, "policy edit")
	if err == nil || !strings.Contains(err.Error(), "another Conven process") {
		t.Fatalf("error = %v", err)
	}
	assertFileContents(t, manifestPath, editablePolicyManifest)
}

func TestResetWorkspacePolicyFromScanBacksUpAndRebuildsManifest(t *testing.T) {
	workspace := t.TempDir()
	writeGoServiceRepository(t, workspace, "api-service", false, "example.com/api-service", "main")
	repository := filepath.Join(workspace, "api-service")
	beforeRepository := analyzerRepositorySnapshot(t, repository)
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	original := `# manual configuration
version: 2
workspace:
  name: manual
  policy: company
policies:
  company:
    drivers:
      framework: go-zero
services:
  custom-api:
    path: api-service
    policy: company
    ports:
      http: 18080
    runner:
      run: [manual]
`
	writeDiscoveryFile(t, manifestPath, original)

	result, err := ResetWorkspacePolicyFromScan(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Created || !reflect.DeepEqual(result.Discovered, []string{"api-service"}) || result.BackupPath == "" {
		t.Fatalf("result = %#v", result)
	}
	assertFileContents(t, result.BackupPath, original)
	backupInfo, err := os.Stat(result.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	if backupInfo.Mode().Perm() != 0600 {
		t.Fatalf("backup mode = %o", backupInfo.Mode().Perm())
	}
	manifest, err := Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ServiceNames(manifest), []string{"api-service"}) || len(manifest.Policies) != 0 || len(manifest.Environments) != 0 {
		t.Fatalf("reset manifest = %#v", manifest)
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, removed := range []string{"manual configuration", "company", "18080", "custom-api"} {
		if strings.Contains(string(data), removed) {
			t.Fatalf("reset manifest retained %q:\n%s", removed, data)
		}
	}
	if afterRepository := analyzerRepositorySnapshot(t, repository); !reflect.DeepEqual(afterRepository, beforeRepository) {
		t.Fatalf("source repository changed during restore:\nbefore=%#v\nafter=%#v", beforeRepository, afterRepository)
	}
	if _, err := os.Stat(filepath.Join(repository, ".conven")); !os.IsNotExist(err) {
		t.Fatalf("restore created a source repository .conven directory: %v", err)
	}
	ignore, err := os.ReadFile(filepath.Join(workspace, ".conven", ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(ignore) != "/runtime/\n/backups/\n" {
		t.Fatalf("gitignore = %q", ignore)
	}
}

func TestResetWorkspacePolicyFromScanPreservesVersionOne(t *testing.T) {
	workspace := t.TempDir()
	writeGoServiceRepository(t, workspace, "api-service", false, "example.com/api-service", "main")
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	original := `version: 1
workspace:
  name: legacy
services:
  custom-api:
    path: api-service
    runner:
      run: [manual]
`
	writeDiscoveryFile(t, manifestPath, original)

	if _, err := ResetWorkspacePolicyFromScan(workspace); err != nil {
		t.Fatal(err)
	}
	manifest, err := Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != 1 || !reflect.DeepEqual(ServiceNames(manifest), []string{"api-service"}) {
		t.Fatalf("reset legacy manifest = %#v", manifest)
	}
}

func TestResetWorkspacePolicyFromScanRepairsInvalidManifest(t *testing.T) {
	workspace := t.TempDir()
	writeGoServiceRepository(t, workspace, "api-service", false, "example.com/api-service", "main")
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	original := "invalid: [\n"
	writeDiscoveryFile(t, manifestPath, original)

	result, err := ResetWorkspacePolicyFromScan(workspace)
	if err != nil {
		t.Fatal(err)
	}
	assertFileContents(t, result.BackupPath, original)
	if _, err := Load(manifestPath); err != nil {
		t.Fatal(err)
	}
}

func TestResetWorkspacePolicyFromScanCreatesMissingManifest(t *testing.T) {
	workspace := t.TempDir()
	writeGoServiceRepository(t, workspace, "api-service", false, "example.com/api-service", "main")
	mustMkdirAll(t, filepath.Join(workspace, ".conven"))
	nested := filepath.Join(workspace, "api-service", "go")

	result, err := ResetWorkspacePolicyFromScan(nested)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || !result.Changed || result.BackupPath != "" {
		t.Fatalf("result = %#v", result)
	}
	if _, err := Load(filepath.Join(workspace, ".conven", "conven.yaml")); err != nil {
		t.Fatal(err)
	}
}

func TestResetWorkspacePolicyFromScanRejectsEmptyScanWithoutWriting(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, editablePolicyManifest)

	result, err := ResetWorkspacePolicyFromScan(workspace)
	if err == nil || !strings.Contains(err.Error(), "no supported") {
		t.Fatalf("error = %v", err)
	}
	if result.Changed || result.BackupPath != "" {
		t.Fatalf("result = %#v", result)
	}
	assertFileContents(t, manifestPath, editablePolicyManifest)
	if _, err := os.Stat(filepath.Join(workspace, ".conven", "backups")); !os.IsNotExist(err) {
		t.Fatalf("empty scan created backups: %v", err)
	}
}

func TestResetWorkspacePolicyFromScanRejectsBackupSymlink(t *testing.T) {
	workspace := t.TempDir()
	writeGoServiceRepository(t, workspace, "api-service", false, "example.com/api-service", "main")
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, editablePolicyManifest)
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(workspace, ".conven", "backups")); err != nil {
		t.Fatal(err)
	}

	_, err := ResetWorkspacePolicyFromScan(workspace)
	if err == nil || !strings.Contains(err.Error(), "backup directory") || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("error = %v", err)
	}
	assertFileContents(t, manifestPath, editablePolicyManifest)
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("backup symlink target was written: %v", entries)
	}
}

func TestEditWorkspacePolicyPreservesChangedDraftOnEditorError(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	writeDiscoveryFile(t, manifestPath, editablePolicyManifest)
	candidate := strings.Replace(editablePolicyManifest, "name: test", "name: candidate", 1)

	result, err := EditWorkspacePolicy(workspace, func(path string) error {
		if err := os.WriteFile(path, []byte(candidate), 0600); err != nil {
			return err
		}
		return errors.New("cancelled")
	})
	if err == nil || !strings.Contains(err.Error(), "policy editor failed") {
		t.Fatalf("error = %v", err)
	}
	assertFileContents(t, manifestPath, editablePolicyManifest)
	assertFileContents(t, result.DraftPath, candidate)
}

func assertFileContents(t *testing.T, path string, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}
