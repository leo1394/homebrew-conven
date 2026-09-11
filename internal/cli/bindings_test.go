package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServiceBindingCommands(t *testing.T) {
	workspace := listenerCommandWorkspace(t)
	var output, errors bytes.Buffer
	app := App{Output: &output, Error: &errors, Cwd: workspace}
	for _, action := range []string{"--disable-binding", "--enable-binding"} {
		if code := app.Run([]string{"services", action, "pigeonRpc"}); code != 0 {
			t.Fatalf("%s: %d: %s", action, code, errors.String())
		}
		output.Reset()
		if code := app.Run([]string{"__completion", "candidates", "disabled-bindings"}); code != 0 {
			t.Fatalf("completion: %d: %s", code, errors.String())
		}
		if strings.Contains(output.String(), "pigeonRpc") != (action == "--disable-binding") {
			t.Fatalf("completion after %s: %s", action, output.String())
		}
	}
	path := filepath.Join(workspace, ".conven", "conven.yaml")
	before, _ := os.ReadFile(path)
	for _, args := range [][]string{{"services", "--disable-binding"}, {"services", "--disable-binding", "okRpc", "bad.name"}, {"services", "--enable-binding", "pigeonRpc", "--disable-binding"}} {
		if code := app.Run(args); code == 0 {
			t.Fatalf("accepted invalid args: %v", args)
		}
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("invalid request modified manifest")
	}
}
