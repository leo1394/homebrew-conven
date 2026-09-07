package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectLocalRPCModuleReplacementIsReadOnlyAndFailClosed(t *testing.T) {
	for _, test := range []struct {
		name string
		rules string
		want string
		errorPart string
	}{
		{name: "local wildcard", rules: "replace github.com/tal-tech/go-zero => ./fork\n", want: "./fork"},
		{name: "quoted local path", rules: "replace github.com/tal-tech/go-zero => \"./local fork\"\n", want: "./local fork"},
		{name: "sole inactive version", rules: "replace github.com/tal-tech/go-zero v1.0.0 => ./decoy\n", errorPart: "version-specific"},
		{name: "sole active version also requires graph resolution", rules: "replace github.com/tal-tech/go-zero v1.5.5 => ./fork\n", errorPart: "version-specific"},
		{name: "remote version overrides local wildcard", rules: "replace (\n github.com/tal-tech/go-zero => ./fork\n github.com/tal-tech/go-zero v1.5.5 => example.test/other v1.5.5\n)\n", errorPart: "version-specific"},
		{name: "remote wildcard", rules: "replace github.com/tal-tech/go-zero => example.test/other v1.5.5\n", errorPart: "local go-zero"},
		{name: "unrelated module", rules: "replace github.com/zeromicro/go-zero => ./fork\n", errorPart: "local go-zero"},
	} {
		t.Run(test.name, func(t *testing.T) {
			moduleFile := filepath.Join(t.TempDir(), "go.mod")
			original := "module example.test/service\n\nrequire github.com/tal-tech/go-zero v1.5.5\n\n" + test.rules
			if err := os.WriteFile(moduleFile, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := inspectLocalRPCModuleReplacement(moduleFile, "github.com/tal-tech/go-zero")
			if test.errorPart == "" {
				if err != nil || got != test.want {
					t.Fatalf("replacement = %q, %v; want %q", got, err, test.want)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.errorPart) {
				t.Fatalf("expected %q error, got %v", test.errorPart, err)
			}
			after, err := os.ReadFile(moduleFile)
			if err != nil || string(after) != original {
				t.Fatalf("read-only replacement inspection rewrote go.mod: %v", err)
			}
		})
	}
}
