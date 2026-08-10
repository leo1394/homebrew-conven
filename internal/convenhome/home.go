package convenhome

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func Root(home string) (string, error) {
	if strings.TrimSpace(home) != "" {
		path, err := filepath.Abs(home)
		if err != nil {
			return "", fmt.Errorf("resolve home directory %q: %w", home, err)
		}
		return filepath.Join(filepath.Clean(path), ".conven"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	path, err := filepath.Abs(home)
	if err != nil {
		return "", fmt.Errorf("resolve home directory %q: %w", home, err)
	}
	return filepath.Join(filepath.Clean(path), ".conven"), nil
}
