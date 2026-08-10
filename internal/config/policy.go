package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const maxPolicyImportSize = 16 << 20

type PolicyEditResult struct {
	Path      string
	Changed   bool
	DraftPath string
}

type PolicyResetResult struct {
	Path        string
	BackupPath  string
	Changed     bool
	Created     bool
	Discovered  []string
	Skipped     []string
}

type PolicyImportResult struct {
	Path       string
	SourcePath string
	BackupPath string
	DraftPath  string
	Changed    bool
	Created    bool
}

type policyCandidateOptions struct {
	validationName string
	candidateName  string
	operation      string
	draftPattern   string
	backupPattern  string
	backupLabel    string
}

func EditWorkspacePolicy(cwd string, edit func(string) error) (PolicyEditResult, error) {
	result := PolicyEditResult{}
	workspace, boundary, err := policyWorkspace(cwd)
	if err != nil {
		return result, err
	}
	path := ManifestPath(workspace)
	result.Path = path
	source, sourceInfo, err := readManifestForUpdate(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return result, fmt.Errorf("Conven manifest %q is missing; run \"conven policy --reset\" or import a complete manifest with \"conven policy --import\"", path)
		}
		return result, err
	}
	if edit == nil {
		return result, fmt.Errorf("policy editor is not configured")
	}

	backupDirectory, err := ensurePolicyBackupDirectory(boundary)
	if err != nil {
		return result, err
	}
	draft, err := os.CreateTemp(backupDirectory, "conven.yaml-edit-*.yaml")
	if err != nil {
		return result, fmt.Errorf("create policy edit draft: %w", err)
	}
	draftPath := draft.Name()
	removeDraft := true
	defer func() {
		if removeDraft {
			_ = os.Remove(draftPath)
		}
	}()
	if err := draft.Chmod(0600); err != nil {
		draft.Close()
		return result, fmt.Errorf("protect policy edit draft: %w", err)
	}
	if _, err := draft.Write(source); err != nil {
		draft.Close()
		return result, fmt.Errorf("write policy edit draft: %w", err)
	}
	if err := draft.Sync(); err != nil {
		draft.Close()
		return result, fmt.Errorf("sync policy edit draft: %w", err)
	}
	if err := draft.Close(); err != nil {
		return result, fmt.Errorf("close policy edit draft: %w", err)
	}

	editErr := edit(draftPath)
	candidate, readErr := readPolicyDraft(draftPath)
	if readErr != nil {
		if editErr != nil {
			return result, fmt.Errorf("policy editor failed: %v; inspect edited draft: %w", editErr, readErr)
		}
		return result, readErr
	}
	if editErr != nil {
		if bytes.Equal(candidate, source) {
			return result, fmt.Errorf("policy editor failed: %w", editErr)
		}
		removeDraft = false
		result.DraftPath = draftPath
		return result, fmt.Errorf("policy editor failed: %w; edited draft was not published by Conven and is kept at %q", editErr, draftPath)
	}
	if _, err := decodeManifest(candidate, draftPath); err != nil {
		removeDraft = false
		result.DraftPath = draftPath
		return result, fmt.Errorf("edited policy manifest is invalid: %w; edited draft was not published by Conven and is kept at %q", err, draftPath)
	}
	if bytes.Equal(candidate, source) {
		if err := verifyManifestSnapshot(path, source, sourceInfo, "policy edit"); err != nil {
			return result, err
		}
		return result, nil
	}
	if err := publishManifestUpdate(path, candidate, source, sourceInfo, "policy edit"); err != nil {
		removeDraft = false
		result.DraftPath = draftPath
		return result, fmt.Errorf("%w; edited draft was not published by Conven and is kept at %q", err, draftPath)
	}
	result.Changed = true
	return result, nil
}

func ImportWorkspacePolicy(cwd string, importPath string, edit func(string) error) (PolicyImportResult, error) {
	result := PolicyImportResult{}
	workspace, boundary, err := policyWorkspace(cwd)
	if err != nil {
		return result, err
	}
	invocationDirectory, err := ResolveDirectory(cwd)
	if err != nil {
		return result, err
	}
	sourcePath := importPath
	if !filepath.IsAbs(sourcePath) {
		sourcePath = filepath.Join(invocationDirectory, sourcePath)
	}
	sourcePath = filepath.Clean(sourcePath)
	manifestPath := ManifestPath(workspace)
	if sourcePath == manifestPath {
		return result, fmt.Errorf("policy import %q is the current workspace manifest; choose a separate source file", sourcePath)
	}
	imported, importInfo, err := readPolicyImportFile(sourcePath)
	if err != nil {
		return result, err
	}
	manifestInfo, statErr := os.Stat(manifestPath)
	if statErr == nil && os.SameFile(importInfo, manifestInfo) {
		return result, fmt.Errorf("policy import %q is the current workspace manifest; choose a separate source file", sourcePath)
	}
	if statErr != nil && !os.IsNotExist(statErr) {
		return result, fmt.Errorf("inspect current Conven manifest %q before policy import: %w", manifestPath, statErr)
	}
	result, err = applyWorkspacePolicyCandidate(workspace, boundary, imported, edit, policyCandidateOptions{
		validationName: fmt.Sprintf("policy import %q", sourcePath),
		candidateName:  "imported policy manifest",
		operation:      "policy import",
		draftPattern:   "conven.yaml-import-edit-*.yaml",
		backupPattern:  "conven.yaml-before-import-*.bak",
		backupLabel:    "import",
	})
	result.SourcePath = sourcePath
	return result, err
}

