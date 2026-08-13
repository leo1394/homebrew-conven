package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestChineseLocaleRecognizesSimplifiedAndTraditionalForms(t *testing.T) {
	for _, locale := range []string{
		"zh",
		"zh_CN.UTF-8",
		"zh-TW",
		"zh_Hant_TW",
		"ZH_hk",
		"zh-MO@calendar=chinese",
	} {
		if !chineseLocale(locale) {
			t.Fatalf("locale %q was not recognized as Chinese", locale)
		}
	}
	for _, locale := range []string{"", "C", "POSIX", "en_US.UTF-8", "ja_JP", "fr-FR"} {
		if chineseLocale(locale) {
			t.Fatalf("locale %q was recognized as Chinese", locale)
		}
	}
}

func TestPreferredLocaleUsesDarwinSystemLanguageAndEnvironmentFallback(t *testing.T) {
	for _, test := range []struct {
		name       string
		goos       string
		environment map[string]string
		apple      string
		want       string
	}{
		{
			name:       "darwin system language overrides process locale",
			goos:       "darwin",
			environment: map[string]string{"LC_ALL": "en_US.UTF-8", "LC_MESSAGES": "zh_TW", "LANG": "zh_CN"},
			apple:      `( "zh-Hans-CN" )`,
			want:       "zh-Hans-CN",
		},
		{
			name:       "darwin missing system language uses process locale",
			goos:       "darwin",
			environment: map[string]string{"LC_ALL": "zh_CN.UTF-8"},
			want:       "zh_CN.UTF-8",
		},
		{
			name:       "lc messages",
			goos:       "linux",
			environment: map[string]string{"LC_MESSAGES": "zh_Hant_TW", "LANG": "en_US"},
			want:       "zh_Hant_TW",
		},
		{
			name:       "language list",
			goos:       "linux",
			environment: map[string]string{"LANGUAGE": "zh_TW:en", "LANG": "en_US"},
			want:       "zh_TW",
		},
		{
			name:       "darwin c locale uses system language",
			goos:       "darwin",
			environment: map[string]string{"LC_ALL": "C.UTF-8", "LANG": "en_US"},
			apple:      "(\n    \"zh-Hans-CN\",\n    \"en-CN\"\n)\n",
			want:       "zh-Hans-CN",
		},
		{
			name:       "linux c locale defaults to english selection",
			goos:       "linux",
			environment: map[string]string{"LC_ALL": "C.UTF-8", "LANG": "zh_CN"},
			want:       "C.UTF-8",
		},
		{
			name:       "darwin missing environment uses system language",
			goos:       "darwin",
			environment: map[string]string{},
			apple:      `( "zh-Hant-HK" )`,
			want:       "zh-Hant-HK",
		},
		{
			name:       "missing language defaults to english selection",
			goos:       "linux",
			environment: map[string]string{},
			want:       "",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			lookup := func(name string) (string, bool) {
				value, found := test.environment[name]
				return value, found
			}
			if got := preferredLocale(test.goos, lookup, func() string { return test.apple }); got != test.want {
				t.Fatalf("preferred locale = %q, want %q", got, test.want)
			}
		})
	}
}

func TestInitSelectsLocalizedPolicySpecificationWithoutOverwritingIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := t.TempDir()
	t.Setenv("LC_ALL", "zh_Hant_TW.UTF-8")
	initialSpecification := workspacePolicySpecification()
	var output bytes.Buffer
	app := App{Output: &output, Error: &output, Cwd: workspace, Version: "test"}
	if code := app.Run([]string{"init"}); code != 0 {
		t.Fatalf("initial init exit code = %d: %s", code, output.String())
	}
	specificationPath := filepath.Join(workspace, "CONVEN-WORKSPACE-POLICY-GENERATOR-AI-SPEC.md")
	assertFileBytes(t, specificationPath, initialSpecification)
	if _, err := os.Lstat(filepath.Join(workspace, "CONVEN-WORKSPACE-POLICY-GENERATOR-AI-SPEC-EN.md")); !os.IsNotExist(err) {
		t.Fatalf("init exposed the embedded English source template: %v", err)
	}

	t.Setenv("LC_ALL", "en_US.UTF-8")
	output.Reset()
	if code := app.Run([]string{"init"}); code != 0 {
		t.Fatalf("re-init exit code = %d: %s", code, output.String())
	}
	assertFileBytes(t, specificationPath, initialSpecification)

	if err := os.Remove(specificationPath); err != nil {
		t.Fatal(err)
	}
	replacementSpecification := workspacePolicySpecification()
	output.Reset()
	if code := app.Run([]string{"init"}); code != 0 {
		t.Fatalf("repair init exit code = %d: %s", code, output.String())
	}
	assertFileBytes(t, specificationPath, replacementSpecification)
}

func assertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s does not match the selected embedded template", path)
	}
}
