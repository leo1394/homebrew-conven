package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/config"
)

func TestInitLocalCreatesServiceFirstWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{Cwd: workspace, Output: &output, Error: &errorOutput, Version: "test"}
	if code := app.Run([]string{"init", "--local"}); code != 0 {
		t.Fatalf("exit code = %d: %s", code, errorOutput.String())
	}
	manifest, err := config.Load(filepath.Join(workspace, ".conven", "conven.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Environments["local"].Connection.Driver != "none" {
		t.Fatalf("manifest = %#v", manifest)
	}
	for _, name := range []string{"compose.yaml", "local.env", "local.env.example"} {
		if _, err := os.Stat(filepath.Join(workspace, ".conven", name)); !os.IsNotExist(err) {
			t.Fatalf("%s should not be generated: %v", name, err)
		}
	}
	if !strings.Contains(output.String(), "conven services --start <service>") || !strings.Contains(output.String(), "configure their local endpoint addresses") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestInitRejectsRemovedDependencyPresetFlag(t *testing.T) {
	var output bytes.Buffer
	app := App{Cwd: t.TempDir(), Output: &output, Error: &output, Version: "test"}
	if code := app.Run([]string{"init", "--local", "--with", "postgres"}); code != 2 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(output.String(), "flag provided but not defined: --with") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRemovedDependenciesCommandIsNotExposed(t *testing.T) {
	var output bytes.Buffer
	app := App{Output: &output, Error: &output, Version: "test"}
	if code := app.Run([]string{"dependencies", "--list"}); code != 2 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(output.String(), "'dependencies' is not a conven command") {
		t.Fatalf("output = %q", output.String())
	}
}
