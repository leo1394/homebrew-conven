package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func ResolvePath(cwd string) (configPath string, workspace string, err error) {
	cwd, err = resolveCwd(cwd)
	if err != nil {
		return "", "", err
	}

	home := resolvedUserHome()
	current := cwd
	for {
		if !sameDirectory(current, home) {
			path, found, err := findConfigInWorkspace(current)
			if err != nil {
				return "", "", err
			}
			if found {
				return path, current, nil
			}
		}

		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}

	return "", "", fmt.Errorf("not a Conven workspace (or any parent): .loom/loom.yaml not found from %q; run \"conven init\"", cwd)
}

func FindWorkspace(cwd string) (string, error) {
	current, err := resolveCwd(cwd)
	if err != nil {
		return "", err
	}
	home := resolvedUserHome()
	for {
		if sameDirectory(current, home) {
			parent := filepath.Dir(current)
			if parent == current {
				break
			}
			current = parent
			continue
		}
		boundary := filepath.Join(current, ".loom")
		info, statErr := os.Stat(boundary)
		if statErr == nil {
			if !info.IsDir() {
				return "", fmt.Errorf("Conven workspace boundary %q is not a directory", boundary)
			}
			return current, nil
		}
		if !os.IsNotExist(statErr) {
			return "", fmt.Errorf("inspect Conven workspace boundary %q: %w", boundary, statErr)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return "", fmt.Errorf("not a Conven workspace (or any parent): .loom directory not found; run \"conven init\"")
}

func resolveCwd(cwd string) (string, error) {
	if strings.TrimSpace(cwd) == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get current directory: %w", err)
		}
	}

	path, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve current directory %q: %w", cwd, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("inspect current directory %q: %w", path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("current directory %q is not a directory", path)
	}
	return filepath.Clean(path), nil
}

func resolvedUserHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	path, err := filepath.Abs(home)
	if err != nil {
		return ""
	}
	return filepath.Clean(path)
}

func sameDirectory(left string, right string) bool {
	if left == "" || right == "" {
		return false
	}
	if left == right {
		return true
	}
	leftInfo, err := os.Stat(left)
	if err != nil {
		return false
	}
	rightInfo, err := os.Stat(right)
	if err != nil {
		return false
	}
	return os.SameFile(leftInfo, rightInfo)
}

func findConfigInWorkspace(workspace string) (path string, found bool, err error) {
	boundary := filepath.Join(workspace, ".loom")
	info, statErr := os.Stat(boundary)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("inspect Conven workspace boundary %q: %w", boundary, statErr)
	}
	if !info.IsDir() {
		return "", false, fmt.Errorf("Conven workspace boundary %q is not a directory", boundary)
	}
	candidate := filepath.Join(boundary, "loom.yaml")
	info, statErr = os.Stat(candidate)
	if statErr == nil {
		if info.IsDir() {
			return "", false, fmt.Errorf("Conven manifest %q is a directory", candidate)
		}
		return filepath.Clean(candidate), true, nil
	}
	if !os.IsNotExist(statErr) {
		return "", false, fmt.Errorf("inspect Conven manifest %q: %w", candidate, statErr)
	}
	return "", false, fmt.Errorf("Conven workspace boundary %q does not contain loom.yaml; run \"conven init\"", boundary)
}

func ResolveDirectory(cwd string) (string, error) {
	return resolveCwd(cwd)
}

func EnsureWorkspaceBoundary(workspace string) error {
	boundary := filepath.Join(workspace, ".loom")
	info, err := os.Stat(boundary)
	if err != nil {
		return fmt.Errorf("inspect Conven workspace boundary %q: %w", boundary, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("Conven workspace boundary %q is not a directory", boundary)
	}
	return nil
}

func ManifestPath(workspace string) string {
	return filepath.Join(workspace, ".loom", "loom.yaml")
}

func LocalSettingsPath(workspace string) string {
	return filepath.Join(workspace, ".loom", "config")
}

func GlobalSettingsPath(home string) (string, error) {
	if strings.TrimSpace(home) == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
	}
	path, err := filepath.Abs(home)
	if err != nil {
		return "", fmt.Errorf("resolve home directory %q: %w", home, err)
	}
	return filepath.Join(filepath.Clean(path), ".loom", "config"), nil
}
