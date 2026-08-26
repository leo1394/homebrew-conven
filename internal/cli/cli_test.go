package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leo1394/homebrew-conven/internal/config"
	convenruntime "github.com/leo1394/homebrew-conven/internal/runtime"
	"github.com/leo1394/homebrew-conven/internal/selector"
)

func TestVersion(t *testing.T) {
	for _, arguments := range [][]string{{"version"}, {"-v"}, {"--version"}} {
		t.Run(arguments[0], func(t *testing.T) {
			var output bytes.Buffer
			var errorOutput bytes.Buffer
			app := App{Output: &output, Error: &errorOutput, Version: "0.2.8", VersionDate: "2026-08-12"}
			if code := app.Run(arguments); code != 0 {
				t.Fatalf("exit code = %d", code)
			}
			const want = `       ccc       /====O
O===O cc   =====O====O
       ccc       \====O
conven version 0.2.8 (2026-08-12)
https://github.com/leo1394/homebrew-conven
`
			if output.String() != want {
				t.Fatalf("output = %q, want %q", output.String(), want)
			}
			if errorOutput.Len() != 0 {
				t.Fatalf("stderr = %q", errorOutput.String())
			}
		})
	}
}

func TestWorkingDirectoryOptionSelectsEffectiveWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	workspace := environmentShortcutWorkspaceAt(t, filepath.Join(root, "workspace with spaces"))
	outside := t.TempDir()
	t.Setenv("CONVEN_WORKSPACE", outside)

	for _, test := range []struct {
		name      string
		arguments []string
		cwd       string
	}{
		{name: "absolute", arguments: []string{"-C", workspace, "services", "--list"}, cwd: outside},
		{name: "workspace child", arguments: []string{"-C", filepath.Join(workspace, "api"), "services", "--list"}, cwd: outside},
		{name: "relative and repeated", arguments: []string{"-C", filepath.Base(workspace), "-C", "api", "-C", "..", "services", "--list"}, cwd: root},
		{name: "empty path leaves directory unchanged", arguments: []string{"-C", "", "services", "--list"}, cwd: workspace},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			var errorOutput bytes.Buffer
			app := App{Output: &output, Error: &errorOutput, Cwd: test.cwd, Version: "test"}
			if code := app.Run(test.arguments); code != 0 {
				t.Fatalf("exit code = %d: %s", code, errorOutput.String())
			}
			if !strings.Contains(output.String(), "api") {
				t.Fatalf("stdout = %q", output.String())
			}
			if errorOutput.Len() != 0 {
				t.Fatalf("stderr = %q", errorOutput.String())
			}
		})
	}
}

func TestWorkingDirectoryOptionControlsInitTarget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	outside := t.TempDir()
	target := filepath.Join(t.TempDir(), "new workspace")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{Output: &output, Error: &errorOutput, Cwd: outside, Version: "test"}
	if code := app.Run([]string{"-C", target, "init"}); code != 0 {
		t.Fatalf("exit code = %d: %s", code, errorOutput.String())
	}
	if _, err := os.Stat(filepath.Join(target, ".conven", "conven.yaml")); err != nil {
		t.Fatalf("target manifest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, ".conven")); !os.IsNotExist(err) {
		t.Fatalf("init changed original cwd: %v", err)
	}
}

func TestWorkingDirectoryOptionAppliesToServiceStart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := environmentShortcutWorkspace(t)
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{Output: &output, Error: &errorOutput, Cwd: t.TempDir(), Version: "test"}
	arguments := []string{"-C", workspace, "services", "--start", "--dry-run", "--test", "api"}
	if code := app.Run(arguments); code != 0 {
		t.Fatalf("exit code = %d: %s", code, errorOutput.String())
	}
	if !strings.Contains(output.String(), "Environment: test\n") || !strings.Contains(output.String(), "Local services: api\n") {
		t.Fatalf("stdout = %q", output.String())
	}
}

func TestWorkingDirectoryOptionResolvesSymlinksLikeChangingDirectory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	workspace := environmentShortcutWorkspaceAt(t, filepath.Join(root, "real-workspace"))
	aliasRoot := filepath.Join(root, "aliases")
	if err := os.Mkdir(aliasRoot, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(aliasRoot, "api")
	if err := os.Symlink(filepath.Join(workspace, "api"), alias); err != nil {
		t.Fatal(err)
	}

	for _, arguments := range [][]string{
		{"-C", alias, "services", "--list"},
		{"-C", alias, "-C", "..", "services", "--list"},
	} {
		var output bytes.Buffer
		var errorOutput bytes.Buffer
		app := App{Output: &output, Error: &errorOutput, Cwd: root, Version: "test"}
		if code := app.Run(arguments); code != 0 {
			t.Fatalf("%v exit code = %d: %s", arguments, code, errorOutput.String())
		}
		if !strings.Contains(output.String(), "api") {
			t.Fatalf("%v stdout = %q", arguments, output.String())
		}
	}
}

func TestWorkingDirectoryOptionValidatesSyntaxAndPath(t *testing.T) {
	directory := t.TempDir()
	file := filepath.Join(t.TempDir(), "workspace-file")
	if err := os.WriteFile(file, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		arguments []string
		code      int
		want      string
	}{
		{name: "missing operand", arguments: []string{"-C"}, code: 2, want: "option -C requires a path"},
		{name: "missing directory", arguments: []string{"-C", filepath.Join(directory, "missing"), "--version"}, code: 1, want: "cannot change to directory"},
		{name: "file", arguments: []string{"-C", file, "--version"}, code: 1, want: "is not a directory"},
		{name: "missing command", arguments: []string{"-C", directory}, code: 2, want: "usage:"},
		{name: "not leading", arguments: []string{"services", "--list", "-C", directory}, code: 2, want: "flag provided but not defined: -C"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			var errorOutput bytes.Buffer
			app := App{Output: &output, Error: &errorOutput, Cwd: directory, Version: "test"}
			if code := app.Run(test.arguments); code != test.code {
				t.Fatalf("exit code = %d, want %d: stdout=%q stderr=%q", code, test.code, output.String(), errorOutput.String())
			}
			if !strings.Contains(errorOutput.String(), test.want) {
				t.Fatalf("stderr = %q, want %q", errorOutput.String(), test.want)
			}
		})
	}
}

func TestLegacyServiceCommandsWereRemoved(t *testing.T) {
	for _, command := range []string{"convening", "discover", "start", "restart", "stop", "logs", "list"} {
		t.Run(command, func(t *testing.T) {
			var output bytes.Buffer
			var errorOutput bytes.Buffer
			app := App{Output: &output, Error: &errorOutput, Version: "test-version"}

			if code := app.Run([]string{command}); code != 2 {
				t.Fatalf("exit code = %d", code)
			}
			if output.Len() != 0 {
				t.Fatalf("stdout = %q", output.String())
			}
			if errorOutput.String() != "conven: '"+command+"' is not a conven command. See 'conven --help'.\n" {
				t.Fatalf("stderr = %q", errorOutput.String())
			}
		})
	}
}

func TestUnknownCommandSuggestsClosestCommand(t *testing.T) {
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{Output: &output, Error: &errorOutput, Version: "test-version"}

	if code := app.Run([]string{"serivces"}); code != 2 {
		t.Fatalf("exit code = %d", code)
	}
	if output.Len() != 0 {
		t.Fatalf("stdout = %q", output.String())
	}
	const want = `conven: 'serivces' is not a conven command. See 'conven --help'.

The most similar command is
	services
`
	if errorOutput.String() != want {
		t.Fatalf("stderr = %q, want %q", errorOutput.String(), want)
	}
}

func TestUnknownCommandEscapesControlCharacters(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "bad\nservices", want: "conven: 'bad\\nservices' is not a conven command. See 'conven --help'.\n"},
		{input: "bad\x1b[31m", want: "conven: 'bad\\x1b[31m' is not a conven command. See 'conven --help'.\n"},
		{input: "it's", want: "conven: 'it\\'s' is not a conven command. See 'conven --help'.\n"},
	} {
		t.Run(test.input, func(t *testing.T) {
			var errorOutput bytes.Buffer
			app := App{Output: io.Discard, Error: &errorOutput, Version: "test-version"}

			if code := app.Run([]string{test.input}); code != 2 {
				t.Fatalf("exit code = %d", code)
			}
			if errorOutput.String() != test.want {
				t.Fatalf("stderr = %q, want %q", errorOutput.String(), test.want)
			}
		})
	}
}

func TestSimilarCommandsRecognizesCommonTypos(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "inti", want: "init"},
		{input: "hlep", want: "help"},
		{input: "confg", want: "config"},
		{input: "polciy", want: "policy"},
		{input: "plguins", want: "plugins"},
		{input: "docotr", want: "doctor"},
		{input: "serivces", want: "services"},
		{input: "verison", want: "version"},
	} {
		t.Run(test.input, func(t *testing.T) {
			got := similarCommands(test.input, []string{"config", "doctor", "help", "init", "plugins", "policy", "services", "version"})
			if strings.Join(got, ",") != test.want {
				t.Fatalf("suggestions = %v, want %q", got, test.want)
			}
		})
	}
}

func TestSimilarCommandsReturnsAllEquallyCloseCandidates(t *testing.T) {
	got := similarCommands("cot", []string{"cat", "cut", "doctor"})
	if strings.Join(got, ",") != "cat,cut" {
		t.Fatalf("suggestions = %v", got)
	}
}

func TestRootHelpIsConciseAndDescriptive(t *testing.T) {
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{Output: &output, Error: &errorOutput, Version: "test-version"}
	if code := app.Run([]string{"help"}); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	const want = `usage:
  conven [-C <path>]... <command> [<args>]
  conven [-C <path>]... help [<command>]
  conven [-C <path>]... [-h | --help | -v | --version]

Global option:
   -C <path>  Run as if conven was started in <path> instead of the current working directory

These are common Conven commands:

set up and configure a workspace
   init       Initialize a Conven workspace
   config     View or change Conven settings
   catalog    Edit or validate the workspace service catalog
   policy     Edit, import, or reset the workspace manifest
   plugins    Install, list, remove, or run plugins

run and inspect local services
   status     Show the complete workspace and runtime status
   services   List, start, restart, stop, and inspect services
   doctor     Validate workspace and connection configuration

Run 'conven help <command>' or 'conven <command> --help' for detailed help.
`
	if output.String() != want {
		t.Fatalf("root help = %q, want %q", output.String(), want)
	}
	if errorOutput.Len() != 0 {
		t.Fatalf("stderr = %q", errorOutput.String())
	}
}

func TestHelpCommandShowsDetailedCommandHelp(t *testing.T) {
	for _, command := range []string{"init", "config", "catalog", "policy", "plugins", "status", "services", "doctor"} {
		t.Run(command, func(t *testing.T) {
			var directOutput bytes.Buffer
			var directError bytes.Buffer
			directApp := App{Output: &directOutput, Error: &directError, Version: "test-version"}
			if code := directApp.Run([]string{command, "--help"}); code != 0 {
				t.Fatalf("direct help exit code = %d", code)
			}

			var helpOutput bytes.Buffer
			var helpError bytes.Buffer
			helpApp := App{Output: &helpOutput, Error: &helpError, Version: "test-version"}
			if code := helpApp.Run([]string{"help", command}); code != 0 {
				t.Fatalf("help command exit code = %d", code)
			}
			if helpOutput.String() != directOutput.String() {
				t.Fatalf("help output = %q, direct output = %q", helpOutput.String(), directOutput.String())
			}
			if directError.Len() != 0 || helpError.Len() != 0 {
				t.Fatalf("direct stderr = %q, help stderr = %q", directError.String(), helpError.String())
			}
		})
	}
}

func TestCatalogValidateAndEdit(t *testing.T) {
	workspace := environmentShortcutWorkspace(t)
	catalogPath := filepath.Join(workspace, ".conven", "catalog.yaml")
	catalog := `version: 1
services:
  - repository: api
    kind: http
    port: 18080
  - rpcBinding: catalogRpc
    kind: rpc
    port: 18081
disabledRpcBindings:
  - legacyRpc
`
	if err := os.WriteFile(catalogPath, []byte(catalog), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{Output: &output, Error: &errorOutput, Cwd: workspace, CatalogEditor: func(_ context.Context, path string) error {
		if path == catalogPath || !strings.Contains(filepath.Base(path), "catalog-edit-") {
			t.Fatalf("catalog editor path = %q", path)
		}
		return os.WriteFile(path, []byte(strings.ReplaceAll(catalog, "legacyRpc", "disabledRpc")), 0600)
	}}
	if code := app.Run([]string{"catalog", "--validate"}); code != 0 {
		t.Fatalf("validate exit code = %d: stdout=%s stderr=%s", code, output.String(), errorOutput.String())
	}
	for _, expected := range []string{"Conven catalog is valid", "Services: 2", "Disabled bindings: 1", catalogPath} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("validate output is missing %q: %q", expected, output.String())
		}
	}
	output.Reset()
	errorOutput.Reset()
	if code := app.Run([]string{"catalog", "--edit"}); code != 0 {
		t.Fatalf("edit exit code = %d: stdout=%s stderr=%s", code, output.String(), errorOutput.String())
	}
	if !strings.Contains(output.String(), "Updated Conven catalog") {
		t.Fatalf("edit output = %q", output.String())
	}
	data, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "disabledRpc") || strings.Contains(string(data), "legacyRpc") {
		t.Fatalf("edited catalog = %q", data)
	}
}

func TestWorkspaceStatusCombinesWorkspaceCatalogAndRuntimeState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := environmentShortcutWorkspace(t)
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	manifest := `version: 2
workspace:
  name: status-workspace
environments:
  local:
    connection:
      driver: none
    endpoints:
      postgres:
        protocol: tcp
        address: 127.0.0.1:5432
services:
  api:
    path: api
    kind: http
    ports:
      metrics: 19090
      http: 18080
    runner:
      run: [sleep, "600"]
`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	catalogPath := filepath.Join(workspace, ".conven", "catalog.yaml")
	if err := os.WriteFile(catalogPath, []byte("version: 1\nservices: []\ndisabledRpcBindings: [zRpc, aRpc]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := config.SetSetting(workspace, "", false, "ktctl.kubeconfig", "/secure/dev-kubeconfig"); err != nil {
		t.Fatal(err)
	}
	if err := config.SetSetting(workspace, "", false, "ktctl.path", "/usr/local/bin/ktctl"); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	app := App{Output: &output, Error: &output, Cwd: workspace}
	if code := app.Run([]string{"status"}); code != 0 {
		t.Fatalf("status exit code = %d: %s", code, output.String())
	}
	for _, expected := range []string{
		"Workspace",
		"Name: status-workspace",
		"Root: " + workspace,
		"Manifest: " + manifestPath,
		"Catalog: " + catalogPath,
		"ktctl.kubeconfig=/secure/dev-kubeconfig",
		"ktctl.path=/usr/local/bin/ktctl",
		"Available services",
		"api: type=http, ports=http=18080,metrics=19090, path=api",
		"Configured endpoints",
		"local.postgres: protocol=tcp, address=127.0.0.1:5432, readiness=tcp",
		"Disabled bindings",
		"aRpc",
		"zRpc",
		"Conven status",
		"No Conven session found.",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("status output is missing %q: %q", expected, output.String())
		}
	}
	if strings.Index(output.String(), "aRpc") > strings.Index(output.String(), "zRpc") {
		t.Fatalf("disabled bindings are not sorted: %q", output.String())
	}
	if strings.Index(output.String(), "Available services") > strings.Index(output.String(), "Configured endpoints") || strings.Index(output.String(), "Configured endpoints") > strings.Index(output.String(), "Disabled bindings") {
		t.Fatalf("workspace inventories are not ordered: %q", output.String())
	}
	if strings.Index(output.String(), "Catalog: ") > strings.Index(output.String(), "ktctl.kubeconfig=") || strings.Index(output.String(), "ktctl.path=") > strings.Index(output.String(), "Available services") {
		t.Fatalf("ktctl settings are not in the Workspace group: %q", output.String())
	}
}

func TestHelpCommandHandlesHelpAndVersionTopics(t *testing.T) {
	for _, test := range []struct {
		arguments []string
		want      string
	}{
		{arguments: []string{"help", "help"}, want: "usage:\n  conven help [<command>]\n"},
		{arguments: []string{"help", "--help"}, want: "usage:\n  conven help [<command>]\n"},
		{arguments: []string{"help", "version"}, want: "usage:\n  conven version\n  conven -v\n  conven --version\n"},
	} {
		var output bytes.Buffer
		var errorOutput bytes.Buffer
		app := App{Output: &output, Error: &errorOutput, Version: "test-version"}

		if code := app.Run(test.arguments); code != 0 {
			t.Fatalf("%v exit code = %d", test.arguments, code)
		}
		if !strings.HasPrefix(output.String(), test.want) {
			t.Fatalf("%v stdout = %q", test.arguments, output.String())
		}
		if errorOutput.Len() != 0 {
			t.Fatalf("%v stderr = %q", test.arguments, errorOutput.String())
		}
	}
}

func TestHelpCommandSuggestsUnknownTopic(t *testing.T) {
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{Output: &output, Error: &errorOutput, Version: "test-version"}

	if code := app.Run([]string{"help", "serivces"}); code != 2 {
		t.Fatalf("exit code = %d", code)
	}
	if output.Len() != 0 {
		t.Fatalf("stdout = %q", output.String())
	}
	if !strings.Contains(errorOutput.String(), "The most similar command is\n\tservices\n") {
		t.Fatalf("stderr = %q", errorOutput.String())
	}
}

