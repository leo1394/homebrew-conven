package cli

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	loomruntime "github.com/leo1394/homebrew-loom/internal/runtime"
)

func TestVersion(t *testing.T) {
	var output bytes.Buffer
	app := App{Output: &output, Error: &output, Version: "test-version"}
	if code := app.Run([]string{"--version"}); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if output.String() != "loom test-version\n" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestLegacyServiceCommandsWereRemoved(t *testing.T) {
	for _, command := range []string{"looming", "discover", "start", "restart", "status", "stop", "logs", "list", "serivces"} {
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
			if !strings.Contains(errorOutput.String(), `unknown command "`+command+`"`) {
				t.Fatalf("stderr = %q", errorOutput.String())
			}
		})
	}
}

func TestRootHelpExposesOnlyServicesCommandForServiceOperations(t *testing.T) {
	var output bytes.Buffer
	var errorOutput bytes.Buffer
	app := App{Output: &output, Error: &errorOutput, Version: "test-version"}
	if code := app.Run([]string{"help"}); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(output.String(), "loom services ACTION") {
		t.Fatalf("root help does not expose the services command: %q", output.String())
	}
	if !strings.Contains(output.String(), "loom policy ACTION") {
		t.Fatalf("root help does not expose the policy command: %q", output.String())
	}
	for _, removed := range []string{
		"loom discover ",
		"loom start ",
		"loom restart ",
		"loom status\n",
		"loom stop ",
		"loom logs ",
		"loom list\n",
	} {
		if strings.Contains(output.String(), removed) {
			t.Fatalf("root help still exposes legacy command %q: %q", removed, output.String())
		}
	}
	if errorOutput.Len() != 0 {
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
		for _, action := range []string{"--list", "--registry", "--start", "--restart", "--status", "--stop", "--stop-all", "--logs"} {
			if !strings.Contains(output.String(), action) {
				t.Fatalf("%v help is missing %s: %q", arguments, action, output.String())
			}
		}
		if errorOutput.Len() != 0 {
			t.Fatalf("%v stderr = %q", arguments, errorOutput.String())
		}
	}
}

