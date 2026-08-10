package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePathDiscoversWorkspaceRoot(t *testing.T) {
	workspace := t.TempDir()
	manifest := filepath.Join(workspace, ".loom", "loom.yaml")
	mustWriteFile(t, manifest)

	configPath, resolvedWorkspace, err := ResolvePath(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if configPath != manifest || resolvedWorkspace != workspace {
		t.Fatalf("ResolvePath = (%q, %q), want (%q, %q)", configPath, resolvedWorkspace, manifest, workspace)
	}
}

func TestResolvePathIgnoresLoomWorkspaceEnvironment(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	otherWorkspace := filepath.Join(root, "other")
	cwd := filepath.Join(workspace, "services", "api")
	manifest := filepath.Join(workspace, ".loom", "loom.yaml")
	mustWriteFile(t, manifest)
	mustWriteFile(t, filepath.Join(otherWorkspace, ".loom", "loom.yaml"))
	mustMkdirAll(t, cwd)
	t.Setenv("LOOM_WORKSPACE", otherWorkspace)

	configPath, resolvedWorkspace, err := ResolvePath(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if configPath != manifest || resolvedWorkspace != workspace {
		t.Fatalf("ResolvePath = (%q, %q), want (%q, %q)", configPath, resolvedWorkspace, manifest, workspace)
	}
}

func TestResolvePathDoesNotUseLoomWorkspaceEnvironmentOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "outside")
	workspace := filepath.Join(root, "workspace")
	mustMkdirAll(t, cwd)
	mustWriteFile(t, filepath.Join(workspace, ".loom", "loom.yaml"))
	t.Setenv("LOOM_WORKSPACE", workspace)

	_, _, err := ResolvePath(cwd)
	if err == nil || !strings.Contains(err.Error(), "not a Conven workspace") {
		t.Fatalf("error = %v, want cwd workspace error", err)
	}
}

func TestResolvePathDiscoversNearestDotLoomUpward(t *testing.T) {
	workspace := t.TempDir()
	manifest := filepath.Join(workspace, ".loom", "loom.yaml")
	cwd := filepath.Join(workspace, "services", "api")
	mustWriteFile(t, manifest)
	mustMkdirAll(t, cwd)

	configPath, resolvedWorkspace, err := ResolvePath(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if configPath != manifest || resolvedWorkspace != workspace {
		t.Fatalf("ResolvePath = (%q, %q), want (%q, %q)", configPath, resolvedWorkspace, manifest, workspace)
	}
}

func TestResolvePathChoosesNearestNestedWorkspace(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	cwd := filepath.Join(nested, "services", "api")
	manifest := filepath.Join(nested, ".loom", "loom.yaml")
	mustWriteFile(t, filepath.Join(root, ".loom", "loom.yaml"))
	mustWriteFile(t, manifest)
	mustMkdirAll(t, cwd)

	configPath, workspace, err := ResolvePath(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if configPath != manifest || workspace != nested {
		t.Fatalf("ResolvePath = (%q, %q), want (%q, %q)", configPath, workspace, manifest, nested)
	}
}

func TestResolvePathRejectsRootManifestWithoutDotLoom(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "loom.yaml"))

	_, _, err := ResolvePath(root)
	if err == nil || !strings.Contains(err.Error(), "not a Conven workspace") {
		t.Fatalf("error = %v, want workspace boundary error", err)
	}
}

func TestResolvePathStopsAtIncompleteBoundary(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	mustWriteFile(t, filepath.Join(root, ".loom", "loom.yaml"))
	mustMkdirAll(t, filepath.Join(nested, ".loom"))

	_, _, err := ResolvePath(nested)
	if err == nil || !strings.Contains(err.Error(), "does not contain loom.yaml") {
		t.Fatalf("error = %v, want incomplete boundary error", err)
	}
}