func TestHelpCommandRejectsMultipleTopics(t *testing.T) {
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{Output: &output, Error: &errorOutput, Version: "test-version"}

	if code := app.Run([]string{"help", "services", "--start"}); code != 2 {
		t.Fatalf("exit code = %d", code)
	}
	if output.Len() != 0 {
		t.Fatalf("stdout = %q", output.String())
	}
	if !strings.Contains(errorOutput.String(), "conven: help accepts at most one command") ||
		!strings.Contains(errorOutput.String(), "conven help [<command>]") {
		t.Fatalf("stderr = %q", errorOutput.String())
	}
}

func TestServicesHelpUsesStdout(t *testing.T) {
	for _, arguments := range [][]string{
		{"services", "--help"},
		{"services", "-h"},
		{"services", "help"},
	} {
		var output bytes.Buffer
		var errorOutput bytes.Buffer
		app := App{Output: &output, Error: &errorOutput, Cwd: t.TempDir(), Version: "test-version"}
		if code := app.Run(arguments); code != 0 {
			t.Fatalf("%v exit code = %d", arguments, code)
		}
		for _, action := range []string{"--list", "--registry", "--start", "--restart", "--status", "--stop", "--stop-all", "--logs", "--dashboard", "--cleanup"} {
			if !strings.Contains(output.String(), action) {
				t.Fatalf("%v help is missing %s: %q", arguments, action, output.String())
			}
		}
		if !strings.Contains(output.String(), "--start opens an interactive selector") ||
			!strings.Contains(output.String(), "--restart restarts only changed services") {
			t.Fatalf("%v help is missing service selection behavior: %q", arguments, output.String())
		}
		if !strings.Contains(output.String(), "Manage the local service session") ||
			!strings.Contains(output.String(), "--registry   Update services from direct-child repositories") {
			t.Fatalf("%v help is missing action descriptions: %q", arguments, output.String())
		}
		if errorOutput.Len() != 0 {
			t.Fatalf("%v stderr = %q", arguments, errorOutput.String())
		}
	}
}

func TestServiceActionHelpUsesStdout(t *testing.T) {
	for _, action := range []string{"--list", "--registry", "--start", "--restart", "--status", "--stop", "--stop-all", "--logs", "--dashboard", "--cleanup"} {
		t.Run(action, func(t *testing.T) {
			var output bytes.Buffer
			var errorOutput bytes.Buffer
			app := App{Output: &output, Error: &errorOutput, Cwd: t.TempDir(), Version: "test-version"}

			if code := app.Run([]string{"services", action, "--help"}); code != 0 {
				t.Fatalf("exit code = %d", code)
			}
			if !strings.Contains(output.String(), "conven services "+action) {
				t.Fatalf("stdout = %q", output.String())
			}
			if errorOutput.Len() != 0 {
				t.Fatalf("stderr = %q", errorOutput.String())
			}
		})
	}
}

func TestServicesRequiresKnownActionFirst(t *testing.T) {
	for _, test := range []struct {
		name      string
		arguments []string
	}{
		{name: "missing", arguments: []string{"services"}},
		{name: "unknown", arguments: []string{"services", "--unknown"}},
		{name: "action after service", arguments: []string{"services", "api", "--start"}},
		{name: "second action", arguments: []string{"services", "--start", "--status"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			var errorOutput bytes.Buffer
			app := App{Output: &output, Error: &errorOutput, Cwd: t.TempDir(), Version: "test-version"}
			if code := app.Run(test.arguments); code != 2 {
				t.Fatalf("exit code = %d", code)
			}
			if output.Len() != 0 {
				t.Fatalf("stdout = %q", output.String())
			}
			if errorOutput.Len() == 0 {
				t.Fatal("stderr is empty")
			}
			if strings.Contains(errorOutput.String(), "not a Conven workspace") {
				t.Fatalf("workspace lookup happened before action validation: %q", errorOutput.String())
			}
		})
	}
}

func TestSubcommandHelpUsesStdout(t *testing.T) {
	for _, command := range []string{"init", "config", "doctor"} {
		t.Run(command, func(t *testing.T) {
			var output bytes.Buffer
			var errorOutput bytes.Buffer
			app := App{Output: &output, Error: &errorOutput, Version: "test-version"}

			if code := app.Run([]string{command, "--help"}); code != 0 {
				t.Fatalf("exit code = %d", code)
			}
			if !strings.Contains(output.String(), "Usage of "+command+":") {
				t.Fatalf("stdout = %q", output.String())
			}
			if errorOutput.Len() != 0 {
				t.Fatalf("stderr = %q", errorOutput.String())
			}
		})
	}
}

func TestPolicyHelpUsesStdout(t *testing.T) {
	for _, arguments := range [][]string{
		{"policy", "--help"},
		{"policy", "-h"},
		{"policy", "help"},
		{"policy", "--edit", "--help"},
		{"policy", "--import", "policy.yaml", "--help"},
		{"policy", "--import", "policy.yaml", "--edit", "--help"},
		{"policy", "--reset", "--help"},
	} {
		var output bytes.Buffer
		var errorOutput bytes.Buffer
		app := App{Output: &output, Error: &errorOutput, Version: "test-version"}
		if code := app.Run(arguments); code != 0 {
			t.Fatalf("%v exit code = %d", arguments, code)
		}
		if !strings.Contains(output.String(), "conven policy") {
			t.Fatalf("%v stdout = %q", arguments, output.String())
		}
		if errorOutput.Len() != 0 {
			t.Fatalf("%v stderr = %q", arguments, errorOutput.String())
		}
	}
}

func TestPluginsHelpUsesStdout(t *testing.T) {
	for _, arguments := range [][]string{
		{"plugins", "--help"},
		{"plugins", "-h"},
		{"plugins", "help"},
	} {
		var output bytes.Buffer
		var errorOutput bytes.Buffer
		app := App{Output: &output, Error: &errorOutput, Version: "test-version"}
		if code := app.Run(arguments); code != 0 {
			t.Fatalf("%v exit code = %d", arguments, code)
		}
		for _, action := range []string{"--install [--global] PYTHON_FILE", "--list [--global]", "--remove [--global] NAME", "--run [NAME]", "--global --run NAME", "--output [FILE]", "--disable-bindings BINDING"} {
			if !strings.Contains(output.String(), action) {
				t.Fatalf("%v help is missing %s: %q", arguments, action, output.String())
			}
		}
		if errorOutput.Len() != 0 {
			t.Fatalf("%v stderr = %q", arguments, errorOutput.String())
		}
	}
}

func TestPolicyRequiresKnownActionBeforeWorkspaceLookup(t *testing.T) {
	for _, arguments := range [][]string{
		{"policy"},
		{"policy", "--unknown"},
		{"policy", "--use-template"},
		{"policy", "manifest", "--edit"},
	} {
		var output bytes.Buffer
		app := App{Output: &output, Error: &output, Cwd: t.TempDir(), Version: "test-version"}
		if code := app.Run(arguments); code != 2 {
			t.Fatalf("%v exit code = %d: %s", arguments, code, output.String())
		}
		if !strings.Contains(output.String(), "conven policy --edit") || strings.Contains(output.String(), "not a Conven workspace") {
			t.Fatalf("%v output = %q", arguments, output.String())
		}
	}
}

func TestPluginsRequireKnownExclusiveAction(t *testing.T) {
	for _, test := range []struct {
		name      string
		arguments []string
		code      int
		want      string
	}{
		{name: "missing", arguments: []string{"plugins"}, code: 2, want: "conven plugins --install"},
		{name: "unknown", arguments: []string{"plugins", "--unknown"}, code: 2, want: "unknown plugins action"},
		{name: "install without source", arguments: []string{"plugins", "--install"}, code: 1, want: "requires exactly one Python file"},
		{name: "install with extra argument", arguments: []string{"plugins", "--install", "plugin.py", "--list"}, code: 1, want: "requires exactly one Python file"},
		{name: "list with second action", arguments: []string{"plugins", "--list", "--install"}, code: 1, want: "does not accept arguments or another action"},
		{name: "remove without name", arguments: []string{"plugins", "--remove"}, code: 1, want: "requires exactly one plugin name"},
		{name: "remove with extra argument", arguments: []string{"plugins", "--remove", "inspect", "--list"}, code: 1, want: "requires exactly one plugin name"},
		{name: "prefix global without run", arguments: []string{"plugins", "--global"}, code: 2, want: "--global must be followed by --run NAME"},
		{name: "prefix global wrong action", arguments: []string{"plugins", "--global", "--list"}, code: 2, want: "--global must be followed by --run NAME"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			var errorOutput bytes.Buffer
			app := App{Output: &output, Error: &errorOutput, Cwd: t.TempDir(), Version: "test-version"}
			if code := app.Run(test.arguments); code != test.code {
				t.Fatalf("exit code = %d, want %d", code, test.code)
			}
			if output.Len() != 0 {
				t.Fatalf("stdout = %q", output.String())
			}
			if !strings.Contains(errorOutput.String(), test.want) {
				t.Fatalf("stderr = %q, want %q", errorOutput.String(), test.want)
			}
			if test.code == 2 && !strings.Contains(errorOutput.String(), "conven plugins --run [NAME]") {
				t.Fatalf("stderr is missing plugin usage: %q", errorOutput.String())
			}
		})
	}
}

func TestPluginsInstallAndListUseTemporaryHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workingDirectory := t.TempDir()
	if err := os.Mkdir(filepath.Join(workingDirectory, ".conven"), 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(workingDirectory, "inspect.py")
	if err := os.WriteFile(source, []byte("#!/usr/bin/env python3\nprint('installed')\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{Output: &output, Error: &errorOutput, Cwd: workingDirectory, Version: "test-version"}
	if code := app.Run([]string{"plugins", "--install", "./inspect.py"}); code != 0 {
		t.Fatalf("install exit code = %d: %s", code, errorOutput.String())
	}
	canonicalWorkspace, err := filepath.EvalSymlinks(workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(canonicalWorkspace, ".conven", "plugins")
	destination := filepath.Join(directory, "inspect.py")
	wantOutput := "==> Installed workspace plugin inspect\n  - Path: plugins/inspect.py\n  - Workspace: " + canonicalWorkspace + "\n"
	if output.String() != wantOutput {
		t.Fatalf("install output = %q, want %q", output.String(), wantOutput)
	}
	installed, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read installed plugin: %v", err)
	}
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if string(installed) != string(original) {
		t.Fatalf("installed plugin = %q, want %q", installed, original)
	}
	output.Reset()
	if code := app.Run([]string{"plugins", "--list"}); code != 0 {
		t.Fatalf("list exit code = %d: %s", code, errorOutput.String())
	}
	wantList := "==> Workspace plugins\n  - inspect\n" +
		"==> Global plugins\n  - (none)\n"
	if output.String() != wantList {
		t.Fatalf("list output = %q", output.String())
	}
	output.Reset()
	if code := app.Run([]string{"plugins", "--remove", "inspect"}); code != 0 {
		t.Fatalf("remove exit code = %d: %s", code, errorOutput.String())
	}
	wantOutput = "==> Removed workspace plugin inspect\n  - Path: plugins/inspect.py\n  - Workspace: " + canonicalWorkspace + "\n"
	if output.String() != wantOutput {
		t.Fatalf("remove output = %q, want %q", output.String(), wantOutput)
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("removed plugin still exists: %v", err)
	}
	if errorOutput.Len() != 0 {
		t.Fatalf("stderr = %q", errorOutput.String())
	}
}

func TestAskPluginOverwriteAcceptsOnlyExplicitYes(t *testing.T) {
	for _, test := range []struct {
		answer    string
		overwrite bool
	}{
		{answer: "y\n", overwrite: true},
		{answer: "YES\n", overwrite: true},
		{answer: "\n"},
		{answer: "n\n"},
		{answer: "cancel\n"},
	} {
		t.Run(strings.TrimSpace(test.answer), func(t *testing.T) {
			var output bytes.Buffer
			overwrite, err := askPluginOverwrite(strings.NewReader(test.answer), &output, "inspect")
			if err != nil {
				t.Fatal(err)
			}
			if overwrite != test.overwrite {
				t.Fatalf("overwrite = %t, want %t", overwrite, test.overwrite)
			}
			if output.String() != "  => Overwrite plugin inspect? [y/N]: " {
				t.Fatalf("prompt = %q", output.String())
			}
		})
	}
}

func TestAskPluginOverwriteContextStopsWhenCancelledWithoutInput(t *testing.T) {
	input, inputWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer inputWriter.Close()
	promptReader, promptWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer promptReader.Close()
	defer promptWriter.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		overwrite bool
		err       error
	}
	done := make(chan result, 1)
	go func() {
		overwrite, err := askPluginOverwriteContext(ctx, input, promptWriter, "inspect")
		done <- result{overwrite: overwrite, err: err}
	}()

	prompt, err := bufio.NewReader(promptReader).ReadString(':')
	if err != nil {
		t.Fatal(err)
	}
	if prompt != "  => Overwrite plugin inspect? [y/N]:" {
		t.Fatalf("prompt = %q", prompt)
	}
	cancel()

	select {
	case got := <-done:
		if got.overwrite {
			t.Fatal("cancelled confirmation allowed overwrite")
		}
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled confirmation remained blocked waiting for input")
	}
}

func TestPluginsDuplicateInstallRequiresTerminalAndPreservesExistingPlugin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	firstDirectory := t.TempDir()
	secondDirectory := t.TempDir()
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, ".conven"), 0700); err != nil {
		t.Fatal(err)
	}
	firstSource := filepath.Join(firstDirectory, "inspect.py")
	secondSource := filepath.Join(secondDirectory, "inspect.py")
	const original = "#!/usr/bin/env python3\nprint('original')\n"
	if err := os.WriteFile(firstSource, []byte(original), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondSource, []byte("#!/usr/bin/env python3\nprint('replacement')\n"), 0700); err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(t.TempDir(), "confirmation")
	if err := os.WriteFile(inputPath, []byte("yes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{Input: input, Output: &output, Error: &errorOutput, Cwd: workspace, Version: "test-version"}
	if code := app.Run([]string{"plugins", "--install", firstSource}); code != 0 {
		t.Fatalf("first install exit code = %d: %s", code, errorOutput.String())
	}
	output.Reset()
	if code := app.Run([]string{"plugins", "--install", secondSource}); code != 1 {
		t.Fatalf("duplicate install exit code = %d, want 1", code)
	}
	for _, expected := range []string{
		"Warning: Workspace plugin already exists.",
		"  - Plugin: inspect",
		"  - Path: plugins/inspect.py",
		"  - Existing plugin was not changed.",
		"  => conven plugins --remove inspect",
		"conven: workspace plugin \"inspect\": overwrite confirmation requires an interactive terminal",
	} {
		if !strings.Contains(errorOutput.String(), expected) {
			t.Fatalf("duplicate install stderr is missing %q: %q", expected, errorOutput.String())
		}
	}
	position, err := input.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatal(err)
	}
	if position != 0 {
		t.Fatalf("non-terminal confirmation input was consumed: offset=%d", position)
	}
	destination := filepath.Join(workspace, ".conven", "plugins", "inspect.py")
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("existing plugin changed after non-terminal install: %q", data)
	}
}

