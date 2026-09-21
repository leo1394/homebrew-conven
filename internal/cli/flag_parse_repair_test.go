package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leo1394/homebrew-conven/internal/config"
)

func TestFlagParseRepairAnswer(t *testing.T) {
	for _, answer := range []string{"y", "Y\n", " y \n"} { if !flagParseRepairApproved(answer) { t.Fatalf("rejected %q", answer) } }
	for _, answer := range []string{"", "\n", "n", "yes", "unknown"} { if flagParseRepairApproved(answer) { t.Fatalf("accepted %q", answer) } }
}

func TestUpdateOffersFlagParseRepair(t *testing.T) {
	for _, action := range []string{"--update", "--registry"} {
		for _, answer := range []string{"yes", "no", "noninteractive"} {
			t.Run(action+"/"+answer, func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				root := t.TempDir()
				var output bytes.Buffer
				app := App{Cwd: root, Output: &output, Error: &output}
				if code := app.Run([]string{"init"}); code != 0 { t.Fatalf("init: %s", &output) }
				writeCLIServiceRepository(t, root, "new-api")
				path := filepath.Join(root, "new-api", "go", "main.go")
				source := "package main\nimport \"flag\"\ntype Config struct { Server rest.RestConf }\nfunc main() {\n dir := flag.String(\"f\", \"../resources\", \"config\")\n start(*dir)\n}\n"
				if err := os.WriteFile(path, []byte(source), 0600); err != nil { t.Fatal(err) }
				if err := os.WriteFile(filepath.Join(root, "new-api", "go", "go.mod"), []byte("module example.test/new-api\nrequire github.com/tal-tech/go-zero v1.0.0\n"), 0600); err != nil { t.Fatal(err) }
				manifestPath := filepath.Join(root, ".conven", "conven.yaml")
				before, _ := os.ReadFile(manifestPath)
				calls := 0
				if answer != "noninteractive" { app.FlagParseRepairConfirmer = func(ctx context.Context, repair *config.FlagParseRepair) (bool, error) { calls++; return answer == "yes", nil } }
				app.Run([]string{"services", action})
				data, _ := os.ReadFile(path)
				if answer == "yes" {
					if calls != 1 || !strings.Contains(string(data), "flag.Parse()") { t.Fatalf("repair missing: calls=%d output=%s", calls, &output) }
					if strings.Contains(output.String(), "does not call flag.Parse()") { t.Fatalf("scan not resumed: %s", &output) }
				} else {
					assertFileContents(t, path, source)
					assertFileContents(t, manifestPath, string(before))
				}
			})
		}
	}
}
