package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEnvironmentValuesAppliesDocumentedPrecedence(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "local.env")
	if err := os.WriteFile(path, []byte("CONVEN_ENV_PRECEDENCE=file\nQUOTED='value'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONVEN_ENV_PRECEDENCE", "process")
	values, err := LoadEnvironmentValues(workspace, map[string]string{"CONVEN_ENV_PRECEDENCE": "manifest"}, "local.env")
	if err != nil {
		t.Fatal(err)
	}
	if values["CONVEN_ENV_PRECEDENCE"] != "process" || values["QUOTED"] != "value" {
		t.Fatalf("values = %#v", values)
	}
}

func TestLoadEnvironmentValuesRejectsSymbolicLink(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "target.env")
	if err := os.WriteFile(target, []byte("TOKEN=value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(workspace, "local.env")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadEnvironmentValues(workspace, nil, "local.env"); err == nil {
		t.Fatal("symbolic environment file was accepted")
	}
}