func TestPluginsDuplicateGlobalInstallSuggestsGlobalRemoval(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	firstDirectory := t.TempDir()
	secondDirectory := t.TempDir()
	firstSource := filepath.Join(firstDirectory, "inspect.py")
	secondSource := filepath.Join(secondDirectory, "inspect.py")
	if err := os.WriteFile(firstSource, []byte("#!/usr/bin/env python3\nprint('original')\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondSource, []byte("#!/usr/bin/env python3\nprint('replacement')\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{Output: &output, Error: &errorOutput, Cwd: t.TempDir(), Version: "test-version"}
	if code := app.Run([]string{"plugins", "--install", "--global", firstSource}); code != 0 {
		t.Fatalf("first install exit code = %d: %s", code, errorOutput.String())
	}
	output.Reset()
	errorOutput.Reset()
	if code := app.Run([]string{"plugins", "--install", "--global", secondSource}); code != 1 {
		t.Fatalf("duplicate install exit code = %d, want 1", code)
	}
	if !strings.Contains(errorOutput.String(), "  => conven plugins --remove --global inspect") {
		t.Fatalf("duplicate global install stderr = %q", errorOutput.String())
	}
}

func TestPluginsRunForwardsWorkspaceArgumentsAndIO(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	plugin := `#!/bin/sh
printf 'cwd=%s\n' "$PWD"
printf 'workspace=%s\n' "$CONVEN_WORKSPACE"
for argument in "$@"; do
  printf 'arg=%s\n' "$argument"
done
IFS= read -r input
printf 'input=%s\n' "$input"
printf 'plugin stderr\n' >&2
`
	workspace := filepath.Join(t.TempDir(), "workspace")
	workingDirectory := filepath.Join(workspace, "service", "nested")
	pluginDirectory := filepath.Join(workspace, ".conven", "plugins")
	if err := os.MkdirAll(pluginDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDirectory, "inspect.py"), []byte(plugin), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workingDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	canonicalWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(t.TempDir(), "plugin-input")
	if err := os.WriteFile(inputPath, []byte("payload\n"), 0600); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{Input: input, Output: &output, Error: &errorOutput, Context: context.Background(), Cwd: workingDirectory, Version: "test-version"}
	if code := app.Run([]string{"plugins", "--run", "inspect", "--check", "--output", "candidate.yaml", "--global", "--list"}); code != 0 {
		t.Fatalf("run exit code = %d: %s", code, errorOutput.String())
	}
	want := strings.Join([]string{
		"cwd=" + canonicalWorkspace,
		"workspace=" + canonicalWorkspace,
		"arg=--workspace",
		"arg=" + canonicalWorkspace,
		"arg=--check",
		"arg=--output",
		"arg=candidate.yaml",
		"arg=--global",
		"arg=--list",
		"input=payload",
		"",
	}, "\n")
	if output.String() != want {
		t.Fatalf("stdout = %q, want %q", output.String(), want)
	}
	if errorOutput.String() != "plugin stderr\n" {
		t.Fatalf("stderr = %q", errorOutput.String())
	}
}

func TestPluginsGlobalInstallAndListWorkOutsideWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workingDirectory := t.TempDir()
	source := filepath.Join(workingDirectory, "inspect.py")
	if err := os.WriteFile(source, []byte("#!/usr/bin/env python3\nprint('global')\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{Output: &output, Error: &errorOutput, Cwd: workingDirectory, Version: "test-version"}
	if code := app.Run([]string{"plugins", "--install", "--global", "./inspect.py"}); code != 0 {
		t.Fatalf("install exit code = %d: %s", code, errorOutput.String())
	}
	directory := filepath.Join(home, ".conven", "plugins")
	destination := filepath.Join(directory, "inspect.py")
	wantInstall := "==> Installed global plugin inspect\n  - Path: " + destination + "\n"
	if output.String() != wantInstall {
		t.Fatalf("install output = %q, want %q", output.String(), wantInstall)
	}
	output.Reset()
	if code := app.Run([]string{"plugins", "--list", "--global"}); code != 0 {
		t.Fatalf("list exit code = %d: %s", code, errorOutput.String())
	}
	if output.String() != "==> Global plugins\n  - inspect\n" {
		t.Fatalf("global list output = %q", output.String())
	}
	output.Reset()
	if code := app.Run([]string{"plugins", "--remove", "--global", "inspect"}); code != 0 {
		t.Fatalf("global remove exit code = %d: %s", code, errorOutput.String())
	}
	if output.String() != "==> Removed global plugin inspect\n  - Path: "+destination+"\n" {
		t.Fatalf("global remove output = %q", output.String())
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("removed global plugin still exists: %v", err)
	}
}

func TestPluginsRunWithoutNameUsesOnlyWorkspaceUniquePlugin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := t.TempDir()
	pluginDirectory := filepath.Join(workspace, ".conven", "plugins")
	if err := os.MkdirAll(pluginDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	plugin := `#!/bin/sh
for argument in "$@"; do
  printf 'arg=%s\n' "$argument"
done
`
	if err := os.WriteFile(filepath.Join(pluginDirectory, "generator.py"), []byte(plugin), 0700); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{Output: &output, Error: &errorOutput, Context: context.Background(), Cwd: workspace, Version: "test-version"}
	if code := app.Run([]string{"plugins", "--run", "--output", "--disable-bindings", "alphaRpc", "betaRpc"}); code != 0 {
		t.Fatalf("run exit code = %d: %s", code, errorOutput.String())
	}
	canonical, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"arg=--workspace",
		"arg=" + canonical,
		"arg=--output",
		"arg=--disable-bindings",
		"arg=alphaRpc",
		"arg=betaRpc",
		"",
	}, "\n")
	if output.String() != want {
		t.Fatalf("stdout = %q, want %q", output.String(), want)
	}
	for _, expected := range []string{
		"Warning: Plugin name omitted; selected the only workspace plugin.",
		"  - Plugin: generator",
	} {
		if !strings.Contains(errorOutput.String(), expected) {
			t.Fatalf("stderr is missing %q: %q", expected, errorOutput.String())
		}
	}
}

func TestPluginsRunWithoutNameSelectsFromMultipleWorkspacePlugins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := t.TempDir()
	pluginDirectory := filepath.Join(workspace, ".conven", "plugins")
	if err := os.MkdirAll(pluginDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alpha", "beta"} {
		content := "#!/bin/sh\necho " + name + "\nprintf 'arg=%s\\n' \"$@\"\n"
		if err := os.WriteFile(filepath.Join(pluginDirectory, name+".py"), []byte(content), 0700); err != nil {
			t.Fatal(err)
		}
	}
	globalDirectory := filepath.Join(home, ".conven", "plugins")
	if err := os.MkdirAll(globalDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalDirectory, "global.py"), []byte("#!/bin/sh\necho should-not-run\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	selectorCalled := false
	app := App{
		Output:  &output,
		Error:   &errorOutput,
		Context: context.Background(),
		Cwd:     workspace,
		Version: "test-version",
		SingleSelector: func(_ context.Context, _ *os.File, _ io.Writer, prompt selector.Prompt, candidates []selector.Candidate) (selector.Candidate, bool, error) {
			selectorCalled = true
			if prompt.Title != "Select a workspace plugin" || prompt.ConfirmationLabel != "Running plugin" || prompt.EmptySelectionNotice != "Select one plugin before confirming." {
				t.Fatalf("prompt = %#v", prompt)
			}
			if len(candidates) != 2 || candidates[0].Name != "alpha" || candidates[1].Name != "beta" || candidates[0].Tag != "" || candidates[1].Tag != "" {
				t.Fatalf("candidates = %#v", candidates)
			}
			return candidates[1], true, nil
		},
	}
	if code := app.Run([]string{"plugins", "--run", "--output", "candidate.yaml"}); code != 0 {
		t.Fatalf("exit code = %d: stdout=%q stderr=%q", code, output.String(), errorOutput.String())
	}
	if !selectorCalled {
		t.Fatal("workspace plugin selector was not called")
	}
	if !strings.Contains(output.String(), "beta\n") || !strings.Contains(output.String(), "arg=--output\narg=candidate.yaml\n") {
		t.Fatalf("stdout = %q", output.String())
	}
	if errorOutput.Len() != 0 {
		t.Fatalf("stderr = %q", errorOutput.String())
	}
}

func TestPluginsRunWithoutNameSelectsGlobalPluginWhenWorkspaceHasNone(t *testing.T) {
	for _, count := range []int{1, 2} {
		t.Run(fmt.Sprintf("global-%d", count), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			workspace := t.TempDir()
			if err := os.MkdirAll(filepath.Join(workspace, ".conven", "plugins"), 0700); err != nil {
				t.Fatal(err)
			}
			globalDirectory := filepath.Join(home, ".conven", "plugins")
			if err := os.MkdirAll(globalDirectory, 0700); err != nil {
				t.Fatal(err)
			}
			for index := 0; index < count; index++ {
				name := fmt.Sprintf("global-%d", index)
				content := "#!/bin/sh\necho " + name + "\n"
				if err := os.WriteFile(filepath.Join(globalDirectory, name+".py"), []byte(content), 0700); err != nil {
					t.Fatal(err)
				}
			}
			var output bytes.Buffer
			var errorOutput bytes.Buffer
			selectorCalled := false
			app := App{
				Output:  &output,
				Error:   &errorOutput,
				Context: context.Background(),
				Cwd:     workspace,
				Version: "test-version",
				SingleSelector: func(_ context.Context, _ *os.File, _ io.Writer, prompt selector.Prompt, candidates []selector.Candidate) (selector.Candidate, bool, error) {
					selectorCalled = true
					if prompt.Title != "Select a global plugin" || len(candidates) != count {
						t.Fatalf("prompt=%#v candidates=%#v", prompt, candidates)
					}
					for _, candidate := range candidates {
						if candidate.Tag != "global" {
							t.Fatalf("global candidate = %#v", candidate)
						}
					}
					return candidates[len(candidates)-1], true, nil
				},
			}
			if code := app.Run([]string{"plugins", "--run", "--output"}); code != 0 {
				t.Fatalf("exit code = %d: stdout=%q stderr=%q", code, output.String(), errorOutput.String())
			}
			if !selectorCalled {
				t.Fatal("global plugin selector was not called")
			}
			if !strings.Contains(output.String(), fmt.Sprintf("global-%d\n", count-1)) {
				t.Fatalf("stdout = %q", output.String())
			}
			if !strings.Contains(errorOutput.String(), "Warning: Running a global plugin.") || !strings.Contains(errorOutput.String(), fmt.Sprintf("  - Plugin: global-%d", count-1)) {
				t.Fatalf("stderr = %q", errorOutput.String())
			}
		})
	}
}

func TestPluginsRunWithoutNameCanCancelSelection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	pluginDirectory := filepath.Join(workspace, ".conven", "plugins")
	if err := os.MkdirAll(pluginDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alpha", "beta"} {
		if err := os.WriteFile(filepath.Join(pluginDirectory, name+".py"), []byte("#!/bin/sh\necho should-not-run\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{
		Output:  &output,
		Error:   &errorOutput,
		Context: context.Background(),
		Cwd:     workspace,
		Version: "test-version",
		SingleSelector: func(_ context.Context, _ *os.File, _ io.Writer, _ selector.Prompt, _ []selector.Candidate) (selector.Candidate, bool, error) {
			return selector.Candidate{}, false, nil
		},
	}
	if code := app.Run([]string{"plugins", "--run"}); code != 0 {
		t.Fatalf("exit code = %d: stderr=%q", code, errorOutput.String())
	}
	if output.String() != "==> Plugin run cancelled\n  - No plugin was run.\n" || errorOutput.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", output.String(), errorOutput.String())
	}
}

func TestPluginsRunWithoutNameReportsNonInteractiveSelection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	pluginDirectory := filepath.Join(workspace, ".conven", "plugins")
	if err := os.MkdirAll(pluginDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alpha", "beta"} {
		if err := os.WriteFile(filepath.Join(pluginDirectory, name+".py"), []byte("#!/bin/sh\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{
		Output:  &output,
		Error:   &errorOutput,
		Context: context.Background(),
		Cwd:     workspace,
		Version: "test-version",
		SingleSelector: func(context.Context, *os.File, io.Writer, selector.Prompt, []selector.Candidate) (selector.Candidate, bool, error) {
			return selector.Candidate{}, false, selector.ErrNotTerminal
		},
	}
	if code := app.Run([]string{"plugins", "--run"}); code != 1 {
		t.Fatalf("exit code = %d: stdout=%q stderr=%q", code, output.String(), errorOutput.String())
	}
	if output.Len() != 0 || !strings.Contains(errorOutput.String(), "plugin selection requires an interactive terminal; specify a plugin name explicitly") {
		t.Fatalf("stdout=%q stderr=%q", output.String(), errorOutput.String())
	}
}

func TestPluginsRunWithoutNameRequiresAnInstalledPlugin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".conven", "plugins"), 0700); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{Output: &output, Error: &errorOutput, Cwd: workspace, Version: "test-version"}
	if code := app.Run([]string{"plugins", "--run"}); code != 1 {
		t.Fatalf("exit code = %d: stdout=%q stderr=%q", code, output.String(), errorOutput.String())
	}
	if output.Len() != 0 || !strings.Contains(errorOutput.String(), "no workspace or global plugin is installed") {
		t.Fatalf("stdout=%q stderr=%q", output.String(), errorOutput.String())
	}
}

func TestPluginsExplicitNamePrefersWorkspaceAndWarnsOnGlobalFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := t.TempDir()
	workspacePlugins := filepath.Join(workspace, ".conven", "plugins")
	globalPlugins := filepath.Join(home, ".conven", "plugins")
	if err := os.MkdirAll(workspacePlugins, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(globalPlugins, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspacePlugins, "shared.py"), []byte("#!/bin/sh\necho workspace\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalPlugins, "shared.py"), []byte("#!/bin/sh\necho global-shared\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalPlugins, "global-only.py"), []byte("#!/bin/sh\necho global-only\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{Output: &output, Error: &errorOutput, Context: context.Background(), Cwd: workspace, Version: "test-version"}
	if code := app.Run([]string{"plugins", "--run", "shared"}); code != 0 {
		t.Fatalf("workspace run exit code = %d: %s", code, errorOutput.String())
	}
	if output.String() != "workspace\n" || errorOutput.Len() != 0 {
		t.Fatalf("workspace precedence stdout=%q stderr=%q", output.String(), errorOutput.String())
	}
	output.Reset()
	if code := app.Run([]string{"plugins", "--run", "global-only"}); code != 0 {
		t.Fatalf("global fallback exit code = %d: %s", code, errorOutput.String())
	}
	if output.String() != "global-only\n" || !strings.Contains(errorOutput.String(), "Warning: Workspace plugin not found; using the global plugin.") || !strings.Contains(errorOutput.String(), "  - Plugin: global-only") {
		t.Fatalf("global fallback stdout=%q stderr=%q", output.String(), errorOutput.String())
	}
	output.Reset()
	errorOutput.Reset()
	if code := app.Run([]string{"plugins", "--run", "global-only.py"}); code != 0 {
		t.Fatalf("global .py fallback exit code = %d: %s", code, errorOutput.String())
	}
	if output.String() != "global-only\n" || !strings.Contains(errorOutput.String(), "Warning: Workspace plugin not found; using the global plugin.") || !strings.Contains(errorOutput.String(), "  - Plugin: global-only") {
		t.Fatalf("global .py fallback stdout=%q stderr=%q", output.String(), errorOutput.String())
	}
	output.Reset()
	errorOutput.Reset()
	if code := app.Run([]string{"plugins", "--run", "--global", "global-only.py"}); code != 0 {
		t.Fatalf("forced global .py run exit code = %d: %s", code, errorOutput.String())
	}
	if output.String() != "global-only\n" || !strings.Contains(errorOutput.String(), "Warning: Running a global plugin.") || !strings.Contains(errorOutput.String(), "  - Plugin: global-only") {
		t.Fatalf("forced global .py stdout=%q stderr=%q", output.String(), errorOutput.String())
	}
	output.Reset()
	errorOutput.Reset()
	if code := app.Run([]string{"plugins", "--global", "--run", "global-only.py"}); code != 0 {
		t.Fatalf("prefix global .py run exit code = %d: %s", code, errorOutput.String())
	}
	if output.String() != "global-only\n" || !strings.Contains(errorOutput.String(), "Warning: Running a global plugin.") || !strings.Contains(errorOutput.String(), "  - Plugin: global-only") {
		t.Fatalf("prefix global .py stdout=%q stderr=%q", output.String(), errorOutput.String())
	}
	for _, arguments := range [][]string{
		{"plugins", "--run", "--global"},
		{"plugins", "--global", "--run"},
	} {
		output.Reset()
		errorOutput.Reset()
		if code := app.Run(arguments); code != 1 {
			t.Fatalf("%v exit code = %d: %s", arguments, code, errorOutput.String())
		}
		if output.Len() != 0 || !strings.Contains(errorOutput.String(), "conven: plugins --global --run requires a plugin name") {
			t.Fatalf("%v stdout=%q stderr=%q", arguments, output.String(), errorOutput.String())
		}
	}
}

func TestInvalidSubcommandFlagUsesStderr(t *testing.T) {
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{Output: &output, Error: &errorOutput, Version: "test-version"}

	if code := app.Run([]string{"services", "--start", "--unknown"}); code != 2 {
		t.Fatalf("exit code = %d", code)
	}
	if output.Len() != 0 {
		t.Fatalf("stdout = %q", output.String())
	}
	if !strings.Contains(errorOutput.String(), "flag provided but not defined") {
		t.Fatalf("stderr = %q", errorOutput.String())
	}
}

func TestLeafFlagErrorsUseCanonicalDoubleDash(t *testing.T) {
	commands := []struct {
		name      string
		arguments []string
	}{
		{name: "init", arguments: []string{"init"}},
		{name: "config", arguments: []string{"config"}},
		{name: "doctor", arguments: []string{"doctor"}},
		{name: "registry", arguments: []string{"services", "--registry"}},
		{name: "start", arguments: []string{"services", "--start"}},
		{name: "restart", arguments: []string{"services", "--restart"}},
		{name: "cleanup", arguments: []string{"services", "--cleanup"}},
		{name: "status", arguments: []string{"services", "--status"}},
		{name: "stop", arguments: []string{"services", "--stop"}},
		{name: "stop-all", arguments: []string{"services", "--stop-all"}},
		{name: "logs", arguments: []string{"services", "--logs"}},
		{name: "dashboard", arguments: []string{"services", "--dashboard"}},
		{name: "list", arguments: []string{"services", "--list"}},
		{name: "policy edit", arguments: []string{"policy", "--edit"}},
		{name: "policy import", arguments: []string{"policy", "--import"}},
		{name: "policy reset", arguments: []string{"policy", "--reset"}},
	}
	for _, command := range commands {
		t.Run(command.name, func(t *testing.T) {
			var output bytes.Buffer
			var errorOutput bytes.Buffer
			app := App{Output: &output, Error: &errorOutput, Cwd: t.TempDir(), Version: "test-version"}
			arguments := append(append([]string(nil), command.arguments...), "--unknown-long")
			if code := app.Run(arguments); code != 2 {
				t.Fatalf("exit code = %d: stdout=%q stderr=%q", code, output.String(), errorOutput.String())
			}
			if !strings.Contains(errorOutput.String(), "flag provided but not defined: --unknown-long\n") {
				t.Fatalf("stderr does not preserve the long option spelling: %q", errorOutput.String())
			}
			if strings.Contains(errorOutput.String(), "flag provided but not defined: -unknown-long\n") {
				t.Fatalf("stderr retained a single-dash long option: %q", errorOutput.String())
			}
			if output.Len() != 0 {
				t.Fatalf("stdout = %q", output.String())
			}
		})
	}
}

func TestFlagHelpUsesCanonicalDoubleDashForLongOptions(t *testing.T) {
	commands := []struct {
		name      string
		arguments []string
		flag      string
	}{
		{name: "config", arguments: []string{"config", "--help"}, flag: "global"},
		{name: "doctor", arguments: []string{"doctor", "--help"}, flag: "env"},
		{name: "registry", arguments: []string{"services", "--registry", "--help"}, flag: "prune"},
		{name: "start", arguments: []string{"services", "--start", "--help"}, flag: "dry-run"},
		{name: "restart", arguments: []string{"services", "--restart", "--help"}, flag: "dashboard"},
		{name: "stop", arguments: []string{"services", "--stop", "--help"}, flag: "force"},
		{name: "logs", arguments: []string{"services", "--logs", "--help"}, flag: "tail"},
		{name: "policy import", arguments: []string{"policy", "--import", "--help"}, flag: "edit"},
	}
	for _, command := range commands {
		t.Run(command.name, func(t *testing.T) {
			var output bytes.Buffer
			var errorOutput bytes.Buffer
			app := App{Output: &output, Error: &errorOutput, Version: "test-version"}
			if code := app.Run(command.arguments); code != 0 {
				t.Fatalf("exit code = %d: stdout=%q stderr=%q", code, output.String(), errorOutput.String())
			}
			if !strings.Contains(output.String(), "\n  --"+command.flag) {
				t.Fatalf("help does not show --%s: %q", command.flag, output.String())
			}
			if strings.Contains(output.String(), "\n  -"+command.flag) {
				t.Fatalf("help retained -%s: %q", command.flag, output.String())
			}
			if errorOutput.Len() != 0 {
				t.Fatalf("stderr = %q", errorOutput.String())
			}
		})
	}
}

func TestFlagValueErrorsUseCanonicalDoubleDash(t *testing.T) {
	tests := []struct {
		name      string
		arguments []string
		want      string
	}{
		{name: "unknown with value", arguments: []string{"init", "--unknown=value"}, want: "flag provided but not defined: --unknown\n"},
		{name: "invalid boolean", arguments: []string{"services", "--start", "--test=wat"}, want: `invalid boolean value "wat" for --test: parse error`},
		{name: "invalid boolean preserves value", arguments: []string{"services", "--start", "--test=value for -decoy"}, want: `invalid boolean value "value for -decoy" for --test: parse error`},
		{name: "missing value", arguments: []string{"services", "--start", "--env"}, want: "flag needs an argument: --env\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			var errorOutput bytes.Buffer
			app := App{Output: &output, Error: &errorOutput, Version: "test-version"}
			if code := app.Run(test.arguments); code != 2 {
				t.Fatalf("exit code = %d: stdout=%q stderr=%q", code, output.String(), errorOutput.String())
			}
			if !strings.Contains(errorOutput.String(), test.want) {
				t.Fatalf("stderr does not contain %q: %q", test.want, errorOutput.String())
			}
		})
	}
}