func TestResolvePathRejectsAlternativeManifestAtNearestBoundary(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	mustWriteFile(t, filepath.Join(root, ".loom", "loom.yaml"))
	mustWriteFile(t, filepath.Join(nested, ".loom", "custom.yaml"))

	_, _, err := ResolvePath(nested)
	if err == nil || !strings.Contains(err.Error(), "does not contain loom.yaml") {
		t.Fatalf("error = %v, want canonical manifest error", err)
	}
}

func TestGlobalConfigDirectoryIsNotWorkspaceBoundary(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "projects", "outside")
	t.Setenv("HOME", home)
	mustWriteFile(t, filepath.Join(home, ".loom", "config"))
	mustMkdirAll(t, cwd)

	_, _, err := ResolvePath(cwd)
	if err == nil || !strings.Contains(err.Error(), "not a Conven workspace") {
		t.Fatalf("ResolvePath error = %v, want outside-workspace error", err)
	}
	if strings.Contains(err.Error(), "does not contain loom.yaml") {
		t.Fatalf("ResolvePath treated global config as an incomplete workspace: %v", err)
	}

	_, err = FindWorkspace(cwd)
	if err == nil || !strings.Contains(err.Error(), "not a Conven workspace") {
		t.Fatalf("FindWorkspace error = %v, want outside-workspace error", err)
	}
}

func TestHomeDirectoryIsReservedForGlobalSettings(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "services", "api")
	t.Setenv("HOME", home)
	mustWriteFile(t, filepath.Join(home, ".loom", "config"))
	mustWriteFile(t, filepath.Join(home, ".loom", "loom.yaml"))
	mustMkdirAll(t, cwd)

	if _, _, err := ResolvePath(cwd); err == nil || !strings.Contains(err.Error(), "not a Conven workspace") {
		t.Fatalf("ResolvePath error = %v, want outside-workspace error", err)
	}
	if _, err := FindWorkspace(cwd); err == nil || !strings.Contains(err.Error(), "not a Conven workspace") {
		t.Fatalf("FindWorkspace error = %v, want outside-workspace error", err)
	}
}

func TestHomeDirectoryAliasesAreReservedForGlobalSettings(t *testing.T) {
	for _, test := range []struct {
		name      string
		homeAlias bool
	}{
		{name: "cwd is symlink", homeAlias: false},
		{name: "HOME is symlink", homeAlias: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			realHome := filepath.Join(root, "real-home")
			alias := filepath.Join(root, "home-alias")
			mustMkdirAll(t, realHome)
			if err := os.Symlink(realHome, alias); err != nil {
				t.Fatal(err)
			}
			home := realHome
			cwd := alias
			if test.homeAlias {
				home = alias
				cwd = realHome
			}
			t.Setenv("HOME", home)
			mustWriteFile(t, filepath.Join(realHome, ".loom", "config"))
			mustWriteFile(t, filepath.Join(realHome, ".loom", "loom.yaml"))

			if _, _, err := ResolvePath(cwd); err == nil || !strings.Contains(err.Error(), "not a Conven workspace") {
				t.Fatalf("ResolvePath error = %v, want outside-workspace error", err)
			}
			if _, err := FindWorkspace(cwd); err == nil || !strings.Contains(err.Error(), "not a Conven workspace") {
				t.Fatalf("FindWorkspace error = %v, want outside-workspace error", err)
			}
		})
	}
}

func TestFindWorkspaceAllowsLocalConfigBeforeManifestExists(t *testing.T) {
	workspace := t.TempDir()
	cwd := filepath.Join(workspace, "services", "api")
	mustMkdirAll(t, filepath.Join(workspace, ".loom"))
	mustMkdirAll(t, cwd)

	resolved, err := FindWorkspace(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != workspace {
		t.Fatalf("workspace = %q, want %q", resolved, workspace)
	}
}

func mustWriteFile(t *testing.T, path string) {
	t.Helper()
	mustMkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(""), 0600); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatalf("create %q: %v", path, err)
	}
}
