package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/leo1394/homebrew-conven/examples"
)

const runtimeIgnoreRule = "/runtime/"
const backupsIgnoreRule = "/backups/"

type InitResult struct {
	Path        string
	Created     bool
	Files       []InitFileResult
	Discovered  []string
	Skipped     []string
	UsedExample bool
}

type InitFileResult struct {
	Name    string
	Created bool
}

func InitWorkspace(cwd string, application []byte) (string, bool, error) {
	result, err := InitWorkspaceDetails(cwd, application)
	return result.Path, result.Created, err
}

func InitWorkspaceDetails(cwd string, application []byte) (InitResult, error) {
	return InitWorkspaceDetailsWithPolicySpecification(cwd, application, examples.WorkspacePolicyGeneratorAISpec)
}

func InitWorkspaceDetailsWithPolicySpecification(cwd string, application []byte, specification []byte) (InitResult, error) {
	result := InitResult{}
	workspaceFiles := examples.WorkspaceFilesForPolicySpecification(specification)
	workspace, err := ResolveDirectory(cwd)
	if err != nil {
		return result, err
	}
	if sameDirectory(workspace, resolvedUserHome()) {
		return result, fmt.Errorf("cannot initialize a Conven workspace in the user home directory %q: ~/.conven is reserved for global configuration", workspace)
	}
	if err := validateWorkspaceFiles(workspace, workspaceFiles); err != nil {
		return result, err
	}
	boundary := filepath.Join(workspace, ".conven")
	info, err := os.Lstat(boundary)
	if err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
		return result, fmt.Errorf("Conven workspace boundary %q is not a directory; symbolic links are not allowed", boundary)
	}
	if err != nil && !os.IsNotExist(err) {
		return result, fmt.Errorf("inspect Conven workspace boundary %q: %w", boundary, err)
	}
	manifest := ManifestPath(workspace)
	result.Path = manifest
	info, err = os.Lstat(manifest)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return result, fmt.Errorf("Conven manifest %q must be a regular file, not a symbolic link", manifest)
		}
		if err := ensureRuntimeIgnored(boundary); err != nil {
			return result, err
		}
		files, err := ensureWorkspaceFiles(workspace, workspaceFiles)
		if err != nil {
			return result, err
		}
		result.Files = files
		return result, nil
	}
	if !os.IsNotExist(err) {
		return result, fmt.Errorf("inspect Conven manifest %q: %w", manifest, err)
	}
	discovered, skipped, err := ScanServices(workspace)
	if err != nil {
		return result, err
	}
	result.Skipped = append([]string(nil), skipped...)
	for _, service := range discovered {
		result.Discovered = append(result.Discovered, service.Name)
	}
	if len(discovered) > 0 {
		application, err = RenderDiscoveredManifest(workspace, discovered)
		if err != nil {
			return result, err
		}
	} else {
		result.UsedExample = true
	}
	if err := os.MkdirAll(boundary, 0700); err != nil {
		return result, fmt.Errorf("create Conven workspace boundary %q: %w", boundary, err)
	}
	if err := ensureRuntimeIgnored(boundary); err != nil {
		return result, err
	}
	created, err := publishNewManifest(manifest, application)
	if err != nil {
		return result, err
	}
	result.Created = created
	files, err := ensureWorkspaceFiles(workspace, workspaceFiles)
	if err != nil {
		return result, err
	}
	result.Files = files
	return result, nil
}

func validateWorkspaceFiles(workspace string, workspaceFiles []examples.WorkspaceFile) error {
	for _, file := range workspaceFiles {
		if file.Name == "" || filepath.Base(file.Name) != file.Name || file.Name == "." {
			return fmt.Errorf("invalid Conven workspace file name %q", file.Name)
		}
		path := filepath.Join(workspace, file.Name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect Conven workspace file %q: %w", path, err)
		}
		if err := validateWorkspaceFile(path, info); err != nil {
			return err
		}
	}
	return nil
}

func ensureWorkspaceFiles(workspace string, workspaceFiles []examples.WorkspaceFile) ([]InitFileResult, error) {
	results := make([]InitFileResult, 0, len(workspaceFiles))
	for _, file := range workspaceFiles {
		path := filepath.Join(workspace, file.Name)
		info, err := os.Lstat(path)
		if err == nil {
			if err := validateWorkspaceFile(path, info); err != nil {
				return nil, err
			}
			results = append(results, InitFileResult{Name: file.Name})
			continue
		}
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("inspect Conven workspace file %q: %w", path, err)
		}
		created, err := publishNewFile(path, file.Data, 0644, "Conven workspace file")
		if err != nil {
			return nil, err
		}
		if created {
			results = append(results, InitFileResult{Name: file.Name, Created: true})
			continue
		}
		info, err = os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect concurrently created Conven workspace file %q: %w", path, err)
		}
		if err := validateWorkspaceFile(path, info); err != nil {
			return nil, err
		}
		results = append(results, InitFileResult{Name: file.Name})
	}
	return results, nil
}

func validateWorkspaceFile(path string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("Conven workspace file %q must be a regular file; symbolic links are not allowed", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("Conven workspace file %q must be a regular file", path)
	}
	return nil
}

func ensureRuntimeIgnored(boundary string) error {
	return ensureConvenPathIgnored(boundary, runtimeIgnoreRule)
}

func ensureBackupsIgnored(boundary string) error {
	return ensureConvenPathIgnored(boundary, backupsIgnoreRule)
}

func ensureConvenPathIgnored(boundary string, rule string) error {
	path := filepath.Join(boundary, ".gitignore")
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("Conven gitignore %q must be a regular file, not a symbolic link", path)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect Conven gitignore %q: %w", path, err)
	}
	var data []byte
	if err == nil {
		data, err = os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read Conven gitignore %q: %w", path, err)
		}
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSuffix(line, "\r") == rule {
			return nil
		}
	}
	prefix := ""
	if len(data) > 0 && data[len(data)-1] != '\n' {
		prefix = "\n"
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return fmt.Errorf("open Conven gitignore %q: %w", path, err)
	}
	if _, err := file.WriteString(prefix + rule + "\n"); err != nil {
		file.Close()
		return fmt.Errorf("update Conven gitignore %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close Conven gitignore %q: %w", path, err)
	}
	return nil
}

func publishNewManifest(path string, data []byte) (bool, error) {
	created, err := publishNewFile(path, data, 0600, "Conven manifest")
	if err != nil || created {
		return created, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return false, fmt.Errorf("inspect concurrently created Conven manifest %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, fmt.Errorf("Conven manifest %q must be a regular file, not a symbolic link", path)
	}
	return false, nil
}

func publishNewFile(path string, data []byte, mode os.FileMode, label string) (bool, error) {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".conven-init-*")
	if err != nil {
		return false, fmt.Errorf("create temporary %s: %w", label, err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return false, fmt.Errorf("protect temporary %s: %w", label, err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return false, fmt.Errorf("write temporary %s: %w", label, err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return false, fmt.Errorf("sync temporary %s: %w", label, err)
	}
	if err := temporary.Close(); err != nil {
		return false, fmt.Errorf("close temporary %s: %w", label, err)
	}
	if err := os.Link(temporaryName, path); err != nil {
		if os.IsExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("publish %s %q: %w", label, path, err)
	}
	_ = syncDirectory(filepath.Dir(path))
	return true, nil
}