func TestCanonicalFlagOutputPreservesShortFlagsAndProse(t *testing.T) {
	input := "flag provided but not defined: -h\n  -x\n  -long-name\nnon-interactive -123 --already-long\ninvalid value \"value for flag -decoy\" for flag -timeout: parse error\n"
	want := "flag provided but not defined: -h\n  -x\n  --long-name\nnon-interactive -123 --already-long\ninvalid value \"value for flag -decoy\" for flag --timeout: parse error\n"
	if got := canonicalFlagOutput(input); got != want {
		t.Fatalf("canonical flag output = %q, want %q", got, want)
	}
}

func TestRestartEnvironmentFlagHintPrecedesParseError(t *testing.T) {
	for _, test := range []struct {
		name      string
		arguments []string
		action    string
	}{
		{name: "test", arguments: []string{"--test"}, action: "  => switch with conven services --start --test."},
		{name: "dev", arguments: []string{"--dev"}, action: "  => switch with conven services --start --dev."},
		{name: "env value", arguments: []string{"--env", "staging"}, action: "  => switch with conven services --start --env NAME."},
		{name: "env equals", arguments: []string{"--env=staging"}, action: "  => switch with conven services --start --env NAME."},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			var errorOutput bytes.Buffer
			app := App{Output: &output, Error: &errorOutput, Version: "test-version"}
			arguments := append([]string{"services", "--restart"}, test.arguments...)
			if code := app.Run(arguments); code != 2 {
				t.Fatalf("exit code = %d: stdout=%q stderr=%q", code, output.String(), errorOutput.String())
			}
			hintIndex := strings.Index(errorOutput.String(), "Warning: --restart reuses the current session environment;")
			actionIndex := strings.Index(errorOutput.String(), test.action)
			usageIndex := strings.Index(errorOutput.String(), "Usage:\n")
			if hintIndex < 0 || actionIndex < hintIndex || usageIndex < 0 || actionIndex >= usageIndex {
				t.Fatalf("restart hint does not precede usage: %q", errorOutput.String())
			}
			if strings.Contains(errorOutput.String(), "flag provided but not defined:") {
				t.Fatalf("restart hint still includes the redundant parser error: %q", errorOutput.String())
			}
			if output.Len() != 0 {
				t.Fatalf("stdout = %q", output.String())
			}
		})
	}
}

func TestRestartEnvironmentFlagHintOnlyDescribesTheActualError(t *testing.T) {
	for _, test := range []struct {
		name      string
		arguments []string
		code      int
	}{
		{name: "other unknown flag", arguments: []string{"services", "--restart", "--unknown"}, code: 2},
		{name: "help first", arguments: []string{"services", "--restart", "--help", "--test"}, code: 0},
		{name: "invalid known flag first", arguments: []string{"services", "--restart", "--skip-build=wat", "--test"}, code: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			var errorOutput bytes.Buffer
			app := App{Output: &output, Error: &errorOutput, Version: "test-version"}
			if code := app.Run(test.arguments); code != test.code {
				t.Fatalf("exit code = %d: stdout=%q stderr=%q", code, output.String(), errorOutput.String())
			}
			if strings.Contains(output.String(), "--restart reuses the current session environment") || strings.Contains(errorOutput.String(), "--restart reuses the current session environment") {
				t.Fatalf("unrelated parse result received a restart environment hint: stdout=%q stderr=%q", output.String(), errorOutput.String())
			}
		})
	}
}

func TestWorkspaceOverrideFlagsWereRemoved(t *testing.T) {
	commands := []struct {
		name      string
		arguments []string
	}{
		{name: "registry", arguments: []string{"services", "--registry"}},
		{name: "start", arguments: []string{"services", "--start"}},
		{name: "restart", arguments: []string{"services", "--restart"}},
		{name: "status", arguments: []string{"services", "--status"}},
		{name: "stop", arguments: []string{"services", "--stop"}},
		{name: "stop-all", arguments: []string{"services", "--stop-all"}},
		{name: "logs", arguments: []string{"services", "--logs"}},
		{name: "dashboard", arguments: []string{"services", "--dashboard"}},
		{name: "list", arguments: []string{"services", "--list"}},
		{name: "cleanup", arguments: []string{"services", "--cleanup"}},
		{name: "doctor", arguments: []string{"doctor"}},
	}
	for _, command := range commands {
		t.Run(command.name+" help", func(t *testing.T) {
			var output bytes.Buffer
			var errorOutput bytes.Buffer
			app := App{Output: &output, Error: &errorOutput, Version: "test-version"}

			arguments := append(append([]string(nil), command.arguments...), "--help")
			if code := app.Run(arguments); code != 0 {
				t.Fatalf("exit code = %d", code)
			}
			for _, removed := range []string{"--workspace", "--config"} {
				if strings.Contains(output.String(), removed) {
					t.Fatalf("%s help still exposes %s: %q", command.name, removed, output.String())
				}
			}
			if errorOutput.Len() != 0 {
				t.Fatalf("stderr = %q", errorOutput.String())
			}
		})

		for _, removed := range []string{"workspace", "config"} {
			t.Run(command.name+" rejects "+removed, func(t *testing.T) {
				var output bytes.Buffer
				var errorOutput bytes.Buffer
				app := App{Output: &output, Error: &errorOutput, Cwd: t.TempDir(), Version: "test-version"}

				arguments := append(append([]string(nil), command.arguments...), "--"+removed+"=/tmp/elsewhere")
				if code := app.Run(arguments); code != 2 {
					t.Fatalf("exit code = %d", code)
				}
				if output.Len() != 0 {
					t.Fatalf("stdout = %q", output.String())
				}
				if !strings.Contains(errorOutput.String(), "flag provided but not defined: --"+removed) {
					t.Fatalf("stderr = %q", errorOutput.String())
				}
			})
		}
	}

	var output bytes.Buffer
	app := App{Output: &output, Error: &output, Version: "test-version"}
	if code := app.Run([]string{"help"}); code != 0 {
		t.Fatalf("root help exit code = %d", code)
	}
	for _, removed := range []string{"--workspace", "--config"} {
		if strings.Contains(output.String(), removed) {
			t.Fatalf("root help still exposes %s: %q", removed, output.String())
		}
	}
}

func TestTailFlagReplacesFollow(t *testing.T) {
	for _, action := range []string{"--start", "--restart", "--logs"} {
		t.Run(action+" help", func(t *testing.T) {
			var output bytes.Buffer
			var errorOutput bytes.Buffer
			app := App{Output: &output, Error: &errorOutput, Version: "test-version"}

			if code := app.Run([]string{"services", action, "--help"}); code != 0 {
				t.Fatalf("exit code = %d", code)
			}
			if !strings.Contains(output.String(), "\n  --tail") {
				t.Fatalf("%s help does not expose --tail: %q", action, output.String())
			}
			if strings.Contains(output.String(), "follow") {
				t.Fatalf("%s help still exposes --follow: %q", action, output.String())
			}
			if errorOutput.Len() != 0 {
				t.Fatalf("stderr = %q", errorOutput.String())
			}
		})

		t.Run(action+" rejects follow", func(t *testing.T) {
			var output bytes.Buffer
			var errorOutput bytes.Buffer
			app := App{Output: &output, Error: &errorOutput, Cwd: t.TempDir(), Version: "test-version"}

			if code := app.Run([]string{"services", action, "--follow"}); code != 2 {
				t.Fatalf("exit code = %d", code)
			}
			if output.Len() != 0 {
				t.Fatalf("stdout = %q", output.String())
			}
			if !strings.Contains(errorOutput.String(), "flag provided but not defined: --follow") {
				t.Fatalf("stderr = %q", errorOutput.String())
			}
		})
	}

	var output bytes.Buffer
	app := App{Output: &output, Error: &output, Version: "test-version"}
	if code := app.Run([]string{"services", "--help"}); code != 0 {
		t.Fatalf("services help exit code = %d", code)
	}
	if !strings.Contains(output.String(), "--tail") || strings.Contains(output.String(), "--follow") {
		t.Fatalf("services help did not replace --follow with --tail: %q", output.String())
	}
}

func TestRestartHelpExposesDashboardMode(t *testing.T) {
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{Output: &output, Error: &errorOutput, Version: "test-version"}

	if code := app.Run([]string{"services", "--restart", "--help"}); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(output.String(), "\n  --dashboard") || !strings.Contains(output.String(), "last --tail or --dashboard flag wins") {
		t.Fatalf("restart help does not expose dashboard mode: %q", output.String())
	}
	if errorOutput.Len() != 0 {
		t.Fatalf("stderr = %q", errorOutput.String())
	}
}