func applyWorkspacePolicyCandidate(workspace string, boundary string, input []byte, edit func(string) error, options policyCandidateOptions) (PolicyImportResult, error) {
	result := PolicyImportResult{}
	path := ManifestPath(workspace)
	result.Path = path
	source, sourceInfo, missing, err := readPolicyManifest(path)
	if err != nil {
		return result, err
	}
	candidate := append([]byte(nil), input...)
	if edit == nil {
		if _, err := decodeManifest(candidate, options.validationName); err != nil {
			return result, fmt.Errorf("validate %s: %w", options.validationName, err)
		}
	}
	draftPath := ""
	removeDraft := false
	defer func() {
		if removeDraft {
			_ = os.Remove(draftPath)
		}
	}()
	keepDraftOnError := func(failure error) error {
		if draftPath == "" {
			return failure
		}
		removeDraft = false
		result.DraftPath = draftPath
		return fmt.Errorf("%w; edited draft was not published by Conven and is kept at %q", failure, draftPath)
	}

	if edit != nil {
		draftPath, err = savePolicySnapshot(boundary, options.draftPattern, candidate)
		if err != nil {
			return result, fmt.Errorf("create %s edit draft: %w", options.candidateName, err)
		}
		removeDraft = true

		editErr := edit(draftPath)
		edited, readErr := readPolicyDraft(draftPath)
		if readErr != nil {
			if editErr != nil {
				return result, fmt.Errorf("%s editor failed: %v; inspect edited draft: %w", options.candidateName, editErr, readErr)
			}
			return result, readErr
		}
		if editErr != nil {
			if bytes.Equal(edited, candidate) {
				return result, fmt.Errorf("%s editor failed: %w", options.candidateName, editErr)
			}
			removeDraft = false
			result.DraftPath = draftPath
			return result, fmt.Errorf("%s editor failed: %w; edited draft was not published by Conven and is kept at %q", options.candidateName, editErr, draftPath)
		}
		if _, err := decodeManifest(edited, draftPath); err != nil {
			removeDraft = false
			result.DraftPath = draftPath
			return result, fmt.Errorf("edited %s is invalid: %w; edited draft was not published by Conven and is kept at %q", options.candidateName, err, draftPath)
		}
		candidate = edited
	}

	if !missing && bytes.Equal(candidate, source) {
		if err := verifyManifestSnapshot(path, source, sourceInfo, options.operation); err != nil {
			return result, keepDraftOnError(err)
		}
		return result, nil
	}
	if err := ensureRuntimeIgnored(boundary); err != nil {
		return result, keepDraftOnError(err)
	}
	if missing {
		created, err := publishNewManifest(path, candidate)
		if err != nil {
			return result, keepDraftOnError(err)
		}
		if !created {
			return result, keepDraftOnError(fmt.Errorf("Conven manifest %q was created during %s; retry the command", path, options.operation))
		}
		result.Changed = true
		result.Created = true
		return result, nil
	}

	backup, err := savePolicySnapshot(boundary, options.backupPattern, source)
	if err != nil {
		return result, keepDraftOnError(fmt.Errorf("back up Conven manifest before %s: %w", options.operation, err))
	}
	result.BackupPath = backup
	if err := publishManifestUpdate(path, candidate, source, sourceInfo, options.operation); err != nil {
		return result, keepDraftOnError(fmt.Errorf("%w; pre-%s manifest backup kept at %q", err, options.backupLabel, backup))
	}
	result.Changed = true
	return result, nil
}

func readPolicyImportFile(path string) ([]byte, os.FileInfo, error) {
	observed, err := os.Lstat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect policy import %q: %w", path, err)
	}
	if observed.Mode()&os.ModeSymlink != 0 || !observed.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("policy import %q must be a real regular file, not a symbolic link", path)
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open policy import %q without following symbolic links: %w", path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("inspect opened policy import %q: %w", path, err)
	}
	if !info.Mode().IsRegular() || !os.SameFile(observed, info) {
		return nil, nil, fmt.Errorf("policy import %q changed while it was opened; retry the command", path)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxPolicyImportSize+1))
	if err != nil {
		return nil, nil, fmt.Errorf("read policy import %q: %w", path, err)
	}
	if len(data) > maxPolicyImportSize {
		return nil, nil, fmt.Errorf("policy import %q exceeds the %d-byte size limit", path, maxPolicyImportSize)
	}
	return data, info, nil
}

