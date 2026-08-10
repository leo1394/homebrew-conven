package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitWorkspaceCreatesManifestAndDoesNotOverwrite(t *testing.T) {
	workspace := t.TempDir()
	template := []byte("version: 1\n")
	path, created, err := InitWorkspace(workspace, template)
	if err != nil {
		t.Fatal(err)
	}
	if !created || path != filepath.Join(workspace, ".conven", "conven.yaml") {
		t.Fatalf("InitWorkspace = (%q, %v)", path, created)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(template) {
		t.Fatalf("manifest = %q", data)
	}
	gitignore := filepath.Join(workspace, ".conven", ".gitignore")
	ignored, err := os.ReadFile(gitignore)
	if err != nil {
		t.Fatal(err)
	}
	if string(ignored) != "/runtime/\n" {
		t.Fatalf("initial gitignore = %q", ignored)
	}
	if err := os.WriteFile(path, []byte("custom\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gitignore, []byte("custom-rule"), 0600); err != nil {
		t.Fatal(err)
	}
	_, created, err = InitWorkspace(workspace, template)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("reinitialization overwrote the manifest")
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "custom\n" {
		t.Fatalf("reinitialized manifest = %q", data)
	}
	ignored, err = os.ReadFile(gitignore)
	if err != nil {
		t.Fatal(err)
	}
	if string(ignored) != "custom-rule\n/runtime/\n" {
		t.Fatalf("merged gitignore = %q", ignored)
	}
	_, created, err = InitWorkspace(workspace, template)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("second reinitialization overwrote the manifest")
	}
	ignored, err = os.ReadFile(gitignore)
	if err != nil {
		t.Fatal(err)
	}
	if string(ignored) != "custom-rule\n/runtime/\n" || strings.Count(string(ignored), "/runtime/\n") != 1 {
		t.Fatalf("repeated initialization changed gitignore: %q", ignored)
	}
}

func TestInitWorkspacePreservesExistingRuntimeIgnoreRule(t *testing.T) {
	workspace := t.TempDir()
	boundary := filepath.Join(workspace, ".conven")
	if err := os.Mkdir(boundary, 0700); err != nil {
		t.Fatal(err)
	}
	gitignore := filepath.Join(boundary, ".gitignore")
	want := "custom-rule\n/runtime/\nother-rule\n"
	if err := os.WriteFile(gitignore, []byte(want), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := InitWorkspace(workspace, []byte("version: 1\n")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(gitignore)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("gitignore = %q, want unchanged %q", data, want)
	}
}

func TestInitWorkspaceRejectsGitignoreSymlink(t *testing.T) {
	workspace := t.TempDir()
	boundary := filepath.Join(workspace, ".conven")
	if err := os.Mkdir(boundary, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "gitignore")
	if err := os.WriteFile(target, []byte("keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(boundary, ".gitignore")); err != nil {
		t.Fatal(err)
	}
	_, _, err := InitWorkspace(workspace, []byte("version: 1\n"))
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("error = %v, want gitignore symlink rejection", err)
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "keep\n" {
		t.Fatalf("gitignore symlink target was changed: %q", data)
	}
}

func TestInitWorkspaceRejectsReadableManifestSymlink(t *testing.T) {
	workspace := t.TempDir()
	boundary := filepath.Join(workspace, ".conven")
	if err := os.Mkdir(boundary, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "conven.yaml")
	if err := os.WriteFile(target, []byte("keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(boundary, "conven.yaml")
	if err := os.Symlink(target, manifest); err != nil {
		t.Fatal(err)
	}

	_, _, err := InitWorkspace(workspace, []byte("replacement\n"))
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("error = %v", err)
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "keep\n" {
		t.Fatalf("manifest symlink target changed: %q", data)
	}
}

func TestInitWorkspaceRejectsDotConvenFile(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, ".conven"), []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err := InitWorkspace(workspace, []byte("version: 1\n"))
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("error = %v, want boundary error", err)
	}
}

func TestInitWorkspaceRejectsUserHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	_, _, err := InitWorkspace(home, []byte("version: 1\n"))
	if err == nil || !strings.Contains(err.Error(), "reserved for global configuration") {
		t.Fatalf("error = %v, want reserved home directory error", err)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".conven")); !os.IsNotExist(statErr) {
		t.Fatalf("home .conven stat error = %v, want directory not created", statErr)
	}
}

func TestInitWorkspaceRejectsUserHomeAliases(t *testing.T) {
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
			if err := os.Mkdir(realHome, 0700); err != nil {
				t.Fatal(err)
			}
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

			_, _, err := InitWorkspace(cwd, []byte("version: 1\n"))
			if err == nil || !strings.Contains(err.Error(), "reserved for global configuration") {
				t.Fatalf("error = %v, want reserved home directory error", err)
			}
			if _, statErr := os.Stat(filepath.Join(realHome, ".conven")); !os.IsNotExist(statErr) {
				t.Fatalf("real home .conven stat error = %v, want directory not created", statErr)
			}
		})
	}
}

func TestPublishNewManifestDoesNotReplaceConcurrentFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conven.yaml")
	if err := os.WriteFile(path, []byte("concurrent\n"), 0600); err != nil {
		t.Fatal(err)
	}
	created, err := publishNewManifest(path, []byte("generated\n"))
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("publish reported replacing an existing manifest")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "concurrent\n" {
		t.Fatalf("manifest = %q", data)
	}
}