func TestServiceActionHelpUsesStdout(t *testing.T) {
	for _, action := range []string{"--list", "--registry", "--start", "--restart", "--status", "--stop", "--stop-all", "--logs"} {
		t.Run(action, func(t *testing.T) {
			var output bytes.Buffer
			var errorOutput bytes.Buffer
			app := App{Output: &output, Error: &errorOutput, Cwd: t.TempDir(), Version: "test-version"}

			if code := app.Run([]string{"services", action, "--help"}); code != 0 {
				t.Fatalf("exit code = %d", code)
			}
			if !strings.Contains(output.String(), "loom services "+action) {
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
			if strings.Contains(errorOutput.String(), "not a loom workspace") {
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
		if !strings.Contains(output.String(), "loom policy") {
			t.Fatalf("%v stdout = %q", arguments, output.String())
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
		if !strings.Contains(output.String(), "loom policy --edit") || strings.Contains(output.String(), "not a loom workspace") {
			t.Fatalf("%v output = %q", arguments, output.String())
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
		{name: "list", arguments: []string{"services", "--list"}},
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
			for _, removed := range []string{"-workspace", "-config"} {
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
				if !strings.Contains(errorOutput.String(), "flag provided but not defined: -"+removed) {
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
			if !strings.Contains(output.String(), "-tail") {
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
			if !strings.Contains(errorOutput.String(), "flag provided but not defined: -follow") {
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

func TestLogsTailNonTerminalOutputContainsOnlyPrefixedLogs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LOOM_STATE_HOME", t.TempDir())
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".loom"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspace, "api"), 0700); err != nil {
		t.Fatal(err)
	}
	manifest := `version: 1
workspace:
  name: non-terminal-tail
services:
  api:
    path: api
    runner:
      run: [sleep, "600"]
`
	if err := os.WriteFile(filepath.Join(workspace, ".loom", "loom.yaml"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "api.log")
	if err := os.WriteFile(logPath, []byte("ready\n"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := loomruntime.NewStore(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(&loomruntime.Session{
		Environment: "dev",
		Services:    []loomruntime.ServiceProcess{{Name: "api", LogPath: logPath}},
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

func TestEnvironmentShortcutsMatchEnvFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := environmentShortcutWorkspace(t)
	store, err := loomruntime.NewStore(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		arguments []string
		want      string
	}{
		{name: "start dev", arguments: []string{"services", "--start", "--dry-run", "--dev", "api"}, want: "Environment: dev\n"},
		{name: "start test", arguments: []string{"services", "--start", "--dry-run", "--test", "api"}, want: "Environment: test\n"},
		{name: "start env test", arguments: []string{"services", "--start", "--dry-run", "--env", "test", "api"}, want: "Environment: test\n"},
		{name: "start matching test", arguments: []string{"services", "--start", "--dry-run", "--test", "--env", "test", "api"}, want: "Environment: test\n"},
		{name: "start repeated matching test", arguments: []string{"services", "--start", "--dry-run", "--env", "test", "--test", "--env=test", "api"}, want: "Environment: test\n"},
		{name: "start custom", arguments: []string{"services", "--start", "--dry-run", "--env", "stage", "api"}, want: "Environment: stage\n"},
		{name: "doctor dev", arguments: []string{"doctor", "--dev"}, want: "Environment: dev\n"},
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
			if strings.Contains(output.String(), "not a loom workspace") {
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
		"loom services --stop-all",
		"bypass identity checks",
		"--force is destructive",
		"loom services --status",
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
	t.Setenv("LOOM_STATE_HOME", t.TempDir())
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
	if !strings.Contains(outputs[0], "No loom session found.") {
		t.Fatalf("stop-all output = %q", outputs[0])
	}
}

func TestStopAllShortcutAcceptsForce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LOOM_STATE_HOME", t.TempDir())
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
			if strings.Contains(output.String(), "not a loom workspace") {
				t.Fatalf("workspace lookup happened before argument validation: %q", output.String())
			}
		})
	}
}

func TestServicesListStatusRestartAndStopRoutes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LOOM_STATE_HOME", t.TempDir())
	workspace := environmentShortcutWorkspace(t)
	store, err := loomruntime.NewStore(workspace)
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
		{name: "status", arguments: []string{"services", "--status"}, code: 0, message: "No loom session found."},
		{name: "restart", arguments: []string{"services", "--restart"}, code: 1, message: "no running loom session found"},
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

func TestStartWithoutServicesRejectsNonTerminal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	serviceDir := filepath.Join(workspace, "user-svc")
	if err := os.Mkdir(serviceDir, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := `version: 1
workspace:
  name: test
services:
  user-svc:
    path: user-svc
    runner:
      run: [sh, -c, "while :; do sleep 1; done"]
`
	if err := os.Mkdir(filepath.Join(workspace, ".loom"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".loom", "loom.yaml"), []byte(manifest), 0600); err != nil {
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

func TestCompletions(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			completion, err := Completion(shell)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(completion, "loom") {
				t.Fatalf("completion for %s is empty", shell)
			}
			for _, command := range []string{"init", "services", "config", "policy", "doctor"} {
				if !strings.Contains(completion, command) {
					t.Fatalf("completion for %s is missing %s", shell, command)
				}
			}
			for _, action := range []string{"--list", "--registry", "--status", "--logs", "--start", "--restart", "--stop", "--stop-all"} {
				marker := action
				if shell == "fish" {
					marker = "-l " + strings.TrimPrefix(action, "--")
				}
				if !strings.Contains(completion, marker) {
					t.Fatalf("completion for %s is missing %s", shell, action)
				}
			}
			if strings.Contains(completion, "looming") {
				t.Fatalf("completion for %s still exposes looming", shell)
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
		`compgen -W "init services config policy doctor help version"`,
		`if [ "$subcommand" = "services" ]`,
		`--list|--status)`,
		`--registry)`,
		`options="--prune --help"`,
		`--logs)`,
		`options="--tail --help"`,
		`--start)`,
		`options="--env --dev --test --kubeconfig --context --namespace --tail --dry-run --skip-build --skip-verify --help"`,
		`--restart)`,
		`options="--tail --skip-build --skip-verify --help"`,
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
		`case $words[2] in`,
		`'services:manage workspace services'`,
		`'policy:edit, import, or rebuild the workspace policy manifest'`,
		`case $action in`,
		`--list|--status)`,
		`--registry)`,
		`--logs)`,
		`--start)`,
		`--restart)`,
		`--stop)`,
		`--stop-all)`,
		`--dev[use the dev environment profile]`,
		`--test[use the test environment profile]`,
		`--all[stop every service and release the workspace connection]`,
		`--force[bypass identity checks and recover saved process groups]`,
		`--tail[tail aggregated logs]`,
		`--prune[remove missing direct-child repository services]`,
		`--reset[destructively reset the manifest to scanned facts]`,
		`--import[import a local YAML file as the entire manifest]`,
	} {
		if !strings.Contains(zsh, expected) {
			t.Fatalf("zsh completion is missing %q", expected)
		}
	}
	for _, removed := range []string{"'discover:", "'start:", "'restart:", "'status:", "'stop:", "'logs:", "'list:"} {
		if strings.Contains(zsh, removed) {
			t.Fatalf("zsh completion still exposes legacy top-level command %q", removed)
		}
	}

	fish, err := Completion("fish")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`function __loom_using_subcommand`,
		`function __loom_services_action`,
		`function __loom_services_without_action`,
		`function __loom_policy_without_action`,
		`function __loom_policy_action`,
		`function __loom_policy_action_without_edit`,
		`function __loom_policy_import_without_source`,
		`__fish_use_subcommand' -a services`,
		`__fish_use_subcommand' -a policy`,
		`__loom_services_without_action' -l list`,
		`__loom_services_without_action' -l registry`,
		`__loom_services_without_action' -l stop-all`,
		`__loom_services_action --start' -l env`,
		`__loom_services_action --start' -l dev`,
		`__loom_services_action --start' -l test`,
		`__loom_services_action --start' -l tail`,
		`__loom_services_action --start' -l dry-run`,
		`__loom_services_action --restart' -l tail`,
		`__loom_services_action --logs' -l tail`,
		`__loom_using_subcommand config' -l global`,
		`__loom_services_action --stop' -l all`,
		`__loom_services_action --stop' -l force`,
		`__loom_services_action --stop-all' -l force`,
		`__loom_services_action --registry' -l prune`,
		`__loom_using_subcommand policy; and __loom_policy_without_action' -l edit`,
		`__loom_using_subcommand policy; and __loom_policy_without_action' -l import`,
		`__loom_using_subcommand policy; and __loom_policy_without_action' -l reset`,
		`__loom_policy_action_without_edit --import' -l edit`,
		`__loom_policy_import_without_source' -F`,
	} {
		if !strings.Contains(fish, expected) {
			t.Fatalf("fish completion is missing %q", expected)
		}
	}
	for _, removed := range []string{"-a discover ", "-a start ", "-a restart ", "-a status ", "-a stop ", "-a logs ", "-a list "} {
		if strings.Contains(fish, removed) {
			t.Fatalf("fish completion still exposes legacy top-level command marker %q", removed)
		}
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
		{name: "action", words: "loom policy --r", want: "--reset\n"},
		{name: "import action", words: "loom policy --i", want: "--import\n"},
		{name: "after action", words: "loom policy --edit --", want: "--help\n"},
		{name: "import options", words: "loom policy --import --", want: "--edit\n--help\n"},
		{name: "after import source", words: "loom policy --import README.md --", want: "--edit\n--help\n"},
		{name: "after import edit", words: "loom policy --import README.md --edit --", want: "--help\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			script := completion + "\nCOMP_WORDS=(" + test.words + ")\nCOMP_CWORD=$((${#COMP_WORDS[@]} - 1))\n_loom\nprintf '%s\\n' \"${COMPREPLY[@]}\"\n"
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
	script := completion + "\nCOMP_WORDS=(loom policy --import candidate)\nCOMP_CWORD=3\n_loom\nprintf '%s\\n' \"${COMPREPLY[@]}\"\n"
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
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".loom"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspace, "api"), 0700); err != nil {
		t.Fatal(err)
	}
	manifest := `version: 1
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
	if err := os.WriteFile(filepath.Join(workspace, ".loom", "loom.yaml"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	return workspace
}

func TestInitCreatesEmbeddedManifestAndRuntimeIgnoreWithoutOverwriting(t *testing.T) {
	workspace := t.TempDir()
	boundary := filepath.Join(workspace, ".loom")
	if err := os.Mkdir(boundary, 0700); err != nil {
		t.Fatal(err)
	}
	ignorePath := filepath.Join(boundary, ".gitignore")
	if err := os.WriteFile(ignorePath, []byte("custom-rule\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	app := App{Output: &output, Error: &output, Cwd: workspace, Version: "test"}
	if code := app.Run([]string{"init"}); code != 0 {
		t.Fatalf("exit code = %d: %s", code, output.String())
	}
	path := filepath.Join(workspace, ".loom", "loom.yaml")
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
	if err := os.WriteFile(path, []byte("custom\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if code := app.Run([]string{"init"}); code != 0 {
		t.Fatalf("reinitialize exit code = %d: %s", code, output.String())
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
}

func TestInitAndRegistryRecognizeDirectChildServices(t *testing.T) {
	workspace := t.TempDir()
	writeCLIServiceRepository(t, workspace, "alpha-service")
	var output bytes.Buffer
	app := App{Output: &output, Error: &output, Cwd: workspace, Version: "test"}
	if code := app.Run([]string{"init"}); code != 0 {
		t.Fatalf("init exit code = %d: %s", code, output.String())
	}
	if !strings.Contains(output.String(), "Discovered supported services: alpha-service") {
		t.Fatalf("init output = %q", output.String())
	}

	writeCLIServiceRepository(t, workspace, "beta-service")
	output.Reset()
	app.Cwd = filepath.Join(workspace, "alpha-service", "go")
	if code := app.Run([]string{"services", "--registry"}); code != 0 {
		t.Fatalf("registry exit code = %d: %s", code, output.String())
	}
	for _, expected := range []string{
		"Discovered supported services: alpha-service, beta-service",
		"Added services: beta-service",
		"Updated Loom manifest:",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("registry output is missing %q: %q", expected, output.String())
		}
	}
	data, err := os.ReadFile(filepath.Join(workspace, ".loom", "loom.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "alpha-service:") || !strings.Contains(string(data), "beta-service:") {
		t.Fatalf("manifest = %s", data)
	}
}

func TestPolicyEditUsesInjectedEditorAndPublishesValidatedManifest(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".loom", "loom.yaml")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0700); err != nil {
		t.Fatal(err)
	}
	original := `version: 1
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
		Cwd:     filepath.Join(workspace, ".loom"),
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
	if !called || !strings.Contains(output.String(), "Updated Loom policy manifest:") || errorOutput.Len() != 0 {
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
	manifestPath := filepath.Join(workspace, ".loom", "loom.yaml")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0700); err != nil {
		t.Fatal(err)
	}
	original := `version: 1
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
		"Discovered supported services: api-service",
		"Reset Loom policy manifest to scan baseline:",
		"Pre-reset manifest backup:",
		"re-declare policies, environments, ports, dependencies",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("output is missing %q: %q", expected, output.String())
		}
	}
	if errorOutput.Len() != 0 {
		t.Fatalf("stderr = %q", errorOutput.String())
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "custom-api") || !strings.Contains(string(data), "api-service") {
		t.Fatalf("manifest = %s", data)
	}
}

func TestPolicyImportReplacesManifestAndFeedsSubsequentServicesCommands(t *testing.T) {
	workspace := t.TempDir()
	manifestPath := filepath.Join(workspace, ".loom", "loom.yaml")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0700); err != nil {
		t.Fatal(err)
	}
	original := `version: 1
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
	imported := `version: 1
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
		"Replaced Loom policy manifest from imported file",
		"Pre-import manifest backup:",
		"without merging repository scan results",
		"loom doctor",
		"loom services --start --dry-run",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("output is missing %q: %q", expected, output.String())
		}
	}
	if errorOutput.Len() != 0 {
		t.Fatalf("stderr = %q", errorOutput.String())
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

func TestPolicyImportEditAcceptsFlagBeforeOrAfterSource(t *testing.T) {
	for _, editFirst := range []bool{false, true} {
		name := "edit-after-source"
		if editFirst {
			name = "edit-before-source"
		}
		t.Run(name, func(t *testing.T) {
			workspace := t.TempDir()
			manifestPath := filepath.Join(workspace, ".loom", "loom.yaml")
			if err := os.MkdirAll(filepath.Dir(manifestPath), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifestPath, []byte(`version: 1
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
			if err := os.WriteFile(importPath, []byte(`version: 1
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
		{[]string{"policy", "--import"}, 1, "requires exactly one YAML file"},
		{[]string{"policy", "--import", "one.yaml", "two.yaml"}, 1, "requires exactly one YAML file"},
		{[]string{"policy", "--import", "--unknown", "one.yaml"}, 2, "flag provided but not defined"},
		{[]string{"policy", "--import", "--reset", "one.yaml"}, 2, "flag provided but not defined"},
	} {
		var output bytes.Buffer
		app := App{Output: &output, Error: &output, Cwd: t.TempDir(), Version: "test"}
		if code := app.Run(test.arguments); code != test.code {
			t.Fatalf("%v exit code = %d, want %d: %s", test.arguments, code, test.code, output.String())
		}
		if !strings.Contains(output.String(), test.message) || strings.Contains(output.String(), "not a loom workspace") {
			t.Fatalf("%v output = %q", test.arguments, output.String())
		}
	}
}

func TestLaunchPolicyEditorUsesLoomEditorAndSupportsArguments(t *testing.T) {
	directory := t.TempDir()
	script := filepath.Join(directory, "editor.sh")
	capture := filepath.Join(directory, "capture")
	contents := "#!/bin/sh\nprintf '%s\\n%s\\n' \"$1\" \"$2\" > \"$EDITOR_CAPTURE\"\n"
	if err := os.WriteFile(script, []byte(contents), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR_CAPTURE", capture)
	t.Setenv("LOOM_EDITOR", script+" --wait")
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
	workspace := t.TempDir()
	writeCLIServiceRepository(t, workspace, "alpha-service")
	writeCLIServiceRepository(t, workspace, "beta-service")
	var output bytes.Buffer
	app := App{Output: &output, Error: &output, Cwd: workspace, Version: "test"}
	if code := app.Run([]string{"init"}); code != 0 {
		t.Fatalf("init exit code = %d: %s", code, output.String())
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
	data, err := os.ReadFile(filepath.Join(workspace, ".loom", "loom.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "alpha-service:") || !strings.Contains(string(data), "beta-service:") {
		t.Fatalf("manifest after registry --prune = %s", data)
	}
}

func TestRegistryRejectsArgumentsAndUnknownFlags(t *testing.T) {
	for _, test := range []struct {
		arguments []string
		code      int
		message   string
	}{
		{[]string{"services", "--registry", "service"}, 1, "does not accept service arguments"},
		{[]string{"services", "--registry", "--workspace", "/tmp/elsewhere"}, 2, "flag provided but not defined: -workspace"},
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
	if err := os.MkdirAll(filepath.Join(workspace, ".loom"), 0700); err != nil {
		t.Fatal(err)
	}
	manifest := `version: 1
workspace:
  name: missing
services:
  removed-service:
    path: removed-service
    runner:
      run: [removed-service]
`
	if err := os.WriteFile(filepath.Join(workspace, ".loom", "loom.yaml"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	app := App{Output: &output, Error: &output, Cwd: workspace, Version: "test"}
	if code := app.Run([]string{"services", "--registry"}); code != 0 {
		t.Fatalf("exit code = %d: %s", code, output.String())
	}
	if !strings.Contains(output.String(), "Manifest unchanged; missing repositories were kept.") {
		t.Fatalf("output = %q", output.String())
	}
	if strings.Contains(output.String(), "already matches") {
		t.Fatalf("output contradicts missing repository status: %q", output.String())
	}
	if !strings.Contains(output.String(), "loom services --registry --prune") || strings.Contains(output.String(), "loom discover") {
		t.Fatalf("output does not use the registry action: %q", output.String())
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
	if err := os.Mkdir(filepath.Join(workspace, ".loom"), 0700); err != nil {
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
	if err := os.WriteFile(filepath.Join(home, ".loom", "loom.yaml"), []byte("version: 1\n"), 0600); err != nil {
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
		if !strings.Contains(output.String(), "not a loom workspace") {
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
	if err := os.WriteFile(filepath.Join(home, ".loom", "loom.yaml"), []byte("version: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}

	output.Reset()
	if code := app.Run([]string{"config", "ktctl.path", "local-ktctl"}); code != 1 {
		t.Fatalf("local set exit code = %d: %s", code, output.String())
	}
	if !strings.Contains(output.String(), "not a loom workspace") {
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

func TestWorkspaceCommandsFailOutsideDotLoomBoundary(t *testing.T) {
	for _, arguments := range [][]string{
		{"services", "--start", "api"},
		{"services", "--restart"},
		{"services", "--status"},
		{"services", "--stop", "--all"},
		{"services", "--stop-all"},
		{"services", "--logs"},
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
			if !strings.Contains(output.String(), "not a loom workspace") {
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
