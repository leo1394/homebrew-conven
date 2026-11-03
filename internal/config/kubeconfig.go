package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/model"
)

func ResolveKubeconfig(connection model.Connection, explicit string, configured string) (string, error) {
	path, _, err := ResolveKubeconfigWithSource(connection, explicit, configured)
	return path, err
}

func ResolveKubeconfigWithSource(connection model.Connection, explicit string, configured string) (string, string, error) {
	resolve := func(value, source string) (string, string, error) { path, err := normalizeKubeconfig(value, source); return path, source, err }
	if value := strings.TrimSpace(explicit); value != "" {
		return resolve(value, "command line")
	}
	if value := strings.TrimSpace(os.Getenv("CONVEN_KUBECONFIG")); value != "" {
		return resolve(value, "CONVEN_KUBECONFIG")
	}
	if value := strings.TrimSpace(os.Getenv("KTCTL_KUBECONFIG")); value != "" {
		return resolve(value, "KTCTL_KUBECONFIG")
	}

	if environmentVariable := strings.TrimSpace(connection.KubeconfigEnv); environmentVariable != "" {
		if value := strings.TrimSpace(os.Getenv(environmentVariable)); value != "" {
			return resolve(value, environmentVariable)
		}
	}
	if value := strings.TrimSpace(configured); value != "" {
		return resolve(value, "ktctl.kubeconfig")
	}
	if value := strings.TrimSpace(connection.Kubeconfig); value != "" {
		return resolve(value, "manifest connection.kubeconfig")
	}
	if value := strings.TrimSpace(os.Getenv("KUBECONFIG")); value != "" {
		return resolve(value, "KUBECONFIG")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", "default", fmt.Errorf("resolve default kubeconfig: %w", err)
	}
	return filepath.Join(home, ".kube", "config"), "default ~/.kube/config", nil
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
