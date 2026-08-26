package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func LoadEnvironmentValues(workspace string, environment map[string]string, envFile string) (map[string]string, error) {
	values := make(map[string]string, len(environment))
	for key, value := range environment {
		values[key] = value
	}
	if strings.TrimSpace(envFile) == "" {
		return values, nil
	}
	path := envFile
	if !filepath.IsAbs(path) {
		path = filepath.Join(workspace, path)
	}
	path = filepath.Clean(path)
	relative, err := filepath.Rel(workspace, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("environment file must stay within the workspace")
	}
	current := workspace
	for _, segment := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, segment)
		component, componentErr := os.Lstat(current)
		if componentErr != nil {
			return nil, fmt.Errorf("inspect environment file %q: %w", path, componentErr)
		}
		if component.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("environment file %q must not contain symbolic links", path)
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect environment file %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("environment file %q must be a regular file, not a symbolic link", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open environment file %q: %w", path, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, value, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !found || !validEnvironmentKey(key) {
			return nil, fmt.Errorf("parse environment file %q line %d: expected KEY=VALUE", path, lineNumber)
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"' || value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1:len(value)-1]
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read environment file %q: %w", path, err)
	}
	for _, entry := range os.Environ() {
		key, value, found := strings.Cut(entry, "=")
		if found {
			values[key] = value
		}
	}
	return values, nil
}

func validEnvironmentKey(key string) bool {
	for index, character := range key {
		if character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character == '_' || index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return key != ""
}
