package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/model"
)

func ResolveKubeconfig(connection model.Connection, explicit string, configured string) (string, error) {
	if value := strings.TrimSpace(explicit); value != "" {
		return normalizeKubeconfig(value, "command line")
	}
	if value := strings.TrimSpace(os.Getenv("LOOM_KUBECONFIG")); value != "" {
		return normalizeKubeconfig(value, "LOOM_KUBECONFIG")
	}
	if value := strings.TrimSpace(os.Getenv("KTCTL_KUBECONFIG")); value != "" {
		return normalizeKubeconfig(value, "KTCTL_KUBECONFIG")
	}

	if environmentVariable := strings.TrimSpace(connection.KubeconfigEnv); environmentVariable != "" {
		if value := strings.TrimSpace(os.Getenv(environmentVariable)); value != "" {
			return normalizeKubeconfig(value, environmentVariable)
		}
	}
	if value := strings.TrimSpace(configured); value != "" {
		return normalizeKubeconfig(value, "ktctl.kubeconfig")
	}
	if value := strings.TrimSpace(connection.Kubeconfig); value != "" {
		return normalizeKubeconfig(value, "manifest connection.kubeconfig")
	}
	if value := strings.TrimSpace(os.Getenv("KUBECONFIG")); value != "" {
		return normalizeKubeconfig(value, "KUBECONFIG")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve default kubeconfig: %w", err)
	}
	return filepath.Join(home, ".kube", "config"), nil
}

func normalizeKubeconfig(value string, source string) (string, error) {
	paths := filepath.SplitList(strings.TrimSpace(value))
	if len(paths) > 1 {
		return "", fmt.Errorf("%s contains multiple kubeconfig files; Conven currently requires a single file", source)
	}
	if len(paths) == 0 || strings.TrimSpace(paths[0]) == "" {
		return "", fmt.Errorf("%s does not contain a kubeconfig path", source)
	}

	path := strings.TrimSpace(paths[0])
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand %s path %q: %w", source, path, err)
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return filepath.Clean(path), nil
}
