package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const runtimeIgnoreRule = "/runtime/"
const backupsIgnoreRule = "/backups/"

type InitResult struct {
	Path        string
	Created     bool
	Discovered  []string
	Skipped     []string
	UsedExample bool
}

func InitWorkspace(cwd string, application []byte) (string, bool, error) {
	result, err := InitWorkspaceDetails(cwd, application)
	return result.Path, result.Created, err
}

func InitWorkspaceDetails(cwd string, application []byte) (InitResult, error) {
	result := InitResult{}
	workspace, err := ResolveDirectory(cwd)
	if err != nil {
		return result, err
	}
	if sameDirectory(workspace, resolvedUserHome()) {
		return result, fmt.Errorf("cannot initialize a Conven workspace in the user home directory %q: ~/.conven is reserved for global configuration", workspace)
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
	return result, nil
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
	temporary, err := os.CreateTemp(filepath.Dir(path), ".conven-init-*")
	if err != nil {
		return false, fmt.Errorf("create temporary Conven manifest: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return false, fmt.Errorf("protect temporary Conven manifest: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return false, fmt.Errorf("write temporary Conven manifest: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return false, fmt.Errorf("sync temporary Conven manifest: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return false, fmt.Errorf("close temporary Conven manifest: %w", err)
	}
	if err := os.Link(temporaryName, path); err != nil {
		if os.IsExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("publish Conven manifest %q: %w", path, err)
	}
	_ = syncDirectory(filepath.Dir(path))
	return true, nil
}