func TestLogsTailNonTerminalOutputContainsOnlyPrefixedLogs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".conven"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspace, "api"), 0700); err != nil {
		t.Fatal(err)
	}
	manifest := `version: 2
workspace:
  name: non-terminal-tail
services:
  api:
    path: api
    runner:
      run: [sleep, "600"]
`
	if err := os.WriteFile(filepath.Join(workspace, ".conven", "conven.yaml"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".conven", "catalog.yaml"), []byte("version: 1\nservices: []\ndisabledRpcBindings: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "api.log")
	if err := os.WriteFile(logPath, []byte("ready\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := convenruntime.NewStore(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(&convenruntime.Session{
		Environment: "dev",
		Services:    []convenruntime.ServiceProcess{{Name: "api", LogPath: logPath}},
	}); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var errorOutput bytes.Buffer
	done := make(chan int, 1)
	go func() {
		app := App{
			Input:   input,
			Output:  writer,
			Error:   &errorOutput,
			Context: ctx,
			Cwd:     workspace,
			Version: "test",
		}
		done <- app.Run([]string{"services", "--logs", "--tail", "api"})
		writer.Close()
	}()
	line, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if line != "[api] ready\n" {
		t.Fatalf("non-terminal CLI tail output = %q", line)
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit code = %d: %s", code, errorOutput.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("non-terminal CLI tail did not stop after cancellation")
	}
	if errorOutput.Len() != 0 {
		t.Fatalf("stderr = %q", errorOutput.String())
	}
}

func TestLogsUsesLastDisplayModeFlag(t *testing.T) {
	tests := []struct {
		arguments []string
		want      logDisplayMode
	}{
		{arguments: nil, want: logDisplaySnapshot},
		{arguments: []string{"--tail"}, want: logDisplayPlain},
		{arguments: []string{"--dashboard"}, want: logDisplayDashboard},
		{arguments: []string{"--tail", "api", "--dashboard"}, want: logDisplayDashboard},
		{arguments: []string{"--dashboard", "api", "--tail"}, want: logDisplayPlain},
		{arguments: []string{"--tail", "--dashboard=false"}, want: logDisplayPlain},
	}
	for _, test := range tests {
		tail := false
		dashboard := false
		for _, argument := range test.arguments {
			if argument == "--tail" {
				tail = true
			}
			if argument == "--dashboard" {
				dashboard = true
			}
		}
		if got := resolveLogDisplayMode(test.arguments, tail, dashboard); got != test.want {
			t.Fatalf("resolve mode %v = %v, want %v", test.arguments, got, test.want)
		}
	}
}

func TestRestartDefaultsToDashboardOnlyOnInteractiveTerminal(t *testing.T) {
	tests := []struct {
		arguments          []string
		tail               bool
		dashboard          bool
		dashboardAvailable bool
		want               logDisplayMode
	}{
		{dashboardAvailable: true, want: logDisplayDashboard},
		{dashboardAvailable: false, want: logDisplaySnapshot},
		{arguments: []string{"--tail"}, tail: true, dashboardAvailable: true, want: logDisplayPlain},
		{arguments: []string{"--dashboard"}, dashboard: true, dashboardAvailable: false, want: logDisplayDashboard},
		{arguments: []string{"--tail", "--dashboard"}, tail: true, dashboard: true, dashboardAvailable: true, want: logDisplayDashboard},
		{arguments: []string{"--dashboard", "--tail"}, tail: true, dashboard: true, dashboardAvailable: true, want: logDisplayPlain},
	}
	for _, test := range tests {
		if got := resolveRestartLogDisplayMode(test.arguments, test.tail, test.dashboard, test.dashboardAvailable); got != test.want {
			t.Fatalf("resolve restart mode %v available=%t = %v, want %v", test.arguments, test.dashboardAvailable, got, test.want)
		}
	}
}

func TestLogsDashboardAliasAndLastModeRouting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".conven"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspace, "api"), 0700); err != nil {
		t.Fatal(err)
	}
	manifest := `version: 2
workspace:
  name: dashboard-alias
services:
  api:
    path: api
    runner:
      run: [sleep, "600"]
`
	if err := os.WriteFile(filepath.Join(workspace, ".conven", "conven.yaml"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".conven", "catalog.yaml"), []byte("version: 1\nservices: []\ndisabledRpcBindings: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "api.log")
	if err := os.WriteFile(logPath, []byte("ready\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := convenruntime.NewStore(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(&convenruntime.Session{
		Environment: "dev",
		Services:    []convenruntime.ServiceProcess{{Name: "api", LogPath: logPath}},
	}); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()

	for _, arguments := range [][]string{
		{"services", "--dashboard", "api"},
		{"services", "--logs", "--dashboard", "api"},
		{"services", "--logs", "--tail", "--dashboard", "api"},
	} {
		var output bytes.Buffer
		var errorOutput bytes.Buffer
		app := App{Input: input, Output: &output, Error: &errorOutput, Cwd: workspace, Version: "test"}
		if code := app.Run(arguments); code != 1 {
			t.Fatalf("%v exit code = %d", arguments, code)
		}
		if !strings.Contains(errorOutput.String(), "dashboard requires an interactive terminal") {
			t.Fatalf("%v stderr = %q", arguments, errorOutput.String())
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{Input: input, Output: &output, Error: &errorOutput, Context: ctx, Cwd: workspace, Version: "test"}
	arguments := []string{"services", "--logs", "--dashboard", "--tail", "api"}
	if code := app.Run(arguments); code != 0 {
		t.Fatalf("%v exit code = %d: %s", arguments, code, errorOutput.String())
	}
	if strings.Contains(output.String(), "\x1b[?1049h") {
		t.Fatalf("plain-last output entered dashboard: %q", output.String())
	}
}

func TestEnvironmentShortcutsMatchEnvFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := environmentShortcutWorkspace(t)
	store, err := convenruntime.NewStore(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		arguments []string
		want      string
	}{
		{name: "start dev", arguments: []string{"services", "--start", "--dry-run", "--dev", "api"}, want: "Environment: dev\n"},
		{name: "start default", arguments: []string{"services", "--start", "--dry-run", "api"}, want: "Environment: dev\n"},
		{name: "start test", arguments: []string{"services", "--start", "--dry-run", "--test", "api"}, want: "Environment: test\n"},
		{name: "start env test", arguments: []string{"services", "--start", "--dry-run", "--env", "test", "api"}, want: "Environment: test\n"},
		{name: "start matching test", arguments: []string{"services", "--start", "--dry-run", "--test", "--env", "test", "api"}, want: "Environment: test\n"},
		{name: "start repeated matching test", arguments: []string{"services", "--start", "--dry-run", "--env", "test", "--test", "--env=test", "api"}, want: "Environment: test\n"},
		{name: "start custom", arguments: []string{"services", "--start", "--dry-run", "--env", "stage", "api"}, want: "Environment: stage\n"},
		{name: "doctor dev", arguments: []string{"doctor", "--dev"}, want: "Environment: dev\n"},
		{name: "doctor default", arguments: []string{"doctor"}, want: "Environment: dev\n"},
		{name: "doctor test", arguments: []string{"doctor", "--test"}, want: "Environment: test\n"},
		{name: "doctor env test", arguments: []string{"doctor", "--env=test"}, want: "Environment: test\n"},
	} {
			t.Run(test.name, func(t *testing.T) {
				var output bytes.Buffer
				app := App{Output: &output, Error: &output, Cwd: workspace, Version: "test"}
				if code := app.Run(test.arguments); code != 0 {
					t.Fatalf("exit code = %d: %s", code, output.String())
				}
				if !strings.Contains(output.String(), test.want) {
					t.Fatalf("output = %q, want %q", output.String(), test.want)
				}
			for _, path := range []string{
				"Runtime: " + store.Root + "\n",
				"Current: " + store.CurrentDir + "\n",
				} {
					if !strings.Contains(output.String(), path) {
						t.Fatalf("output = %q, want fixed runtime path %q", output.String(), path)
					}
				}
			})
		}
}

func TestEnvironmentShortcutsRejectConflictsBeforeWorkspaceLookup(t *testing.T) {
	for _, test := range []struct {
		name      string
		arguments []string
		want      string
	}{
		{name: "both shortcuts", arguments: []string{"services", "--start", "--dev", "--test", "api"}, want: "--dev and --test"},
		{name: "dev versus test", arguments: []string{"services", "--start", "--dev", "--env", "test", "api"}, want: `--dev conflicts with --env "test"`},
		{name: "test versus dev", arguments: []string{"doctor", "--test", "--env", "dev"}, want: `--test conflicts with --env "dev"`},
		{name: "test versus stage", arguments: []string{"services", "--start", "api", "--test", "--env=stage"}, want: `--test conflicts with --env "stage"`},
		{name: "repeated env cannot hide conflict", arguments: []string{"services", "--start", "api", "--test", "--env", "dev", "--env=test"}, want: `--test conflicts with --env "dev"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			app := App{Output: &output, Error: &output, Cwd: t.TempDir(), Version: "test"}
			if code := app.Run(test.arguments); code != 1 {
				t.Fatalf("exit code = %d: %s", code, output.String())
			}
			if !strings.Contains(output.String(), test.want) {
				t.Fatalf("output = %q, want conflict %q", output.String(), test.want)
			}
			if strings.Contains(output.String(), "not a Conven workspace") {
				t.Fatalf("workspace lookup happened before conflict validation: %q", output.String())
			}
		})
	}
}

func TestStopHelpDocumentsMutualExclusionAndForceRisk(t *testing.T) {
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{Output: &output, Error: &errorOutput, Version: "test-version"}
	if code := app.Run([]string{"services", "--stop", "--help"}); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	for _, expected := range []string{
		"(<service...>|--all)",
		"conven services --stop-all",
		"bypass identity checks",
		"--force is destructive",
		"conven services --status",
		"unleased shared connection records",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("stop help is missing %q: %s", expected, output.String())
		}
	}
	if errorOutput.Len() != 0 {
		t.Fatalf("stderr = %q", errorOutput.String())
	}
}

func TestStopRejectsAllWithServiceNames(t *testing.T) {
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{Output: &output, Error: &errorOutput, Version: "test-version"}
	if code := app.Run([]string{"services", "--stop", "--all", "api"}); code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(errorOutput.String(), "--all cannot be combined with service names") {
		t.Fatalf("stderr = %q", errorOutput.String())
	}
}

func TestStopAllShortcutRejectsServiceNames(t *testing.T) {
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{Output: &output, Error: &errorOutput, Version: "test-version"}
	if code := app.Run([]string{"services", "--stop-all", "api"}); code != 1 {
		t.Fatalf("exit code = %d", code)
	}
	if output.Len() != 0 {
		t.Fatalf("stdout = %q", output.String())
	}
	if !strings.Contains(errorOutput.String(), "cannot be combined with service names") {
		t.Fatalf("stderr = %q", errorOutput.String())
	}
}

func TestStopAllShortcutMatchesStopAll(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := environmentShortcutWorkspace(t)
	outputs := make([]string, 0, 2)
	for _, arguments := range [][]string{
		{"services", "--stop", "--all"},
		{"services", "--stop-all"},
	} {
		var output bytes.Buffer
		var errorOutput bytes.Buffer
		app := App{Output: &output, Error: &errorOutput, Cwd: workspace, Version: "test-version"}
		if code := app.Run(arguments); code != 0 {
			t.Fatalf("%v exit code = %d: %s", arguments, code, errorOutput.String())
		}
		if errorOutput.Len() != 0 {
			t.Fatalf("%v stderr = %q", arguments, errorOutput.String())
		}
		outputs = append(outputs, output.String())
	}
	if outputs[0] != outputs[1] {
		t.Fatalf("--stop --all output %q differs from --stop-all output %q", outputs[0], outputs[1])
	}
	if !strings.Contains(outputs[0], "Warning: No Conven session found.") {
		t.Fatalf("stop-all output = %q", outputs[0])
	}
}

func TestStopAllShortcutAcceptsForce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := environmentShortcutWorkspace(t)
	var output bytes.Buffer
	app := App{Output: &output, Error: &output, Cwd: workspace, Version: "test-version"}
	if code := app.Run([]string{"services", "--stop-all", "--force"}); code != 0 {
		t.Fatalf("exit code = %d: %s", code, output.String())
	}
	if !strings.Contains(output.String(), "No unleased shared connection records were recoverable.") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestServiceActionsRejectInvalidPositionalsBeforeWorkspaceLookup(t *testing.T) {
	for _, test := range []struct {
		name      string
		arguments []string
		message   string
	}{
		{name: "list", arguments: []string{"services", "--list", "api"}, message: "does not accept service arguments"},
		{name: "registry", arguments: []string{"services", "--registry", "api"}, message: "does not accept service arguments"},
		{name: "status", arguments: []string{"services", "--status", "api"}, message: "does not accept service arguments"},
		{name: "cleanup", arguments: []string{"services", "--cleanup", "api"}, message: "does not accept service arguments"},
		{name: "stop all", arguments: []string{"services", "--stop", "--all", "api"}, message: "cannot be combined with service names"},
		{name: "stop-all", arguments: []string{"services", "--stop-all", "api"}, message: "cannot be combined with service names"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			app := App{Output: &output, Error: &output, Cwd: t.TempDir(), Version: "test-version"}
			if code := app.Run(test.arguments); code != 1 {
				t.Fatalf("exit code = %d: %s", code, output.String())
			}
			if !strings.Contains(output.String(), test.message) {
				t.Fatalf("output = %q, want %q", output.String(), test.message)
			}
			if strings.Contains(output.String(), "not a Conven workspace") {
				t.Fatalf("workspace lookup happened before argument validation: %q", output.String())
			}
		})
	}
}

func TestServicesListStatusRestartAndStopRoutes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := environmentShortcutWorkspace(t)
	store, err := convenruntime.NewStore(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		arguments []string
		code      int
		message   string
	}{
		{name: "list", arguments: []string{"services", "--list"}, code: 0, message: "api"},
		{name: "status", arguments: []string{"services", "--status"}, code: 0, message: "Warning: No Conven session found."},
		{name: "restart", arguments: []string{"services", "--restart"}, code: 1, message: "no running Conven session found"},
		{name: "stop requires target", arguments: []string{"services", "--stop"}, code: 1, message: "requires service names or --all"},
	} {
			t.Run(test.name, func(t *testing.T) {
				var output bytes.Buffer
				app := App{Output: &output, Error: &output, Cwd: workspace, Version: "test-version"}
				if code := app.Run(test.arguments); code != test.code {
					t.Fatalf("exit code = %d, want %d: %s", code, test.code, output.String())
			}
				if !strings.Contains(output.String(), test.message) {
					t.Fatalf("output = %q, want %q", output.String(), test.message)
				}
				if test.name == "status" {
				for _, path := range []string{
					"Runtime: " + store.Root + "\n",
					"Current: " + store.CurrentDir + "\n",
					} {
						if !strings.Contains(output.String(), path) {
							t.Fatalf("status output = %q, want fixed runtime path %q", output.String(), path)
						}
					}
				}
			})
	}
}

func TestServicesCleanupRequiresStoppedSessionAndPreservesConfigs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := environmentShortcutWorkspace(t)
	store, err := convenruntime.NewStore(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ResetCurrent(); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(store.CurrentDir, "artifacts", "api")
	log := filepath.Join(store.CurrentDir, "logs", "api.log")
	configPath := filepath.Join(store.CurrentDir, "configs", "api", "application.yaml")
	for _, path := range []string{artifact, log, configPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("runtime\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Save(&convenruntime.Session{Workspace: workspace}); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	app := App{Output: &output, Error: &output, Cwd: workspace, Version: "test-version"}
	if code := app.Run([]string{"services", "--cleanup"}); code != 1 {
		t.Fatalf("active-session cleanup exit code = %d: %s", code, output.String())
	}
	if !strings.Contains(output.String(), "conven services --stop-all") {
		t.Fatalf("active-session cleanup output = %q", output.String())
	}
	if _, err := os.Stat(artifact); err != nil {
		t.Fatalf("rejected cleanup changed artifact: %v", err)
	}
	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if code := app.Run([]string{"services", "--cleanup"}); code != 0 {
		t.Fatalf("cleanup exit code = %d: %s", code, output.String())
	}
	if !strings.Contains(output.String(), "Cleared build artifacts and service logs") {
		t.Fatalf("cleanup output = %q", output.String())
	}
	for _, path := range []string{artifact, log} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("cleanup target %q still exists: %v", path, err)
		}
	}
	if data, err := os.ReadFile(configPath); err != nil || string(data) != "runtime\n" {
		t.Fatalf("cleanup changed config: data=%q err=%v", data, err)
	}
}

func TestStartWithoutServicesRejectsNonTerminal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	serviceDir := filepath.Join(workspace, "user-svc")
	if err := os.Mkdir(serviceDir, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := `version: 2
workspace:
  name: test
services:
  user-svc:
    path: user-svc
    runner:
      run: [sh, -c, "while :; do sleep 1; done"]
`
	if err := os.Mkdir(filepath.Join(workspace, ".conven"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".conven", "conven.yaml"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	var output bytes.Buffer
	app := App{
		Input:   input,
		Output:  &output,
		Error:   &output,
		Context: context.Background(),
		Cwd:     workspace,
		Version: "test",
	}
	code := app.Run([]string{"services", "--start", "--dry-run"})
	if code == 0 {
		t.Fatal("non-terminal selection unexpectedly succeeded")
	}
	if !strings.Contains(output.String(), "interactive selection requires a terminal") {
		t.Fatalf("unexpected output %q", output.String())
	}
}

func TestStartWithDependenciesExpandsVersionOneServiceSelection(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".conven"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"api", "worker", "storage"} {
		if err := os.Mkdir(filepath.Join(workspace, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	manifest := `version: 1
workspace:
  name: with-dependencies
environments:
  dev:
    connection:
      driver: none
services:
  api:
    path: api
    runner:
      run: [api]
    dependencies:
      worker:
        localEnv:
          WORKER_ADDRESS: 127.0.0.1
  worker:
    path: worker
    runner:
      run: [worker]
    dependencies:
      storage:
        localEnv:
          STORAGE_ADDRESS: 127.0.0.1
  storage:
    path: storage
    runner:
      run: [storage]
`
	if err := os.WriteFile(filepath.Join(workspace, ".conven", "conven.yaml"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{Output: &output, Error: &errorOutput, Cwd: workspace, Version: "test"}
	if code := app.Run([]string{"services", "--start", "--dry-run", "api"}); code != 0 {
		t.Fatalf("default exit code = %d: %s", code, errorOutput.String())
	}
	if !strings.Contains(output.String(), "Local services: api\n") || strings.Contains(output.String(), "Local services: api, worker") {
		t.Fatalf("default start plan expanded services: %q", output.String())
	}
	output.Reset()
	errorOutput.Reset()
	if code := app.Run([]string{"services", "--start", "--dry-run", "api", "--with-dependencies"}); code != 0 {
		t.Fatalf("exit code = %d: %s", code, errorOutput.String())
	}
	if !strings.Contains(output.String(), "Local services: api, worker, storage") || !strings.Contains(output.String(), "Start groups: storage -> worker -> api") {
		t.Fatalf("start plan = %q", output.String())
	}
}

func TestAskStartReplacementOffersStopOrCancel(t *testing.T) {
	for _, test := range []struct {
		name    string
		answer  string
		replace bool
	}{
		{name: "short stop", answer: "s\n", replace: true},
		{name: "short stop eof", answer: "s", replace: true},
		{name: "stop", answer: "STOP\n", replace: true},
		{name: "yes", answer: "yes\n", replace: true},
		{name: "short cancel", answer: "c\n"},
		{name: "cancel", answer: "cancel\n"},
		{name: "no", answer: "no\n"},
		{name: "default", answer: "\n"},
		{name: "eof"},
		{name: "retry invalid", answer: "later\ns\n", replace: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			replace, err := askStartReplacement(strings.NewReader(test.answer), &output, []string{"api", "worker"})
			if err != nil {
				t.Fatal(err)
			}
			if replace != test.replace {
				t.Fatalf("replace = %t, want %t", replace, test.replace)
			}
			for _, expected := range []string{"api, worker", "Stop then start", "Cancel"} {
				if !strings.Contains(output.String(), expected) {
					t.Fatalf("prompt = %q, want %q", output.String(), expected)
				}
			}
			if test.name == "retry invalid" && !strings.Contains(output.String(), "  => Choose [s] Stop then start or [c] Cancel:") {
				t.Fatalf("invalid answer was not retried: %q", output.String())
			}
		})
	}
}

func TestAskStartReplacementContextStopsWhenCancelledWithoutInput(t *testing.T) {
	input, inputWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer inputWriter.Close()
	promptReader, promptWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer promptReader.Close()
	defer promptWriter.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		replace bool
		err     error
	}
	done := make(chan result, 1)
	go func() {
		replace, err := askStartReplacementContext(ctx, input, promptWriter, []string{"api"})
		done <- result{replace: replace, err: err}
	}()

	expectedPrompt := "Warning: Workspace already has running services.\n  - Services: api\n  => Choose [s] Stop then start or [c] Cancel (default): "
	prompt := make([]byte, len(expectedPrompt))
	if _, err := io.ReadFull(promptReader, prompt); err != nil {
		t.Fatal(err)
	}
	if string(prompt) != expectedPrompt {
		t.Fatalf("prompt = %q", string(prompt))
	}
	cancel()

	select {
	case got := <-done:
		if got.replace {
			t.Fatal("cancelled confirmation allowed replacement")
		}
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled confirmation remained blocked waiting for input")
	}
}

func TestStartRunningSessionUsesInjectedReplacementConfirmation(t *testing.T) {
	for _, test := range []struct {
		name       string
		replace    bool
		confirmErr error
	}{
		{name: "cancel"},
		{name: "stop then start", replace: true},
		{name: "confirmation cancelled", confirmErr: context.Canceled},
	} {
			t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			workspaceRoot := environmentShortcutWorkspace(t)
			var output bytes.Buffer
			initialApp := App{Output: &output, Error: &output, Cwd: workspaceRoot, Version: "test"}
			if code := initialApp.Run([]string{"services", "--start", "--dev", "api"}); code != 0 {
				t.Fatalf("initial start exit code = %d: %s", code, output.String())
			}
			workspace, err := convenruntime.OpenWorkspace(convenruntime.CommonOptions{Cwd: workspaceRoot, Environment: "dev"})
			if err != nil {
				t.Fatal(err)
			}
			defer convenruntime.Stop(context.Background(), workspace, nil, true, false, &output)
			initial, err := workspace.Store.Load()
			if err != nil {
				t.Fatal(err)
			}
			oldProcess := initial.Services[0]
			confirmed := 0
			output.Reset()
			var errorOutput bytes.Buffer
			app := App{
				Output:  &output,
				Error:   &errorOutput,
				Cwd:     workspaceRoot,
				Version: "test",
				StartReplacementConfirmer: func(_ context.Context, services []string) (bool, error) {
					confirmed++
					if strings.Join(services, ",") != "api" {
						t.Fatalf("running services = %v", services)
					}
					return test.replace, test.confirmErr
				},
			}
			code := app.Run([]string{"services", "--start", "--dev", "api"})
			if test.confirmErr != nil {
				if code != 1 {
					t.Fatalf("replacement confirmation error exit code = %d: stdout=%s stderr=%s", code, output.String(), errorOutput.String())
				}
				if !strings.Contains(errorOutput.String(), context.Canceled.Error()) {
					t.Fatalf("replacement confirmation stderr = %q", errorOutput.String())
				}
			} else if code != 0 {
				t.Fatalf("replacement start exit code = %d: stdout=%s stderr=%s", code, output.String(), errorOutput.String())
			}
			if confirmed != 1 {
				t.Fatalf("replacement confirmation calls = %d", confirmed)
			}
			current, err := workspace.Store.Load()
			if err != nil {
				t.Fatal(err)
			}
			if test.confirmErr != nil {
				if current.Services[0].PID != oldProcess.PID || !convenruntime.ProcessAlive(oldProcess.PID) {
					t.Fatalf("confirmation error changed the running process: old=%#v current=%#v", oldProcess, current.Services)
				}
				return
			}
			if !test.replace {
				if current.Services[0].PID != oldProcess.PID || !convenruntime.ProcessAlive(oldProcess.PID) {
					t.Fatalf("cancel changed the running process: old=%#v current=%#v", oldProcess, current.Services)
				}
				if errorOutput.Len() != 0 {
					t.Fatalf("cancel stderr = %q; stdout = %q", errorOutput.String(), output.String())
				}
				if !strings.Contains(output.String(), "==> Start cancelled\n  - Running services were left unchanged.") {
					t.Fatalf("cancel stdout = %q", output.String())
				}
				return
			}
			if current.Services[0].PID == oldProcess.PID || convenruntime.ProcessAlive(oldProcess.PID) {
				t.Fatalf("replacement did not replace process: old=%#v current=%#v", oldProcess, current.Services)
			}
		})
	}
}

func TestStartRunningSessionNonTerminalDoesNotConsumeConfirmationInput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspaceRoot := environmentShortcutWorkspace(t)
	var output bytes.Buffer
	initialApp := App{Output: &output, Error: &output, Cwd: workspaceRoot, Version: "test"}
	if code := initialApp.Run([]string{"services", "--start", "--dev", "api"}); code != 0 {
		t.Fatalf("initial start exit code = %d: %s", code, output.String())
	}
	workspace, err := convenruntime.OpenWorkspace(convenruntime.CommonOptions{Cwd: workspaceRoot, Environment: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	defer convenruntime.Stop(context.Background(), workspace, nil, true, false, &output)
	initial, err := workspace.Store.Load()
	if err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(t.TempDir(), "confirmation")
	if err := os.WriteFile(inputPath, []byte("stop\n"), 0600); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output.Reset()
	app := App{Input: input, Output: &output, Error: &output, Cwd: workspaceRoot, Version: "test"}
	if code := app.Run([]string{"services", "--start", "--dev", "api"}); code != 1 {
		t.Fatalf("non-terminal replacement exit code = %d: %s", code, output.String())
	}
	if !strings.Contains(output.String(), "replacement confirmation requires an interactive terminal") {
		t.Fatalf("non-terminal output = %q", output.String())
	}
	position, err := input.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatal(err)
	}
	if position != 0 {
		t.Fatalf("non-terminal replacement consumed input: offset=%d", position)
	}
	if !convenruntime.ProcessAlive(initial.Services[0].PID) {
		t.Fatalf("non-terminal replacement stopped the current service: %#v", initial.Services[0])
	}
}

func TestCompletions(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			completion, err := Completion(shell)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(completion, "conven") {
				t.Fatalf("completion for %s is empty", shell)
			}
			workingDirectoryMarker := "-C"
			if shell == "fish" {
				workingDirectoryMarker = "-s C"
			}
			if !strings.Contains(completion, workingDirectoryMarker) {
				t.Fatalf("completion for %s is missing -C", shell)
			}
			for _, command := range []string{"init", "services", "config", "catalog", "policy", "plugins", "status", "doctor"} {
				if !strings.Contains(completion, command) {
					t.Fatalf("completion for %s is missing %s", shell, command)
				}
			}
			for _, action := range []string{"--list", "--registry", "--status", "--logs", "--dashboard", "--start", "--restart", "--stop", "--stop-all", "--cleanup"} {
				marker := action
				if shell == "fish" {
					marker = "-l " + strings.TrimPrefix(action, "--")
				}
				if !strings.Contains(completion, marker) {
					t.Fatalf("completion for %s is missing %s", shell, action)
				}
			}
			for _, action := range []string{"--install", "--remove", "--run"} {
				marker := action
				if shell == "fish" {
					marker = "-l " + strings.TrimPrefix(action, "--")
				}
				if !strings.Contains(completion, marker) {
					t.Fatalf("completion for %s is missing plugin action %s", shell, action)
				}
			}
			if strings.Contains(completion, "convening") {
				t.Fatalf("completion for %s still exposes convening", shell)
			}
			if strings.Contains(completion, "--follow") {
				t.Fatalf("completion for %s still exposes --follow", shell)
			}
			for _, removed := range []string{"--workspace", "--config", "-l workspace", "-l config", "--use-template", "-l use-template"} {
				if strings.Contains(completion, removed) {
					t.Fatalf("completion for %s still exposes %s", shell, removed)
				}
			}
		})
	}
}

func TestCompletionsScopeFlagsByServiceAction(t *testing.T) {
	bash, err := Completion("bash")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`compgen -W "init services config catalog policy plugins status doctor help version"`,
		`compgen -W "-C init services config catalog policy plugins status doctor help version"`,
		`compgen -d -- "$cur"`,
		`if [ "$subcommand" = "help" ]`,
		`if [ "$subcommand" = "services" ]`,
		`--list|--status|--dashboard|--cleanup)`,
		`--registry)`,
		`options="--prune --help"`,
		`--logs)`,
		`options="--tail --dashboard --help"`,
		`--start)`,
		`options="--env --dev --test --kubeconfig --context --namespace --tail --dry-run --with-dependencies --skip-build --skip-verify --help"`,
		`--restart)`,
		`options="--tail --dashboard --skip-build --skip-verify --help"`,
		`--stop)`,
		`options="--all --force --help"`,
		`--stop-all)`,
		`options="--force --help"`,
		`options="--env --dev --test --kubeconfig --context --namespace --help"`,
		`if [ "$subcommand" = "policy" ]`,
		`options="--edit --import --reset --help"`,
		`--edit|--reset)`,
		`--import)`,
		`options="--edit --help"`,
		`if [ "$subcommand" = "catalog" ]`,
		`options="--edit --validate --help"`,
		`if [ "$subcommand" = "plugins" ]`,
		`[ "${COMP_WORDS[action_index + 1]}" = "--global" ]`,
		`options="--global --output --disable-bindings"`,
		`options="--global --install --list --remove --run --help"`,
		`elif [ "$action" = "--global" ]; then`,
		`options="--run"`,
		`compopt -o filenames 2>/dev/null`,
	} {
		if !strings.Contains(bash, expected) {
			t.Fatalf("bash completion is missing %q", expected)
		}
	}
	for _, removed := range []string{"init discover", "init start", "init restart", "init status", "init stop", "init logs", "init list"} {
		if strings.Contains(bash, removed) {
			t.Fatalf("bash completion still exposes legacy top-level command marker %q", removed)
		}
	}

	zsh, err := Completion("zsh")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`root_candidates=(`,
		`'-C:run as if conven was started in a different directory'`,
		`_directories`,
		`case $words[2] in`,
		`'1:command:(init services config catalog policy plugins status doctor help version)'`,
		`'services:manage workspace services'`,
		`'catalog:edit or validate the workspace service catalog'`,
		`'policy:edit, import, or rebuild the workspace policy manifest'`,
		`'plugins:install, list, remove, or run Conven plugins'`,
		`'status:show the complete workspace and runtime status'`,
		`case $action in`,
		`--list|--status|--cleanup)`,
		`--registry)`,
		`--logs)`,
		`--dashboard)`,
		`--start)`,
		`--restart)`,
		`--stop)`,
		`--stop-all)`,
		`--cleanup[remove saved build artifacts and service logs]`,
		`--dev[use the dev environment profile]`,
		`--test[use the test environment profile]`,
		`--all[stop every service and release the workspace connection]`,
		`--force[bypass identity checks and recover saved process groups]`,
		`--tail[stream aggregated logs as plain text]`,
		`--with-dependencies[also start transitive local service dependencies]`,
		`--dashboard[open the interactive log dashboard]`,
		`--prune[remove missing direct-child repository services]`,
		`--reset[destructively reset the manifest to scanned facts]`,
		`--import[import a local YAML file as the entire manifest]`,
		`--validate[validate the workspace service catalog]`,
		`'1::yaml file:_files'`,
		`--install[install a Python plugin]`,
		`--remove[remove an installed plugin]`,
		`--run[run an installed plugin]`,
		`--global[force a named user-global plugin run]`,
		`--global[use the user-global plugin scope]`,
		`words=($words[1] $words[3,-1])`,
		`'1:global plugin:'`,
		`--output[generator output path; omit the value for application.yaml]`,
		`--disable-bindings[replace disabled bindings for this generator run]`,
		`'1::plugin:'`,
	} {
		if !strings.Contains(zsh, expected) {
			t.Fatalf("zsh completion is missing %q", expected)
		}
	}
	for _, removed := range []string{"'discover:", "'start:", "'restart:", "'stop:", "'logs:", "'list:"} {
		if strings.Contains(zsh, removed) {
			t.Fatalf("zsh completion still exposes legacy top-level command %q", removed)
		}
	}

	fish, err := Completion("fish")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`function __conven_command_tokens`,
		`function __conven_without_command`,
		`__conven_global_context' -s C -r`,
		`function __conven_using_subcommand`,
		`function __conven_help_without_command`,
		`__conven_using_subcommand help; and __conven_help_without_command`,
		`function __conven_services_action`,
		`function __conven_services_without_action`,
		`function __conven_policy_without_action`,
		`function __conven_catalog_without_action`,
		`function __conven_policy_action`,
		`function __conven_policy_action_without_edit`,
		`function __conven_policy_import_without_source`,
		`function __conven_plugins_without_action`,
		`function __conven_plugins_action`,
		`function __conven_plugins_scope_position`,
		`function __conven_plugins_global_without_action`,
		`function __conven_plugins_global_run`,
		`function __conven_plugins_run_arguments`,
		`__conven_without_command' -a services`,
		`__conven_without_command' -a catalog`,
		`__conven_without_command' -a policy`,
		`__conven_without_command' -a plugins`,
		`__conven_without_command' -a status`,
		`__conven_services_without_action' -l list`,
		`__conven_services_without_action' -l registry`,
		`__conven_services_without_action' -l stop-all`,
		`__conven_services_without_action' -l cleanup`,
		`__conven_services_action --start' -l env`,
		`__conven_services_action --start' -l dev`,
		`__conven_services_action --start' -l test`,
		`__conven_services_action --start' -l tail`,
		`__conven_services_action --start' -l dry-run`,
		`__conven_services_action --start' -l with-dependencies`,
		`__conven_services_action --restart' -l tail`,
		`__conven_services_action --restart' -l dashboard`,
		`__conven_services_action --logs' -l tail`,
		`__conven_services_action --logs' -l dashboard`,
		`__conven_using_subcommand config' -l global`,
		`__conven_services_action --stop' -l all`,
		`__conven_services_action --stop' -l force`,
		`__conven_services_action --stop-all' -l force`,
		`__conven_services_action --registry' -l prune`,
		`__conven_using_subcommand policy; and __conven_policy_without_action' -l edit`,
		`__conven_using_subcommand policy; and __conven_policy_without_action' -l import`,
		`__conven_using_subcommand policy; and __conven_policy_without_action' -l reset`,
		`__conven_using_subcommand catalog; and __conven_catalog_without_action' -l edit`,
		`__conven_using_subcommand catalog; and __conven_catalog_without_action' -l validate`,
		`__conven_policy_action_without_edit --import' -l edit`,
		`__conven_policy_import_without_source' -F`,
		`__conven_using_subcommand plugins; and __conven_plugins_without_action' -l install`,
		`__conven_using_subcommand plugins; and __conven_plugins_without_action' -l list`,
		`__conven_using_subcommand plugins; and __conven_plugins_without_action' -l remove`,
		`__conven_using_subcommand plugins; and __conven_plugins_without_action' -l run`,
		`__conven_using_subcommand plugins; and __conven_plugins_without_action' -l global`,
		`__conven_plugins_global_without_action' -l run`,
		`__conven_plugins_scope_position --install' -l global`,
		`__conven_plugins_scope_position --list' -l global`,
		`__conven_plugins_scope_position --run' -l global`,
		`__conven_plugins_run_arguments' -l output`,
		`__conven_plugins_run_arguments' -l disable-bindings`,
	} {
		if !strings.Contains(fish, expected) {
			t.Fatalf("fish completion is missing %q", expected)
		}
	}
	for _, removed := range []string{"-a discover ", "-a start ", "-a restart ", "-a stop ", "-a logs ", "-a list "} {
		if strings.Contains(fish, removed) {
			t.Fatalf("fish completion still exposes legacy top-level command marker %q", removed)
		}
	}
}

