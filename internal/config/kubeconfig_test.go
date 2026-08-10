package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/model"
)

func TestResolveKubeconfigPriority(t *testing.T) {
	tests := []struct {
		name       string
		explicit   string
		conven       string
		ktctl      string
		profile    string
		configured string
		manifest   string
		kubernetes string
		want       string
	}{
		{
			name:       "explicit",
			explicit:   "/config/explicit",
			conven:       "/config/conven",
			ktctl:      "/config/ktctl",
			profile:    "/config/profile",
			configured: "/config/configured",
			manifest:   "/config/manifest",
			kubernetes: "/config/kubernetes",
			want:       "/config/explicit",
		},
		{
			name:       "conven environment",
			conven:       "/config/conven",
			ktctl:      "/config/ktctl",
			profile:    "/config/profile",
			configured: "/config/configured",
			manifest:   "/config/manifest",
			kubernetes: "/config/kubernetes",
			want:       "/config/conven",
		},
		{
			name:       "legacy ktctl environment",
			ktctl:      "/config/ktctl",
			profile:    "/config/profile",
			configured: "/config/configured",
			manifest:   "/config/manifest",
			kubernetes: "/config/kubernetes",
			want:       "/config/ktctl",
		},
		{
			name:       "manifest named environment",
			profile:    "/config/profile",
			configured: "/config/configured",
			manifest:   "/config/manifest",
			kubernetes: "/config/kubernetes",
			want:       "/config/profile",
		},
		{
			name:       "conven config setting",
			configured: "/config/configured",
			manifest:   "/config/manifest",
			kubernetes: "/config/kubernetes",
			want:       "/config/configured",
		},
		{
			name:       "manifest path",
			manifest:   "/config/manifest",
			kubernetes: "/config/kubernetes",
			want:       "/config/manifest",
		},
		{
			name:       "kubernetes environment",
			kubernetes: "/config/kubernetes",
			want:       "/config/kubernetes",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearKubeconfigEnvironment(t)
			t.Setenv("CONVEN_KUBECONFIG", test.conven)
			t.Setenv("KTCTL_KUBECONFIG", test.ktctl)
			t.Setenv("PROFILE_KUBECONFIG", test.profile)
			t.Setenv("KUBECONFIG", test.kubernetes)
			connection := model.Connection{
				KubeconfigEnv: "PROFILE_KUBECONFIG",
				Kubeconfig:    test.manifest,
			}

			got, err := ResolveKubeconfig(connection, test.explicit, test.configured)
			if err != nil {
				t.Fatalf("ResolveKubeconfig returned error: %v", err)
			}
			if got != test.want {
				t.Fatalf("ResolveKubeconfig = %q, want %q", got, test.want)
			}
		})
	}
}

func TestResolveKubeconfigUsesDefaultPath(t *testing.T) {
	clearKubeconfigEnvironment(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := ResolveKubeconfig(model.Connection{}, "", "")
	if err != nil {
		t.Fatalf("ResolveKubeconfig returned error: %v", err)
	}
	want := filepath.Join(home, ".kube", "config")
	if got != want {
		t.Fatalf("ResolveKubeconfig = %q, want %q", got, want)
	}
}

func TestResolveKubeconfigExpandsHome(t *testing.T) {
	clearKubeconfigEnvironment(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := ResolveKubeconfig(model.Connection{}, "~/.kube/dev", "")
	if err != nil {
		t.Fatalf("ResolveKubeconfig returned error: %v", err)
	}
	want := filepath.Join(home, ".kube", "dev")
	if got != want {
		t.Fatalf("ResolveKubeconfig = %q, want %q", got, want)
	}
}

func TestResolveKubeconfigRejectsMultipleFiles(t *testing.T) {
	clearKubeconfigEnvironment(t)
	t.Setenv("KUBECONFIG", strings.Join([]string{"/config/one", "/config/two"}, string(os.PathListSeparator)))

	_, err := ResolveKubeconfig(model.Connection{}, "", "")
	if err == nil || !strings.Contains(err.Error(), "multiple kubeconfig files") {
		t.Fatalf("error = %v, want multiple kubeconfig files error", err)
	}
}

func clearKubeconfigEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("CONVEN_KUBECONFIG", "")
	t.Setenv("KTCTL_KUBECONFIG", "")
	t.Setenv("PROFILE_KUBECONFIG", "")
	t.Setenv("KUBECONFIG", "")
}