func ResetWorkspacePolicyFromScan(cwd string) (PolicyResetResult, error) {
	result := PolicyResetResult{}
	workspace, boundary, err := policyWorkspace(cwd)
	if err != nil {
		return result, err
	}
	path := ManifestPath(workspace)
	result.Path = path
	source, sourceInfo, missing, err := readPolicyManifest(path)
	if err != nil {
		return result, err
	}
	discovered, skipped, err := ScanServices(workspace)
	if err != nil {
		return result, err
	}
	result.Skipped = append([]string(nil), skipped...)
	for _, service := range discovered {
		result.Discovered = append(result.Discovered, service.Name)
	}
	if len(discovered) == 0 {
		return result, fmt.Errorf("scan found no supported direct-child repositories; policy reset did not publish a manifest")
	}
	candidate, err := RenderDiscoveredManifest(workspace, discovered)
	if err != nil {
		return result, err
	}
	if _, err := decodeManifest(candidate, path); err != nil {
		return result, fmt.Errorf("validate scan-reset Conven manifest: %w", err)
	}
	if !missing && bytes.Equal(candidate, source) {
		if err := verifyManifestSnapshot(path, source, sourceInfo, "policy reset"); err != nil {
			return result, err
		}
		return result, nil
	}
	if err := ensureRuntimeIgnored(boundary); err != nil {
		return result, err
	}
	if missing {
		created, err := publishNewManifest(path, candidate)
		if err != nil {
			return result, err
		}
		if !created {
			return result, fmt.Errorf("Conven manifest %q was created during policy reset; retry the command", path)
		}
		result.Changed = true
		result.Created = true
		return result, nil
	}

	backup, err := savePolicySnapshot(boundary, "conven.yaml-before-reset-*.bak", source)
	if err != nil {
		return result, fmt.Errorf("back up Conven manifest before policy reset: %w", err)
	}
	result.BackupPath = backup
	if err := publishManifestUpdate(path, candidate, source, sourceInfo, "policy reset"); err != nil {
		return result, fmt.Errorf("%w; pre-reset manifest backup kept at %q", err, backup)
	}
	result.Changed = true
	return result, nil
}

func policyWorkspace(cwd string) (string, string, error) {
	workspace, err := FindWorkspace(cwd)
	if err != nil {
		return "", "", err
	}
	boundary := filepath.Join(workspace, ".conven")
	info, err := os.Lstat(boundary)
	if err != nil {
		return "", "", fmt.Errorf("inspect Conven workspace boundary %q: %w", boundary, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", "", fmt.Errorf("Conven workspace boundary %q must be a real directory, not a symbolic link", boundary)
	}
	return workspace, boundary, nil
}

func readPolicyManifest(path string) ([]byte, os.FileInfo, bool, error) {
	_, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil, true, nil
	}
	if err != nil {
		return nil, nil, false, fmt.Errorf("inspect Conven manifest %q: %w", path, err)
	}
	data, info, err := readManifestForUpdate(path)
	if err != nil {
		return nil, nil, false, err
	}
	return data, info, false, nil
}

func readPolicyDraft(path string) ([]byte, error) {
	data, _, err := readManifestForUpdate(path)
	if err != nil {
		return nil, fmt.Errorf("inspect edited policy draft %q: %w", path, err)
	}
	return data, nil
}

func savePolicySnapshot(boundary string, pattern string, data []byte) (string, error) {
	directory, err := ensurePolicyBackupDirectory(boundary)
	if err != nil {
		return "", err
	}
	file, err := os.CreateTemp(directory, pattern)
	if err != nil {
		return "", fmt.Errorf("create conven policy snapshot: %w", err)
	}
	path := file.Name()
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return "", fmt.Errorf("protect conven policy snapshot: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return "", fmt.Errorf("write conven policy snapshot: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return "", fmt.Errorf("sync conven policy snapshot: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close conven policy snapshot: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return "", err
	}
	remove = false
	return path, nil
}

func ensurePolicyBackupDirectory(boundary string) (string, error) {
	directory := filepath.Join(boundary, "backups")
	created := false
	info, err := os.Lstat(directory)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("conven policy backup directory %q must be a real directory, not a symbolic link", directory)
		}
	} else if os.IsNotExist(err) {
		if err := os.Mkdir(directory, 0700); err != nil {
			return "", fmt.Errorf("create conven policy backup directory %q: %w", directory, err)
		}
		created = true
	} else {
		return "", fmt.Errorf("inspect conven policy backup directory %q: %w", directory, err)
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return "", fmt.Errorf("protect conven policy backup directory %q: %w", directory, err)
	}
	if created {
		if err := syncDirectory(boundary); err != nil {
			return "", err
		}
	}
	if err := ensureBackupsIgnored(boundary); err != nil {
		return "", err
	}
	return directory, nil
}