func TestBashCompletionSupportsWorkingDirectoryOption(t *testing.T) {
	completion, err := Completion("bash")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		words string
		want  string
	}{
		{name: "command", words: "conven -C /tmp ser", want: "services\n"},
		{name: "repeated command", words: "conven -C /tmp -C .. doc", want: "doctor\n"},
		{name: "service action", words: "conven -C /tmp services --sta", want: "--status\n--start\n"},
		{name: "service flag", words: "conven -C /tmp services --start --tes", want: "--test\n"},
		{name: "help topic", words: "conven -C /tmp help ser", want: "services\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			script := completion + "\nCOMP_WORDS=(" + test.words + ")\nCOMP_CWORD=$((${#COMP_WORDS[@]} - 1))\n_conven\nfor candidate in \"${COMPREPLY[@]}\"; do printf '%s\\n' \"$candidate\"; done\n"
			output, err := exec.Command("bash", "-c", script).CombinedOutput()
			if err != nil {
				t.Fatalf("bash completion failed: %v: %s", err, output)
			}
			if string(output) != test.want {
				t.Fatalf("completion = %q, want %q", output, test.want)
			}
		})
	}
}

func TestBashCompletionPreservesSpacesInWorkingDirectoryCandidates(t *testing.T) {
	completion, err := Completion("bash")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "workspace with spaces"), 0700); err != nil {
		t.Fatal(err)
	}
	script := completion + "\nCOMP_WORDS=(conven -C workspace)\nCOMP_CWORD=2\n_conven\nprintf '<%s>\\n' \"${COMPREPLY[@]}\"\n"
	command := exec.Command("bash", "-c", script)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("bash completion failed: %v: %s", err, output)
	}
	if string(output) != "<workspace with spaces>\n" {
		t.Fatalf("completion = %q", output)
	}
}

func TestBashHelpCompletionOffersCommandTopics(t *testing.T) {
	completion, err := Completion("bash")
	if err != nil {
		t.Fatal(err)
	}
	script := completion + "\nCOMP_WORDS=(conven help ser)\nCOMP_CWORD=2\n_conven\nprintf '%s\\n' \"${COMPREPLY[@]}\"\n"
	output, err := exec.Command("bash", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("bash completion failed: %v: %s", err, output)
	}
	if string(output) != "services\n" {
		t.Fatalf("completion = %q, want services", output)
	}
}

func TestBashPluginInstallCompletesOnlyPythonFilesAndDirectories(t *testing.T) {
	completion, err := Completion("bash")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "plugin.py"), []byte("plugin"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "notes.txt"), []byte("notes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directory, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	script := completion + "\nCOMP_WORDS=(conven plugins --install '')\nCOMP_CWORD=3\n_conven\nprintf '%s\\n' \"${COMPREPLY[@]}\"\n"
	command := exec.Command("bash", "-c", script)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("bash completion failed: %v: %s", err, output)
	}
	for _, expected := range []string{"nested", "plugin.py"} {
		if !strings.Contains(string(output), expected+"\n") {
			t.Fatalf("completion is missing %q: %q", expected, output)
		}
	}
	if strings.Contains(string(output), "notes.txt") {
		t.Fatalf("completion includes a non-Python file: %q", output)
	}
}

func TestBashPluginGlobalCompletionOnlyImmediatelyAfterAction(t *testing.T) {
	completion, err := Completion("bash")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		words string
		want  string
	}{
		{name: "run scope", words: "conven plugins --run --g", want: "--global\n"},
		{name: "run after name", words: "conven plugins --run generator --g", want: ""},
		{name: "prefix global action", words: "conven plugins --global --r", want: "--run\n"},
		{name: "prefix global requires name", words: "conven plugins --global --run --g", want: ""},
		{name: "list scope", words: "conven plugins --list --g", want: "--global\n"},
		{name: "list after scope", words: "conven plugins --list --global --g", want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			script := completion + "\nCOMP_WORDS=(" + test.words + ")\nCOMP_CWORD=$((${#COMP_WORDS[@]} - 1))\n_conven\nfor candidate in \"${COMPREPLY[@]}\"; do printf '%s\\n' \"$candidate\"; done\n"
			output, err := exec.Command("bash", "-c", script).CombinedOutput()
			if err != nil {
				t.Fatalf("bash completion failed: %v: %s", err, output)
			}
			if string(output) != test.want {
				t.Fatalf("completion = %q, want %q", output, test.want)
			}
		})
	}
}

func TestBashPolicyCompletionDoesNotOfferSecondAction(t *testing.T) {
	completion, err := Completion("bash")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		words string
		want  string
	}{
		{name: "action", words: "conven policy --r", want: "--reset\n"},
		{name: "import action", words: "conven policy --i", want: "--import\n"},
		{name: "after action", words: "conven policy --edit --", want: "--help\n"},
		{name: "import options", words: "conven policy --import --", want: "--edit\n--help\n"},
		{name: "after import source", words: "conven policy --import README.md --", want: "--edit\n--help\n"},
		{name: "after import edit", words: "conven policy --import README.md --edit --", want: "--help\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			script := completion + "\nCOMP_WORDS=(" + test.words + ")\nCOMP_CWORD=$((${#COMP_WORDS[@]} - 1))\n_conven\nprintf '%s\\n' \"${COMPREPLY[@]}\"\n"
			output, err := exec.Command("bash", "-c", script).CombinedOutput()
			if err != nil {
				t.Fatalf("bash completion failed: %v: %s", err, output)
			}
			if string(output) != test.want {
				t.Fatalf("completion = %q, want %q", output, test.want)
			}
		})
	}
}

func TestBashPolicyImportCompletesLocalFilesBeforeSource(t *testing.T) {
	completion, err := Completion("bash")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "candidate-policy.yaml"), []byte("candidate"), 0600); err != nil {
		t.Fatal(err)
	}
	script := completion + "\nCOMP_WORDS=(conven policy --import candidate)\nCOMP_CWORD=3\n_conven\nprintf '%s\\n' \"${COMPREPLY[@]}\"\n"
	command := exec.Command("bash", "-c", script)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("bash completion failed: %v: %s", err, output)
	}
	if string(output) != "candidate-policy.yaml\n" {
		t.Fatalf("completion = %q", output)
	}
}

