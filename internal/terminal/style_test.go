package terminal

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestStyleUsesANSIForTerminal(t *testing.T) {
	unsetEnvironment(t, "NO_COLOR")
	t.Setenv("TERM", "xterm-256color")
	file, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	style := newStyle(file, func(int) bool { return true })

	if got := style.Identifier("api-service"); got != boldCyan+"api-service"+reset {
		t.Fatalf("identifier style = %q", got)
	}
	if got := style.Failure("failed"); got != boldRed+"failed"+reset {
		t.Fatalf("failure style = %q", got)
	}
	if got := style.Identifiers([]string{"api", "worker"}, ", "); got != boldCyan+"api"+reset+", "+boldCyan+"worker"+reset {
		t.Fatalf("identifier list style = %q", got)
	}
}

func TestStyleDisablesANSIForNonFileWriter(t *testing.T) {
	style := newStyle(&bytes.Buffer{}, func(int) bool { return true })
	if got := style.Warning("warning"); got != "warning" {
		t.Fatalf("non-file warning style = %q", got)
	}
}

func TestStyleDisablesANSIForNonTerminalFile(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	style := newStyle(file, func(int) bool { return false })
	if got := style.Success("ready"); got != "ready" {
		t.Fatalf("non-terminal success style = %q", got)
	}
}

func TestStyleHonorsNoColorPresence(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	file, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	style := newStyle(file, func(int) bool { return true })
	if got := style.Failure("failed"); got != "failed" {
		t.Fatalf("NO_COLOR failure style = %q", got)
	}
}

func TestStyleDisablesANSIForDumbTerminal(t *testing.T) {
	unsetEnvironment(t, "NO_COLOR")
	t.Setenv("TERM", " DuMb ")
	file, err := os.CreateTemp(t.TempDir(), "terminal")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	style := newStyle(file, func(int) bool { return true })
	if got := style.Label("Workspace"); got != "Workspace" {
		t.Fatalf("dumb terminal label style = %q", got)
	}
}

func unsetEnvironment(t *testing.T, name string) {
	t.Helper()
	value, found := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if found {
			_ = os.Setenv(name, value)
		} else {
			_ = os.Unsetenv(name)
		}
	})
}

func TestStyleMethodsDoNotAlterPlainText(t *testing.T) {
	style := Style{}
	values := []string{
		style.Label("Workspace"),
		style.Identifier("api-service"),
		style.Warning("warning"),
		style.Failure("failed"),
		style.Success("ready"),
	}
	if got := strings.Join(values, "|"); got != "Workspace|api-service|warning|failed|ready" {
		t.Fatalf("plain styles = %q", got)
	}
}
