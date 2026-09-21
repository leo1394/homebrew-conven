package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFlagParseRepair(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		root := t.TempDir()
		writeAnalyzerFile(t, filepath.Join(root, "go.mod"), "module example.test/api\n")
		source := strings.ReplaceAll("package main\nimport \"flag\"\nfunc main() {\n    dir := flag.String(\"f\", \"config\", \"config\")\n    // load config\n    start(*dir)\n}\n", "\n", newline)
		path := filepath.Join(root, "main.go")
		if err := os.WriteFile(path, []byte(source), 0600); err != nil { t.Fatal(err) }
		var repair *FlagParseRepair
		if err := ValidateGoZeroRuntimeConfigSource("api", root, "."); !errors.As(err, &repair) { t.Fatalf("expected proposed repair: %v", err) }
		assertFileContents(t, path, source)
		if err := repair.Apply(); err != nil { t.Fatal(err) }
		assertFileContents(t, path, strings.Replace(source, "    start(*dir)", "    flag.Parse()"+newline+"    start(*dir)", 1))
		if err := ValidateGoZeroRuntimeConfigSource("api", root, "."); err != nil { t.Fatal(err) }
		if err := repair.Apply(); err == nil { t.Fatal("stale repair accepted") }
	}
}

func TestFlagParseRepairDeclinesUnsafeInsertion(t *testing.T) {
	for _, body := range []string{
		"if enabled { start(*dir) }",
		"start(*dir); flag.Parse()",
		"go start(*dir)",
		"defer start(*dir)",
		"start(*dir)\n flag.String(\"later\", \"\", \"\")",
		"run(func() { start(*dir) })",
	} {
		root := t.TempDir()
		writeAnalyzerFile(t, filepath.Join(root, "go.mod"), "module example.test/api\n")
		writeAnalyzerFile(t, filepath.Join(root, "main.go"), "package main\nimport \"flag\"\nvar dir = flag.String(\"f\", \"\", \"\")\nfunc main() {\n "+body+"\n}\n")
		var repair *FlagParseRepair
		if errors.As(ValidateGoZeroRuntimeConfigSource("api", root, "."), &repair) { t.Fatalf("unsafe repair offered: %s", body) }
	}
}

func TestFlagParseRepairRejectsChangedSourceAndSymlink(t *testing.T) {
	root := t.TempDir()
	writeAnalyzerFile(t, filepath.Join(root, "go.mod"), "module example.test/api\n")
	path := filepath.Join(root, "main.go")
	source := "package main\nimport \"flag\"\nvar dir = flag.String(\"f\", \"\", \"\")\nfunc main() {\n start(*dir)\n}\n"
	writeAnalyzerFile(t, path, source)
	var repair *FlagParseRepair
	if err := ValidateGoZeroRuntimeConfigSource("api", root, "."); !errors.As(err, &repair) { t.Fatal(err) }
	writeAnalyzerFile(t, path, source+"// user edit\n")
	if err := repair.Apply(); err == nil { t.Fatal("overwrote user edit") }
	if err := os.Rename(path, path+".original"); err != nil { t.Fatal(err) }
	if err := os.Symlink(path+".original", path); err != nil { t.Fatal(err) }
	if err := repair.Apply(); err == nil { t.Fatal("followed symlink") }
}