func TestFlagsMayFollowServiceArguments(t *testing.T) {
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dryRun := flags.Bool("dry-run", false, "")
	testEnvironment := flags.Bool("test", false, "")
	environment := flags.String("env", "", "")
	normalized, err := intersperseFlags(flags, []string{"user-svc", "--dry-run", "--env", "test", "--test", "order-svc"})
	if err != nil {
		t.Fatal(err)
	}
	if err := flags.Parse(normalized); err != nil {
		t.Fatal(err)
	}
	if !*dryRun || !*testEnvironment || *environment != "test" {
		t.Fatalf("flags were not parsed: dryRun=%v test=%v environment=%q", *dryRun, *testEnvironment, *environment)
	}
	if strings.Join(flags.Args(), ",") != "user-svc,order-svc" {
		t.Fatalf("service arguments = %v", flags.Args())
	}
}

func environmentShortcutWorkspace(t *testing.T) string {
	t.Helper()
	return environmentShortcutWorkspaceAt(t, t.TempDir())
}

func environmentShortcutWorkspaceAt(t *testing.T, workspace string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(workspace, ".conven"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspace, "api"), 0700); err != nil {
		t.Fatal(err)
	}
	manifest := `version: 2
workspace:
  name: environment-shortcuts
environments:
  dev:
    registry: dev-registry
  test:
    registry: test-registry
  stage:
    registry: stage-registry
services:
  api:
    path: api
    runner:
      run: [sh, -c, "while :; do sleep 1; done"]
`
	if err := os.WriteFile(filepath.Join(workspace, ".conven", "conven.yaml"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".conven", "catalog.yaml"), []byte("version: 1\nservices: []\ndisabledRpcBindings: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return workspace
}

func TestInitCreatesEmbeddedManifestAndRuntimeIgnoreWithoutOverwriting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := t.TempDir()
	boundary := filepath.Join(workspace, ".conven")
	if err := os.Mkdir(boundary, 0700); err != nil {
		t.Fatal(err)
	}
	ignorePath := filepath.Join(boundary, ".gitignore")
	if err := os.WriteFile(ignorePath, []byte("custom-rule\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workspace, ".conven", "conven.yaml")
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{Output: &output, Error: &errorOutput, Cwd: workspace, Version: "test"}
	if code := app.Run([]string{"init"}); code != 0 {
		t.Fatalf("exit code = %d: stdout=%s stderr=%s", code, output.String(), errorOutput.String())
	}
	wantInitFiles := "==> Initialized Conven workspace\n" +
		"  - Manifest: " + path + "\n" +
		"  - .conven/catalog.yaml\n" +
		"  - CONVEN-WORKSPACE-POLICY-GENERATOR-AI-SPEC.md\n" +
		"  - README.md\n" +
		"==> Initial service registry scan complete\n"
	if !strings.Contains(output.String(), wantInitFiles) || !strings.Contains(output.String(), "  - Discovered services: none") {
		t.Fatalf("init stdout = %q", output.String())
	}
	if errorOutput.String() != "Warning: No supported child repositories were detected.\n  - Manifest source: embedded example\n" {
		t.Fatalf("init stderr = %q", errorOutput.String())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "workspace:\n") || !strings.Contains(string(data), "services:\n") {
		t.Fatalf("generated manifest is not the application example: %s", data)
	}
	ignore, err := os.ReadFile(ignorePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(ignore) != "custom-rule\n/runtime/\n" {
		t.Fatalf("init did not merge runtime ignore non-destructively: %q", ignore)
	}
	pluginDirectory := filepath.Join(home, ".conven", "plugins")
	pluginInfo, err := os.Stat(pluginDirectory)
	if err != nil {
		t.Fatalf("init did not prepare the built-in plugin directory: %v", err)
	}
	if !pluginInfo.IsDir() {
		t.Fatalf("plugin path is not a directory: %s", pluginDirectory)
	}
	if strings.Contains(strings.ToLower(output.String()), "plugin") {
		t.Fatalf("init misleadingly reported plugins when no built-ins exist: %q", output.String())
	}
	if err := os.WriteFile(path, []byte("custom\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	errorOutput.Reset()
	if code := app.Run([]string{"init"}); code != 0 {
		t.Fatalf("reinitialize exit code = %d: %s", code, output.String())
	}
	wantReinitFiles := "==> Reused Conven workspace\n" +
		"  - Manifest: " + path + "\n" +
		"  - .conven/catalog.yaml Skipped\n" +
		"  - CONVEN-WORKSPACE-POLICY-GENERATOR-AI-SPEC.md Skipped\n" +
		"  - README.md Skipped\n" +
		"  - Existing manifest was not overwritten.\n"
	if !strings.Contains(output.String(), wantReinitFiles) {
		t.Fatalf("reinitialize stdout = %q", output.String())
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "custom\n" {
		t.Fatalf("init overwrote manifest: %q", data)
	}
	ignore, err = os.ReadFile(ignorePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(ignore) != "custom-rule\n/runtime/\n" {
		t.Fatalf("reinitialize duplicated or overwrote runtime ignore: %q", ignore)
	}
	if _, err := os.Stat(pluginDirectory); err != nil {
		t.Fatalf("reinitialize did not preserve the plugin directory: %v", err)
	}
	if strings.Contains(strings.ToLower(output.String()), "plugin") {
		t.Fatalf("reinitialize misleadingly reported plugins when no built-ins exist: %q", output.String())
	}
	if errorOutput.Len() != 0 {
		t.Fatalf("reinitialize stderr = %q", errorOutput.String())
	}

	missingWorkspaceFile := filepath.Join(workspace, ".conven", "catalog.yaml")
	if err := os.Remove(missingWorkspaceFile); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	errorOutput.Reset()
	if code := app.Run([]string{"init"}); code != 0 {
		t.Fatalf("repair init exit code = %d: %s", code, output.String())
	}
	wantRepairFiles := "==> Reused Conven workspace\n" +
		"  - Manifest: " + path + "\n" +
		"  - .conven/catalog.yaml\n" +
		"  - CONVEN-WORKSPACE-POLICY-GENERATOR-AI-SPEC.md Skipped\n" +
		"  - README.md Skipped\n" +
		"  - Existing manifest was not overwritten.\n"
	if !strings.Contains(output.String(), wantRepairFiles) {
		t.Fatalf("repair init stdout = %q", output.String())
	}
	if _, err := os.Stat(missingWorkspaceFile); err != nil {
		t.Fatalf("repair init did not recreate .conven/catalog.yaml: %v", err)
	}
	if errorOutput.Len() != 0 {
		t.Fatalf("repair init stderr = %q", errorOutput.String())
	}
}

func TestInitAndRegistryRecognizeDirectChildServices(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	writeCLIServiceRepository(t, workspace, "alpha-service")
	var output bytes.Buffer
	app := App{Output: &output, Error: &output, Cwd: workspace, Version: "test"}
	if code := app.Run([]string{"init"}); code != 0 {
		t.Fatalf("init exit code = %d: %s", code, output.String())
	}
	if !strings.Contains(output.String(), "==> Initial service registry scan complete\n  - Discovered services: alpha-service") {
		t.Fatalf("init output = %q", output.String())
	}
	catalogPath := filepath.Join(workspace, ".conven", "catalog.yaml")
	catalog := "version: 1\nservices: []\ndisabledRpcBindings: [bindingStaysUnchanged]\n"
	if err := os.WriteFile(catalogPath, []byte(catalog), 0644); err != nil {
		t.Fatal(err)
	}

	writeCLIServiceRepository(t, workspace, "beta-service")
	output.Reset()
	app.Cwd = filepath.Join(workspace, "alpha-service", "go")
	if code := app.Run([]string{"services", "--registry"}); code != 0 {
		t.Fatalf("registry exit code = %d: %s", code, output.String())
	}
	for _, expected := range []string{
		"==> Service registry scan complete",
		"  - Discovered services: alpha-service, beta-service",
		"  - Added services: beta-service",
		"  - Manifest:",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("registry output is missing %q: %q", expected, output.String())
		}
	}
	data, err := os.ReadFile(filepath.Join(workspace, ".conven", "conven.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "alpha-service:") || !strings.Contains(string(data), "beta-service:") {
		t.Fatalf("manifest = %s", data)
	}
	assertFileContents(t, catalogPath, catalog)
}

func TestPolicyEditUsesInjectedEditorAndPublishesValidatedManifest(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0700); err != nil {
		t.Fatal(err)
	}
	original := `version: 2
workspace:
  name: before
services:
  api:
    path: api
    runner:
      run: [api]
`
	if err := os.WriteFile(manifestPath, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{
		Output:  &output,
		Error:   &errorOutput,
		Cwd:     filepath.Join(workspace, ".conven"),
		Version: "test",
		PolicyEditor: func(_ context.Context, path string) error {
			called = true
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			updated := strings.Replace(string(data), "name: before", "name: after", 1)
			return os.WriteFile(path, []byte(updated), 0600)
		},
	}
	if code := app.Run([]string{"policy", "--edit"}); code != 0 {
		t.Fatalf("exit code = %d: %s", code, errorOutput.String())
	}
	if !called || !strings.Contains(output.String(), "==> Updated Conven policy manifest\n  - Manifest:") || errorOutput.Len() != 0 {
		t.Fatalf("called=%v stdout=%q stderr=%q", called, output.String(), errorOutput.String())
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "name: after") {
		t.Fatalf("manifest = %s", data)
	}
}

func TestPolicyResetReportsBackupAndLostRulesWarning(t *testing.T) {
	workspace := t.TempDir()
	writeCLIServiceRepository(t, workspace, "api-service")
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0700); err != nil {
		t.Fatal(err)
	}
	original := `version: 2
workspace:
  name: manual
services:
  custom-api:
    path: api-service
    ports:
      http: 18080
    runner:
      run: [manual]
`
	if err := os.WriteFile(manifestPath, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{Output: &output, Error: &errorOutput, Cwd: workspace, Version: "test"}
	if code := app.Run([]string{"policy", "--reset"}); code != 0 {
		t.Fatalf("exit code = %d: %s", code, errorOutput.String())
	}
	for _, expected := range []string{
		"==> Reset Conven policy manifest to scan baseline",
		"  - Discovered services: api-service",
		"  - Manifest:",
		"  - Backup:",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("output is missing %q: %q", expected, output.String())
		}
	}
	for _, expected := range []string{
		"Warning: Policy reset rebuilds the complete workspace manifest.",
		"  - Review and restore manually declared policies, environments, ports, dependencies, health checks, patches, and runner changes.",
		"  => conven doctor",
		"  => conven services --start --dry-run",
	} {
		if !strings.Contains(errorOutput.String(), expected) {
			t.Fatalf("stderr is missing %q: %q", expected, errorOutput.String())
		}
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "custom-api") || !strings.Contains(string(data), "api-service") {
		t.Fatalf("manifest = %s", data)
	}
}

func TestPolicyResetAlreadyAtScanBaselineDoesNotWarn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	writeCLIServiceRepository(t, workspace, "api-service")
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{Output: &output, Error: &errorOutput, Cwd: workspace, Version: "test"}
	if code := app.Run([]string{"init"}); code != 0 {
		t.Fatalf("init exit code = %d: stdout=%s stderr=%s", code, output.String(), errorOutput.String())
	}
	output.Reset()
	errorOutput.Reset()
	if code := app.Run([]string{"policy", "--reset"}); code != 0 {
		t.Fatalf("reset exit code = %d: stdout=%s stderr=%s", code, output.String(), errorOutput.String())
	}
	if !strings.Contains(output.String(), "==> Conven policy manifest already matches scan baseline") {
		t.Fatalf("reset stdout = %q", output.String())
	}
	if errorOutput.Len() != 0 {
		t.Fatalf("reset stderr = %q", errorOutput.String())
	}
}

func TestPolicyImportReplacesManifestAndFeedsSubsequentServicesCommands(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0700); err != nil {
		t.Fatal(err)
	}
	original := `version: 2
workspace:
  name: before-import
services:
  old-api:
    path: old-api
    runner:
      run: [old-api]
`
	if err := os.WriteFile(manifestPath, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	importPath := filepath.Join(t.TempDir(), "generated policy.yaml")
	imported := `version: 2
workspace:
  name: imported-workspace
services:
  generated-api:
    path: generated-api
    runner:
      run: [generated-api]
`
	if err := os.WriteFile(importPath, []byte(imported), 0644); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{
		Output:  &output,
		Error:   &errorOutput,
		Cwd:     workspace,
		Version: "test",
		PolicyEditor: func(context.Context, string) error {
			t.Fatal("policy --import without --edit launched the editor")
			return nil
		},
		SingleSelector: func(context.Context, *os.File, io.Writer, selector.Prompt, []selector.Candidate) (selector.Candidate, bool, error) {
			t.Fatal("explicit policy import unexpectedly opened the selector")
			return selector.Candidate{}, false, nil
		},
	}
	if code := app.Run([]string{"policy", "--import", importPath}); code != 0 {
		t.Fatalf("exit code = %d: %s", code, errorOutput.String())
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte(imported)) {
		t.Fatalf("manifest = %s", data)
	}
	for _, expected := range []string{
		"==> Replaced Conven policy manifest",
		"  - Source: "+importPath,
		"  - Manifest: "+manifestPath,
		"  - Backup:",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("output is missing %q: %q", expected, output.String())
		}
	}
	for _, expected := range []string{
		"Warning: Policy import treats the source as the complete workspace manifest.",
		"  - Repository scan results were not merged.",
		"  => conven doctor",
		"  => conven services --start --dry-run",
	} {
		if !strings.Contains(errorOutput.String(), expected) {
			t.Fatalf("stderr is missing %q: %q", expected, errorOutput.String())
		}
	}

	output.Reset()
	errorOutput.Reset()
	if code := app.Run([]string{"services", "--list"}); code != 0 {
		t.Fatalf("services --list exit code = %d: %s", code, errorOutput.String())
	}
	if !strings.Contains(output.String(), "generated-api") || strings.Contains(output.String(), "old-api") {
		t.Fatalf("services --list output = %q", output.String())
	}
}

func TestPolicyImportWithoutFilenameSelectsTheOnlyWorkspaceYAML(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0700); err != nil {
		t.Fatal(err)
	}
	original := `version: 2
workspace:
  name: before-import
services:
  old-api:
    path: old-api
    runner:
      run: [old-api]
`
	if err := os.WriteFile(manifestPath, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	imported := `version: 2
workspace:
  name: default-import
services:
  generated-api:
    path: generated-api
    runner:
      run: [generated-api]
`
	defaultPath := filepath.Join(workspace, "application.yaml")
	if err := os.WriteFile(defaultPath, []byte(imported), 0644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(workspace, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	var errorOutput bytes.Buffer
	selectorCalled := false
	app := App{
		Output:  &output,
		Error:   &errorOutput,
		Cwd:     nested,
		Version: "test",
		SingleSelector: func(_ context.Context, _ *os.File, _ io.Writer, prompt selector.Prompt, candidates []selector.Candidate) (selector.Candidate, bool, error) {
			selectorCalled = true
			if prompt.Title != "Select a policy file" || prompt.ConfirmationLabel != "Importing policy file" || prompt.EmptySelectionNotice != "Select one policy file before confirming." {
				t.Fatalf("prompt = %#v", prompt)
			}
			if len(candidates) != 1 || candidates[0].Name != "application.yaml" {
				t.Fatalf("candidates = %#v", candidates)
			}
			return candidates[0], true, nil
		},
	}
	if code := app.Run([]string{"policy", "--import"}); code != 0 {
		t.Fatalf("exit code = %d: %s", code, errorOutput.String())
	}
	if !selectorCalled {
		t.Fatal("policy selector was not called for the sole YAML file")
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte(imported)) {
		t.Fatalf("manifest = %s", data)
	}
	for _, expected := range []string{"Warning: Policy import treats the source as the complete workspace manifest."} {
		if !strings.Contains(errorOutput.String(), expected) {
			t.Fatalf("stderr is missing %q: %q", expected, errorOutput.String())
		}
	}
	if !strings.Contains(output.String(), "==> Replaced Conven policy manifest\n  - Source: "+defaultPath) {
		t.Fatalf("stdout = %q", output.String())
	}
}

func TestPolicyImportWithoutFilenameRequiresWorkspaceYAML(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0700); err != nil {
		t.Fatal(err)
	}
	original := `version: 2
workspace:
  name: unchanged
services:
  api:
    path: api
    runner:
      run: [api]
`
	if err := os.WriteFile(manifestPath, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{Output: &output, Error: &errorOutput, Cwd: workspace, Version: "test"}
	if code := app.Run([]string{"policy", "--import"}); code != 1 {
		t.Fatalf("exit code = %d: stdout=%q stderr=%q", code, output.String(), errorOutput.String())
	}
	if output.Len() != 0 {
		t.Fatalf("stdout = %q", output.String())
	}
	for _, expected := range []string{"no YAML policy files were found in the workspace root", "specify a YAML filename"} {
		if !strings.Contains(errorOutput.String(), expected) {
			t.Fatalf("stderr is missing %q: %q", expected, errorOutput.String())
		}
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte(original)) {
		t.Fatalf("missing default import changed manifest: %s", data)
	}
}

func TestWorkspaceYAMLCandidatesUseSortedDirectRegularFiles(t *testing.T) {
	workspace := t.TempDir()
	for _, name := range []string{"z.yml", "a.yaml", "M.YAML", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte("candidate"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(workspace, "directory.yaml"), 0700); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(workspace, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "nested.yaml"), []byte("candidate"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(workspace, "a.yaml"), filepath.Join(workspace, "link.yaml")); err != nil {
		t.Fatal(err)
	}

	candidates, err := workspaceYAMLCandidates(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 3 || candidates[0].Name != "M.YAML" || candidates[1].Name != "a.yaml" || candidates[2].Name != "z.yml" {
		t.Fatalf("candidates = %#v", candidates)
	}
}

func TestPolicyImportWithoutFilenameSelectsAmongMultipleWorkspaceYAMLFiles(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0700); err != nil {
		t.Fatal(err)
	}
	original := `version: 2
workspace:
  name: before
services:
  api:
    path: api
    runner:
      run: [api]
`
	if err := os.WriteFile(manifestPath, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	alpha := strings.Replace(original, "name: before", "name: alpha", 1)
	beta := strings.Replace(original, "name: before", "name: beta", 1)
	if err := os.WriteFile(filepath.Join(workspace, "alpha.yaml"), []byte(alpha), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "beta.yml"), []byte(beta), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{
		Output:  &output,
		Error:   &errorOutput,
		Cwd:     workspace,
		Version: "test",
		SingleSelector: func(_ context.Context, _ *os.File, _ io.Writer, _ selector.Prompt, candidates []selector.Candidate) (selector.Candidate, bool, error) {
			if len(candidates) != 2 || candidates[0].Name != "alpha.yaml" || candidates[1].Name != "beta.yml" {
				t.Fatalf("candidates = %#v", candidates)
			}
			return candidates[1], true, nil
		},
	}
	if code := app.Run([]string{"policy", "--import"}); code != 0 {
		t.Fatalf("exit code = %d: stdout=%q stderr=%q", code, output.String(), errorOutput.String())
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != beta {
		t.Fatalf("manifest = %s", data)
	}
	if !strings.Contains(output.String(), "  - Source: "+filepath.Join(workspace, "beta.yml")) {
		t.Fatalf("stdout = %q", output.String())
	}
}

func TestPolicyImportWithoutFilenameCanCancelSelection(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0700); err != nil {
		t.Fatal(err)
	}
	original := `version: 2
workspace:
  name: unchanged
services:
  api:
    path: api
    runner:
      run: [api]
`
	if err := os.WriteFile(manifestPath, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "candidate.yaml"), []byte(strings.Replace(original, "unchanged", "changed", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{
		Output:  &output,
		Error:   &errorOutput,
		Cwd:     workspace,
		Version: "test",
		SingleSelector: func(context.Context, *os.File, io.Writer, selector.Prompt, []selector.Candidate) (selector.Candidate, bool, error) {
			return selector.Candidate{}, false, nil
		},
	}
	if code := app.Run([]string{"policy", "--import"}); code != 0 {
		t.Fatalf("exit code = %d: stderr=%q", code, errorOutput.String())
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original || output.String() != "==> Policy import cancelled\n  - Conven policy manifest was not changed.\n" || errorOutput.Len() != 0 {
		t.Fatalf("manifest=%s stdout=%q stderr=%q", data, output.String(), errorOutput.String())
	}
}

func TestPolicyImportWithoutFilenameReportsNonInteractiveSelection(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".conven"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "candidate.yaml"), []byte("candidate"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{
		Output:  &output,
		Error:   &errorOutput,
		Cwd:     workspace,
		Version: "test",
		SingleSelector: func(context.Context, *os.File, io.Writer, selector.Prompt, []selector.Candidate) (selector.Candidate, bool, error) {
			return selector.Candidate{}, false, selector.ErrNotTerminal
		},
	}
	if code := app.Run([]string{"policy", "--import"}); code != 1 {
		t.Fatalf("exit code = %d: stdout=%q stderr=%q", code, output.String(), errorOutput.String())
	}
	if output.Len() != 0 || !strings.Contains(errorOutput.String(), "policy import selection requires an interactive terminal; specify a YAML filename explicitly") {
		t.Fatalf("stdout=%q stderr=%q", output.String(), errorOutput.String())
	}
}

func TestPolicyImportEditAcceptsFlagBeforeOrAfterSource(t *testing.T) {
	for _, editFirst := range []bool{false, true} {
		name := "edit-after-source"
		if editFirst {
			name = "edit-before-source"
		}
		t.Run(name, func(t *testing.T) {
			workspace := t.TempDir()
			manifestPath := filepath.Join(workspace, ".conven", "conven.yaml")
			if err := os.MkdirAll(filepath.Dir(manifestPath), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifestPath, []byte(`version: 2
workspace:
  name: before
services:
  api:
    path: api
    runner:
      run: [api]
`), 0600); err != nil {
				t.Fatal(err)
			}
			importPath := filepath.Join(t.TempDir(), "policy.yaml")
			if err := os.WriteFile(importPath, []byte(`version: 2
workspace:
  name: imported
services:
  api:
    path: api
    runner:
      run: [api]
`), 0644); err != nil {
				t.Fatal(err)
			}
			called := false
			var output bytes.Buffer
			app := App{
				Output:  &output,
				Error:   &output,
				Cwd:     workspace,
				Version: "test",
				PolicyEditor: func(_ context.Context, path string) error {
					called = true
					data, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					edited := strings.Replace(string(data), "name: imported", "name: reviewed", 1)
					return os.WriteFile(path, []byte(edited), 0600)
				},
			}
			arguments := []string{"policy", "--import", importPath, "--edit"}
			if editFirst {
				arguments = []string{"policy", "--import", "--edit", importPath}
			}
			if code := app.Run(arguments); code != 0 {
				t.Fatalf("exit code = %d: %s", code, output.String())
			}
			data, err := os.ReadFile(manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			if !called || !strings.Contains(string(data), "name: reviewed") {
				t.Fatalf("called=%v manifest=%s output=%s", called, data, output.String())
			}
		})
	}
}

func TestPolicyActionsRejectArgumentsAndUnknownFlags(t *testing.T) {
	for _, test := range []struct {
		arguments []string
		code      int
		message   string
	}{
		{[]string{"policy", "--edit", "extra"}, 1, "does not accept arguments"},
		{[]string{"policy", "--reset", "extra"}, 1, "does not accept arguments"},
		{[]string{"policy", "--edit", "--unknown"}, 2, "flag provided but not defined"},
		{[]string{"policy", "--import", "one.yaml", "two.yaml"}, 1, "accepts at most one YAML file"},
		{[]string{"policy", "--import", "--unknown", "one.yaml"}, 2, "flag provided but not defined"},
		{[]string{"policy", "--import", "--reset", "one.yaml"}, 2, "flag provided but not defined"},
	} {
		var output bytes.Buffer
		app := App{Output: &output, Error: &output, Cwd: t.TempDir(), Version: "test"}
		if code := app.Run(test.arguments); code != test.code {
			t.Fatalf("%v exit code = %d, want %d: %s", test.arguments, code, test.code, output.String())
		}
		if !strings.Contains(output.String(), test.message) || strings.Contains(output.String(), "not a Conven workspace") {
			t.Fatalf("%v output = %q", test.arguments, output.String())
		}
	}
}

func TestLaunchPolicyEditorUsesConvenEditorAndSupportsArguments(t *testing.T) {
	directory := t.TempDir()
	script := filepath.Join(directory, "editor.sh")
	capture := filepath.Join(directory, "capture")
	contents := "#!/bin/sh\nprintf '%s\\n%s\\n' \"$1\" \"$2\" > \"$EDITOR_CAPTURE\"\n"
	if err := os.WriteFile(script, []byte(contents), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR_CAPTURE", capture)
	t.Setenv("CONVEN_EDITOR", script+" --wait")
	t.Setenv("VISUAL", "false")
	t.Setenv("EDITOR", "false")
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	manifest := filepath.Join(directory, "manifest with spaces.yaml")
	if err := launchPolicyEditor(context.Background(), input, io.Discard, io.Discard, manifest); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "--wait\n"+manifest+"\n" {
		t.Fatalf("editor arguments = %q", data)
	}
}

func TestRegistryPruneRemovesMissingDirectChildService(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	writeCLIServiceRepository(t, workspace, "alpha-service")
	writeCLIServiceRepository(t, workspace, "beta-service")
	var output bytes.Buffer
	app := App{Output: &output, Error: &output, Cwd: workspace, Version: "test"}
	if code := app.Run([]string{"init"}); code != 0 {
		t.Fatalf("init exit code = %d: %s", code, output.String())
	}
	catalogPath := filepath.Join(workspace, ".conven", "catalog.yaml")
	catalog := "version: 1\nservices: []\ndisabledRpcBindings: [bindingSurvivesPrune]\n"
	if err := os.WriteFile(catalogPath, []byte(catalog), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(workspace, "alpha-service")); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if code := app.Run([]string{"services", "--registry", "--prune"}); code != 0 {
		t.Fatalf("registry --prune exit code = %d: %s", code, output.String())
	}
	if !strings.Contains(output.String(), "Pruned services: alpha-service") {
		t.Fatalf("registry --prune output = %q", output.String())
	}
	data, err := os.ReadFile(filepath.Join(workspace, ".conven", "conven.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "alpha-service:") || !strings.Contains(string(data), "beta-service:") {
		t.Fatalf("manifest after registry --prune = %s", data)
	}
	assertFileContents(t, catalogPath, catalog)
}

func TestRegistryRejectsArgumentsAndUnknownFlags(t *testing.T) {
	for _, test := range []struct {
		arguments []string
		code      int
		message   string
	}{
		{[]string{"services", "--registry", "service"}, 1, "does not accept service arguments"},
		{[]string{"services", "--registry", "--workspace", "/tmp/elsewhere"}, 2, "flag provided but not defined: --workspace"},
	} {
		var output bytes.Buffer
		app := App{Output: &output, Error: &output, Cwd: t.TempDir(), Version: "test"}
		if code := app.Run(test.arguments); code != test.code {
			t.Fatalf("%v exit code = %d, want %d: %s", test.arguments, code, test.code, output.String())
		}
		if !strings.Contains(output.String(), test.message) {
			t.Fatalf("%v output = %q", test.arguments, output.String())
		}
	}
}

func TestRegistryReportsKeptMissingRepositoriesAsUnchanged(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".conven"), 0700); err != nil {
		t.Fatal(err)
	}
	manifest := `version: 2
workspace:
  name: missing
services:
  removed-service:
    path: removed-service
    runner:
      run: [removed-service]
`
	if err := os.WriteFile(filepath.Join(workspace, ".conven", "conven.yaml"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{Output: &output, Error: &errorOutput, Cwd: workspace, Version: "test"}
	if code := app.Run([]string{"services", "--registry"}); code != 0 {
		t.Fatalf("exit code = %d: stdout=%s stderr=%s", code, output.String(), errorOutput.String())
	}
	if !strings.Contains(output.String(), "  - Manifest: unchanged; missing repositories kept") {
		t.Fatalf("output = %q", output.String())
	}
	if strings.Contains(output.String(), "already matches") {
		t.Fatalf("output contradicts missing repository status: %q", output.String())
	}
	for _, expected := range []string{
		"Warning: Service registry scan requires review.",
		"  - Missing repositories kept: removed-service",
		"  => conven services --registry --prune",
	} {
		if !strings.Contains(errorOutput.String(), expected) {
			t.Fatalf("stderr is missing %q: %q", expected, errorOutput.String())
		}
	}
	if strings.Contains(errorOutput.String(), "conven discover") {
		t.Fatalf("stderr uses the obsolete discovery command: %q", errorOutput.String())
	}
}

func TestInitRejectsUserHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var output bytes.Buffer
	app := App{Output: &output, Error: &output, Cwd: home, Version: "test"}

	if code := app.Run([]string{"init"}); code != 1 {
		t.Fatalf("exit code = %d: %s", code, output.String())
	}
	if !strings.Contains(output.String(), "reserved for global configuration") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestConfigLocalSetGetListAndUnset(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, ".conven"), 0700); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	app := App{Output: &output, Error: &output, Cwd: workspace, Version: "test"}
	if code := app.Run([]string{"config", "ktctl.path", "/custom/ktctl"}); code != 0 {
		t.Fatalf("set exit code = %d: %s", code, output.String())
	}
	output.Reset()
	if code := app.Run([]string{"config", "ktctl.kubeconfig", "/custom/kubeconfig"}); code != 0 {
		t.Fatalf("set kubeconfig exit code = %d: %s", code, output.String())
	}
	output.Reset()
	if code := app.Run([]string{"config", "ktctl.path"}); code != 0 {
		t.Fatalf("get exit code = %d: %s", code, output.String())
	}
	if output.String() != "/custom/ktctl\n" {
		t.Fatalf("get output = %q", output.String())
	}
	output.Reset()
	if code := app.Run([]string{"config", "--list"}); code != 0 {
		t.Fatalf("list exit code = %d: %s", code, output.String())
	}
	if output.String() != "ktctl.kubeconfig=/custom/kubeconfig\nktctl.path=/custom/ktctl\n" {
		t.Fatalf("list output = %q", output.String())
	}
	output.Reset()
	if code := app.Run([]string{"config", "--unset", "ktctl.path"}); code != 0 {
		t.Fatalf("unset exit code = %d: %s", code, output.String())
	}
	output.Reset()
	if code := app.Run([]string{"config", "--unset", "ktctl.kubeconfig"}); code != 0 {
		t.Fatalf("unset kubeconfig exit code = %d: %s", code, output.String())
	}
}

func TestGlobalConfigWorksOutsideWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()
	var output bytes.Buffer
	app := App{Output: &output, Error: &output, Cwd: cwd, Version: "test"}
	if code := app.Run([]string{"config", "--global", "ktctl.path", "custom-ktctl"}); code != 0 {
		t.Fatalf("set exit code = %d: %s", code, output.String())
	}
	output.Reset()
	if code := app.Run([]string{"config", "--global", "ktctl.kubeconfig", "/global/kubeconfig"}); code != 0 {
		t.Fatalf("set kubeconfig exit code = %d: %s", code, output.String())
	}
	output.Reset()
	if code := app.Run([]string{"config", "--global", "--list"}); code != 0 {
		t.Fatalf("list exit code = %d: %s", code, output.String())
	}
	if output.String() != "ktctl.kubeconfig=/global/kubeconfig\nktctl.path=custom-ktctl\n" {
		t.Fatalf("list output = %q", output.String())
	}
}

func TestLocalConfigDoesNotTreatGlobalSettingsAsWorkspace(t *testing.T) {
	home := t.TempDir()
	cwd := filepath.Join(home, "projects", "outside")
	t.Setenv("HOME", home)
	if err := os.MkdirAll(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	app := App{Output: &output, Error: &output, Cwd: cwd, Version: "test"}
	if code := app.Run([]string{"config", "--global", "ktctl.path", "global-ktctl"}); code != 0 {
		t.Fatalf("global set exit code = %d: %s", code, output.String())
	}
	if err := os.WriteFile(filepath.Join(home, ".conven", "conven.yaml"), []byte("version: 2\n"), 0600); err != nil {
		t.Fatal(err)
	}

	for _, arguments := range [][]string{
		{"config", "--list"},
		{"config", "ktctl.path", "local-ktctl"},
		{"config", "--unset", "ktctl.path"},
	} {
		output.Reset()
		if code := app.Run(arguments); code != 1 {
			t.Fatalf("%v exit code = %d: %s", arguments, code, output.String())
		}
		if !strings.Contains(output.String(), "not a Conven workspace") {
			t.Fatalf("%v output = %q", arguments, output.String())
		}
	}

	output.Reset()
	if code := app.Run([]string{"config", "--global", "ktctl.path"}); code != 0 {
		t.Fatalf("global get exit code = %d: %s", code, output.String())
	}
	if output.String() != "global-ktctl\n" {
		t.Fatalf("global value = %q, want unchanged value", output.String())
	}
}

func TestLocalConfigThroughHomeAliasDoesNotChangeGlobalSettings(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "real-home")
	alias := filepath.Join(root, "home-alias")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(home, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	var output bytes.Buffer
	app := App{Output: &output, Error: &output, Cwd: alias, Version: "test"}
	if code := app.Run([]string{"config", "--global", "ktctl.path", "global-ktctl"}); code != 0 {
		t.Fatalf("global set exit code = %d: %s", code, output.String())
	}
	if err := os.WriteFile(filepath.Join(home, ".conven", "conven.yaml"), []byte("version: 2\n"), 0600); err != nil {
		t.Fatal(err)
	}

	output.Reset()
	if code := app.Run([]string{"config", "ktctl.path", "local-ktctl"}); code != 1 {
		t.Fatalf("local set exit code = %d: %s", code, output.String())
	}
	if !strings.Contains(output.String(), "not a Conven workspace") {
		t.Fatalf("local set output = %q", output.String())
	}
	output.Reset()
	if code := app.Run([]string{"config", "--global", "ktctl.path"}); code != 0 {
		t.Fatalf("global get exit code = %d: %s", code, output.String())
	}
	if output.String() != "global-ktctl\n" {
		t.Fatalf("global value = %q, want unchanged value", output.String())
	}
}

func TestWorkspaceCommandsFailOutsideDotConvenBoundary(t *testing.T) {
	for _, arguments := range [][]string{
		{"services", "--start", "api"},
		{"services", "--restart"},
		{"services", "--status"},
		{"services", "--stop", "--all"},
		{"services", "--stop-all"},
		{"services", "--logs"},
		{"services", "--dashboard"},
		{"services", "--cleanup"},
		{"doctor"},
		{"services", "--list"},
		{"services", "--registry"},
		{"policy", "--edit"},
		{"policy", "--reset"},
		{"policy", "--import", filepath.Join(t.TempDir(), "policy.yaml")},
		{"config", "--list"},
	} {
		t.Run(strings.Join(arguments, "_"), func(t *testing.T) {
			var output bytes.Buffer
			app := App{Output: &output, Error: &output, Cwd: t.TempDir(), Version: "test"}
			if code := app.Run(arguments); code != 1 {
				t.Fatalf("exit code = %d: %s", code, output.String())
			}
			if !strings.Contains(output.String(), "not a Conven workspace") {
				t.Fatalf("output = %q", output.String())
			}
		})
	}
}

func writeCLIServiceRepository(t *testing.T, workspace string, name string) {
	t.Helper()
	repository := filepath.Join(workspace, name)
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repository, "go"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "go", "go.mod"), []byte("module example.com/"+name+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "go", "main.go"), []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func assertFileContents(t *testing.T, path string, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}
