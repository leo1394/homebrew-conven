package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leo1394/homebrew-conven/examples"
)

func TestInitWorkspaceCreatesManifestAndDoesNotOverwrite(t *testing.T) {
	workspace := t.TempDir()
	template := []byte("version: 1\n")
	path, created, err := InitWorkspace(workspace, template)
	if err != nil {
		t.Fatal(err)
	}
	if !created || path != filepath.Join(workspace, ".conven", "conven.yaml") {
		t.Fatalf("InitWorkspace = (%q, %v)", path, created)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(template) {
		t.Fatalf("manifest = %q", data)
	}
	gitignore := filepath.Join(workspace, ".conven", ".gitignore")
	ignored, err := os.ReadFile(gitignore)
	if err != nil {
		t.Fatal(err)
	}
	if string(ignored) != "/runtime/\n" {
		t.Fatalf("initial gitignore = %q", ignored)
	}
	if err := os.WriteFile(path, []byte("custom\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gitignore, []byte("custom-rule"), 0600); err != nil {
		t.Fatal(err)
	}
	_, created, err = InitWorkspace(workspace, template)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("reinitialization overwrote the manifest")
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "custom\n" {
		t.Fatalf("reinitialized manifest = %q", data)
	}
	ignored, err = os.ReadFile(gitignore)
	if err != nil {
		t.Fatal(err)
	}
	if string(ignored) != "custom-rule\n/runtime/\n" {
		t.Fatalf("merged gitignore = %q", ignored)
	}
	_, created, err = InitWorkspace(workspace, template)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("second reinitialization overwrote the manifest")
	}
	ignored, err = os.ReadFile(gitignore)
	if err != nil {
		t.Fatal(err)
	}
	if string(ignored) != "custom-rule\n/runtime/\n" || strings.Count(string(ignored), "/runtime/\n") != 1 {
		t.Fatalf("repeated initialization changed gitignore: %q", ignored)
	}
}

func TestInitWorkspaceCreatesAndPreservesWorkspaceFiles(t *testing.T) {
	workspace := t.TempDir()
	result, err := InitWorkspaceDetailsWithPolicySpecification(workspace, []byte("version: 1\n"), examples.WorkspacePolicyGeneratorAISpec)
	if err != nil {
		t.Fatal(err)
	}
	workspaceFiles := examples.WorkspaceFiles()
	if len(result.Files) != len(workspaceFiles) {
		t.Fatalf("initialized file results = %#v", result.Files)
	}
	for index, file := range workspaceFiles {
		if result.Files[index].Name != file.Name || !result.Files[index].Created {
			t.Fatalf("initialized file result %d = %#v", index, result.Files[index])
		}
		path := filepath.Join(workspace, file.Name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read initialized workspace file %q: %v", file.Name, err)
		}
		if string(data) != string(file.Data) {
			t.Fatalf("initialized workspace file %q does not match its embedded template", file.Name)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0644 {
			t.Fatalf("initialized workspace file %q mode = %04o, want 0644", file.Name, info.Mode().Perm())
		}
	}

	preserved := make(map[string]string)
	for _, file := range workspaceFiles {
		value := "custom " + file.Name + "\n"
		preserved[file.Name] = value
		if err := os.WriteFile(filepath.Join(workspace, file.Name), []byte(value), 0644); err != nil {
			t.Fatal(err)
		}
	}
	missing := ".conven/catalog.yaml"
	if err := os.Remove(filepath.Join(workspace, missing)); err != nil {
		t.Fatal(err)
	}
	result, err = InitWorkspaceDetailsWithPolicySpecification(workspace, []byte("replacement\n"), examples.WorkspacePolicyGeneratorAISpec)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != len(workspaceFiles) {
		t.Fatalf("reinitialized file results = %#v", result.Files)
	}
	for index, file := range workspaceFiles {
		wantCreated := file.Name == missing
		if result.Files[index].Name != file.Name || result.Files[index].Created != wantCreated {
			t.Fatalf("reinitialized file result %d = %#v, want name %q created %v", index, result.Files[index], file.Name, wantCreated)
		}
		data, err := os.ReadFile(filepath.Join(workspace, file.Name))
		if err != nil {
			t.Fatal(err)
		}
		want := preserved[file.Name]
		if file.Name == missing {
			want = string(file.Data)
		}
		if string(data) != want {
			t.Fatalf("reinitialized workspace file %q = %q, want %q", file.Name, data, want)
		}
	}
}

func TestInitWorkspaceFilesContainDocumentedCatalogHeaders(t *testing.T) {
	for _, expected := range []string{
		"version: 1",
		"services: []",
		"repository: catalog-api",
		"rpcBinding: catalogRpc",
		"disabledRpcBindings: []",
	} {
		if !strings.Contains(string(examples.CatalogYAML), expected) {
			t.Fatalf("embedded catalog.yaml is missing %q", expected)
		}
	}
	for _, expected := range []string{
		"spec: conven-workspace-policy-generator",
		"pluginInvocation: \"conven plugins --run [NAME] [plugin-args...]\"",
		"repository: \"https://github.com/leo1394/homebrew-conven\"",
		"# Conven 工作区 Policy 生成器：AI 实现规范",
		"go-zero-apollo-consul-v1",
		".conven/catalog.yaml",
		"rpcBinding",
		"disabledRpcBindings",
		"conven plugins --run [NAME]",
		"--output [FILE]",
		"--disable-bindings",
	} {
		if !strings.Contains(string(examples.WorkspacePolicyGeneratorAISpec), expected) {
			t.Fatalf("embedded AI specification is missing %q", expected)
		}
	}
	for _, expected := range []string{
		"language: en",
		"pluginInvocation: \"conven plugins --run [NAME] [plugin-args...]\"",
		"repository: \"https://github.com/leo1394/homebrew-conven\"",
		"# Conven Workspace Policy Generator: AI Implementation Specification",
		"go-zero-apollo-consul-v1",
		".conven/catalog.yaml",
		"rpcBinding",
		"disabledRpcBindings",
		"conven plugins --run [NAME]",
		"--output [FILE]",
		"--disable-bindings",
	} {
		if !strings.Contains(string(examples.WorkspacePolicyGeneratorAISpecEnglish), expected) {
			t.Fatalf("embedded English AI specification is missing %q", expected)
		}
	}
	for _, expected := range []string{
		"# Conven Workspace Quick Start",
		"conven-generator.json",
		"conven plugins --run --output",
		"conven policy --import --edit",
	} {
		if !strings.Contains(string(examples.WorkspaceREADME), expected) {
			t.Fatalf("embedded workspace README is missing %q", expected)
		}
	}
}

func TestWorkspacePolicySpecificationsHaveNoReferenceImplementationTraces(t *testing.T) {
	for _, forbidden := range []string{
		"generate-apollo-consul.py",
		"referenceImplementation:",
		"currentInvocation:",
		"SERVICE_PRESETS",
	} {
		if strings.Contains(string(examples.WorkspacePolicyGeneratorAISpec), forbidden) {
			t.Fatalf("embedded Chinese AI specification contains reference implementation trace %q", forbidden)
		}
		if strings.Contains(string(examples.WorkspacePolicyGeneratorAISpecEnglish), forbidden) {
			t.Fatalf("embedded English AI specification contains reference implementation trace %q", forbidden)
		}
	}
	for _, forbidden := range []string{
		"下一版",
		"参考实现",
		"相较参考实现",
		"参考算法",
		"参考实现的已知偏差",
		"旧版 Conven",
	} {
		if strings.Contains(string(examples.WorkspacePolicyGeneratorAISpec), forbidden) {
			t.Fatalf("embedded Chinese AI specification contains reference implementation trace %q", forbidden)
		}
	}
	for _, forbidden := range []string{
		"next-generation",
		"reference implementation",
		"Compared with the reference implementation",
		"reference algorithm",
		"Recommended compatible invocations",
		"Known Deviations of the Reference Implementation",
		"Required by older Conven",
	} {
		if strings.Contains(string(examples.WorkspacePolicyGeneratorAISpecEnglish), forbidden) {
			t.Fatalf("embedded English AI specification contains reference implementation trace %q", forbidden)
		}
	}
}

func TestInitWorkspaceRejectsUnsafeWorkspaceFiles(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(t *testing.T, path string) string
		want    string
	}{
		{
			name: "symbolic link",
			prepare: func(t *testing.T, path string) string {
				target := filepath.Join(t.TempDir(), "target")
				if err := os.WriteFile(target, []byte("keep\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
				return target
			},
			want: "symbolic links are not allowed",
		},
		{
			name: "directory",
			prepare: func(t *testing.T, path string) string {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				return ""
			},
			want: "must be a regular file",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			boundary := filepath.Join(workspace, ".conven")
			if err := os.Mkdir(boundary, 0700); err != nil {
				t.Fatal(err)
			}
			unsafePath := filepath.Join(boundary, "catalog.yaml")
			target := test.prepare(t, unsafePath)
			_, _, err := InitWorkspace(workspace, []byte("version: 1\n"))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if target != "" {
				data, readErr := os.ReadFile(target)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if string(data) != "keep\n" {
					t.Fatalf("workspace symlink target changed: %q", data)
				}
			}
		})
	}
}

func TestInitWorkspacePreservesExistingRuntimeIgnoreRule(t *testing.T) {
	workspace := t.TempDir()
	boundary := filepath.Join(workspace, ".conven")
	if err := os.Mkdir(boundary, 0700); err != nil {
		t.Fatal(err)
	}
	gitignore := filepath.Join(boundary, ".gitignore")
	want := "custom-rule\n/runtime/\nother-rule\n"
	if err := os.WriteFile(gitignore, []byte(want), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := InitWorkspace(workspace, []byte("version: 1\n")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(gitignore)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("gitignore = %q, want unchanged %q", data, want)
	}
}

func TestInitWorkspaceRejectsGitignoreSymlink(t *testing.T) {
	workspace := t.TempDir()
	boundary := filepath.Join(workspace, ".conven")
	if err := os.Mkdir(boundary, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "gitignore")
	if err := os.WriteFile(target, []byte("keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(boundary, ".gitignore")); err != nil {
		t.Fatal(err)
	}
	_, _, err := InitWorkspace(workspace, []byte("version: 1\n"))
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("error = %v, want gitignore symlink rejection", err)
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "keep\n" {
		t.Fatalf("gitignore symlink target was changed: %q", data)
	}
}

func TestInitWorkspaceRejectsReadableManifestSymlink(t *testing.T) {
	workspace := t.TempDir()
	boundary := filepath.Join(workspace, ".conven")
	if err := os.Mkdir(boundary, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "conven.yaml")
	if err := os.WriteFile(target, []byte("keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(boundary, "conven.yaml")
	if err := os.Symlink(target, manifest); err != nil {
		t.Fatal(err)
	}

	_, _, err := InitWorkspace(workspace, []byte("replacement\n"))
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("error = %v", err)
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "keep\n" {
		t.Fatalf("manifest symlink target changed: %q", data)
	}
}

func TestInitWorkspaceRejectsDotConvenFile(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, ".conven"), []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err := InitWorkspace(workspace, []byte("version: 1\n"))
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("error = %v, want boundary error", err)
	}
}

func TestInitWorkspaceRejectsUserHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	_, _, err := InitWorkspace(home, []byte("version: 1\n"))
	if err == nil || !strings.Contains(err.Error(), "reserved for global configuration") {
		t.Fatalf("error = %v, want reserved home directory error", err)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".conven")); !os.IsNotExist(statErr) {
		t.Fatalf("home .conven stat error = %v, want directory not created", statErr)
	}
}

func TestInitWorkspaceRejectsUserHomeAliases(t *testing.T) {
	for _, test := range []struct {
		name      string
		homeAlias bool
	}{
		{name: "cwd is symlink", homeAlias: false},
		{name: "HOME is symlink", homeAlias: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			realHome := filepath.Join(root, "real-home")
			alias := filepath.Join(root, "home-alias")
			if err := os.Mkdir(realHome, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(realHome, alias); err != nil {
				t.Fatal(err)
			}
			home := realHome
			cwd := alias
			if test.homeAlias {
				home = alias
				cwd = realHome
			}
			t.Setenv("HOME", home)

			_, _, err := InitWorkspace(cwd, []byte("version: 1\n"))
			if err == nil || !strings.Contains(err.Error(), "reserved for global configuration") {
				t.Fatalf("error = %v, want reserved home directory error", err)
			}
			if _, statErr := os.Stat(filepath.Join(realHome, ".conven")); !os.IsNotExist(statErr) {
				t.Fatalf("real home .conven stat error = %v, want directory not created", statErr)
			}
		})
	}
}

func TestPublishNewManifestDoesNotReplaceConcurrentFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conven.yaml")
	if err := os.WriteFile(path, []byte("concurrent\n"), 0600); err != nil {
		t.Fatal(err)
	}
	created, err := publishNewManifest(path, []byte("generated\n"))
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("publish reported replacing an existing manifest")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "concurrent\n" {
		t.Fatalf("manifest = %q", data)
	}
}

func TestPublishNewManifestRejectsConcurrentNonRegularFile(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(string) error
	}{
		{name: "directory", setup: func(path string) error { return os.Mkdir(path, 0700) }},
		{name: "symbolic link", setup: func(path string) error {
			target := filepath.Join(filepath.Dir(path), "target.yaml")
			if err := os.WriteFile(target, []byte("target\n"), 0600); err != nil {
				return err
			}
			return os.Symlink(target, path)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "conven.yaml")
			if err := test.setup(path); err != nil {
				t.Fatal(err)
			}
			created, err := publishNewManifest(path, []byte("generated\n"))
			if err == nil || !strings.Contains(err.Error(), "must be a regular file") {
				t.Fatalf("error = %v, want regular file error", err)
			}
			if created {
				t.Fatal("publish reported replacing a non-regular manifest")
			}
		})
	}
}
