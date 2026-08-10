package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

func LoadSettings(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("open conven config %q: %w", path, err)
	}
	defer file.Close()

	values := map[string]string{}
	decoder := yaml.NewDecoder(file)
	if err := decoder.Decode(&values); err != nil {
		if err == io.EOF {
			return values, nil
		}
		return nil, fmt.Errorf("decode conven config %q: %w", path, err)
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("decode conven config %q: %w", path, err)
		}
		return nil, fmt.Errorf("conven config %q contains multiple YAML documents", path)
	}
	for key, value := range values {
		if err := validateSetting(key, value); err != nil {
			return nil, fmt.Errorf("validate conven config %q: %w", path, err)
		}
	}
	return values, nil
}

func EffectiveSettings(workspace string, home string) (map[string]string, error) {
	globalPath, err := GlobalSettingsPath(home)
	if err != nil {
		return nil, err
	}
	values, err := LoadSettings(globalPath)
	if err != nil {
		return nil, err
	}
	local, err := LoadSettings(LocalSettingsPath(workspace))
	if err != nil {
		return nil, err
	}
	for key, value := range local {
		values[key] = value
	}
	return values, nil
}

func ScopeSettings(workspace string, home string, global bool) (map[string]string, error) {
	if global {
		path, err := GlobalSettingsPath(home)
		if err != nil {
			return nil, err
		}
		return LoadSettings(path)
	}
	return EffectiveSettings(workspace, home)
}

func SetSetting(workspace string, home string, global bool, key string, value string) error {
	if err := validateSetting(key, value); err != nil {
		return err
	}
	path := LocalSettingsPath(workspace)
	if global {
		var err error
		path, err = GlobalSettingsPath(home)
		if err != nil {
			return err
		}
	} else if err := EnsureWorkspaceBoundary(workspace); err != nil {
		return err
	}
	values, err := LoadSettings(path)
	if err != nil {
		return err
	}
	values[key] = value
	return saveSettings(path, values)
}

func UnsetSetting(workspace string, home string, global bool, key string) error {
	if err := validateSettingKey(key); err != nil {
		return err
	}
	path := LocalSettingsPath(workspace)
	if global {
		var err error
		path, err = GlobalSettingsPath(home)
		if err != nil {
			return err
		}
	} else if err := EnsureWorkspaceBoundary(workspace); err != nil {
		return err
	}
	values, err := LoadSettings(path)
	if err != nil {
		return err
	}
	if _, found := values[key]; !found {
		return fmt.Errorf("config key %q is not set in the selected scope", key)
	}
	delete(values, key)
	return saveSettings(path, values)
}

func SortedSettingKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func validateSetting(key string, value string) error {
	if err := validateSettingKey(key); err != nil {
		return err
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("config value for %q must not be empty", key)
	}
	if key == "ktctl.path" {
		path := strings.TrimSpace(value)
		if strings.ContainsRune(path, filepath.Separator) && !filepath.IsAbs(path) && !strings.HasPrefix(path, "~/") {
			return fmt.Errorf("ktctl.path must be an absolute path, a ~/ path, or a command name")
		}
	}
	if key == "ktctl.kubeconfig" {
		if _, err := normalizeKubeconfig(value, "ktctl.kubeconfig"); err != nil {
			return err
		}
	}
	return nil
}

func validateSettingKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("config key must not be empty")
	}
	for index, character := range key {
		letter := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
		digit := character >= '0' && character <= '9'
		if index == 0 && !letter && !digit {
			return fmt.Errorf("invalid config key %q", key)
		}
		if !letter && !digit && character != '.' && character != '_' && character != '-' {
			return fmt.Errorf("invalid config key %q", key)
		}
	}
	return nil
}

func saveSettings(path string, values map[string]string) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("create conven config directory %q: %w", directory, err)
	}
	data, err := yaml.Marshal(values)
	if err != nil {
		return fmt.Errorf("encode conven config %q: %w", path, err)
	}
	temporary, err := os.CreateTemp(directory, ".config-*")
	if err != nil {
		return fmt.Errorf("create temporary conven config: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return fmt.Errorf("protect temporary conven config: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary conven config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary conven config: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("publish conven config %q: %w", path, err)
	}
	return nil
}

func ResolveExecutable(value string) (string, error) {
	path := strings.TrimSpace(value)
	if !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
}
