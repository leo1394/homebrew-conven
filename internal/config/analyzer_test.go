package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAnalyzeRepositoryMatchesRootGoModule(t *testing.T) {
	repository := newAnalyzerRepository(t, "root-service")
	writeAnalyzerFile(t, filepath.Join(repository, "go.mod"), "module example.com/root-service\n")
	writeAnalyzerFile(t, filepath.Join(repository, "main.go"), "package main\n\nfunc main() {}\n")

	analysis, matched, err := AnalyzeRepository(repository)
	if err != nil {
		t.Fatal(err)
	}
	if !matched {
		t.Fatal("root Go module was not matched")
	}
	if analysis.Analyzer != "go-root-module" {
		t.Fatalf("analyzer = %q", analysis.Analyzer)
	}
	if analysis.ServiceName != "root-service" || analysis.ModulePath != "example.com/root-service" {
		t.Fatalf("analysis identity = %#v", analysis)
	}
	assertAnalyzerRunner(t, analysis, ".")
}

func TestAnalyzeRepositoryMatchesGoSubdirectoryModule(t *testing.T) {
	repository := newAnalyzerRepository(t, "nested-service")
	writeAnalyzerFile(t, filepath.Join(repository, "go", "go.mod"), "module example.com/nested-service\n")
	writeAnalyzerFile(t, filepath.Join(repository, "go", "main.go"), "package main\n\nfunc main() {}\n")

	analysis, matched, err := AnalyzeRepository(repository)
	if err != nil {
		t.Fatal(err)
	}
	if !matched {
		t.Fatal("go/ Go module was not matched")
	}
	if analysis.Analyzer != "go-subdirectory-module" {
		t.Fatalf("analyzer = %q", analysis.Analyzer)
	}
	assertAnalyzerRunner(t, analysis, "go")
}

func TestAnalyzeRepositoryRejectsMultipleAnalyzerMatches(t *testing.T) {
	repository := newAnalyzerRepository(t, "conflict-service")
	for _, directory := range []string{repository, filepath.Join(repository, "go")} {
		writeAnalyzerFile(t, filepath.Join(directory, "go.mod"), "module example.com/conflict-service\n")
		writeAnalyzerFile(t, filepath.Join(directory, "main.go"), "package main\n\nfunc main() {}\n")
	}

	_, matched, err := AnalyzeRepository(repository)
	if err == nil {
		t.Fatal("multiple analyzer matches were accepted")
	}
	if matched {
		t.Fatal("conflicting repository was reported as matched")
	}
	for _, expected := range []string{"matched multiple analyzers", "go-root-module", "go-subdirectory-module"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("conflict error is missing %q: %v", expected, err)
		}
	}
}

func TestAnalyzeRepositoryInfersKindsAndRPCClientBindings(t *testing.T) {
	tests := []struct {
		name       string
		workdir    string
		serverType string
		kind       string
		yamlKey    string
	}{
		{name: "http", workdir: ".", serverType: "rest.RestConf", kind: RepositoryKindHTTP, yamlKey: "partnerRpc"},
		{name: "rpc", workdir: "go", serverType: "zrpc.RpcServerConf", kind: RepositoryKindRPC, yamlKey: "visitRpc"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repositoryName := test.name + "-service"
			repository := newAnalyzerRepository(t, repositoryName)
			moduleDirectory := repository
			if test.workdir != "." {
				moduleDirectory = filepath.Join(repository, test.workdir)
			}
			writeAnalyzerFile(t, filepath.Join(moduleDirectory, "go.mod"), "module example.com/"+repositoryName+"\n")
			writeAnalyzerFile(t, filepath.Join(moduleDirectory, "main.go"), "package main\n\nfunc main() {}\n")
			writeAnalyzerFile(t, filepath.Join(moduleDirectory, "config", "config.go"), `package config

import (
	"example.com/rest"
	"example.com/zrpc"
)

type Config struct {
	Server `+test.serverType+` `+"`yaml:\",inline\"`"+`
	Client zrpc.RpcClientConf `+"`yaml:\""+test.yamlKey+"\" json:\",optional\"`"+`
	NoBinding zrpc.RpcClientConf `+"`json:\"noBinding\"`"+`
}
`)
			for _, skipped := range []string{".git", "testdata", "vendor"} {
				writeAnalyzerFile(t, filepath.Join(moduleDirectory, skipped, "broken.go"), "package\n")
			}

			analysis, matched, err := AnalyzeRepository(repository)
			if err != nil {
				t.Fatal(err)
			}
			if !matched {
				t.Fatal("repository was not matched")
			}
			if analysis.Kind != test.kind {
				t.Fatalf("kind = %q, want %q", analysis.Kind, test.kind)
			}
			wantFile := "config/config.go"
			if test.workdir != "." {
				wantFile = test.workdir + "/" + wantFile
			}
			wantBindings := []RPCClientBindingCandidate{{
				File:       wantFile,
				StructName: "Config",
				FieldName:  "Client",
				YAMLKey:    test.yamlKey,
			}}
			if !reflect.DeepEqual(analysis.RPCClientBindings, wantBindings) {
				t.Fatalf("RPC client bindings = %#v, want %#v", analysis.RPCClientBindings, wantBindings)
			}
		})
	}
}

func TestAnalyzeRepositoryDoesNotModifyRepository(t *testing.T) {
	repository := newAnalyzerRepository(t, "read-only-service")
	writeAnalyzerFile(t, filepath.Join(repository, "go.mod"), "module example.com/read-only-service\n")
	writeAnalyzerFile(t, filepath.Join(repository, "main.go"), "package main\n\nfunc main() {}\n")
	writeAnalyzerFile(t, filepath.Join(repository, "config", "config.go"), "package config\n")
	before := analyzerRepositorySnapshot(t, repository)

	if _, matched, err := AnalyzeRepository(repository); err != nil {
		t.Fatal(err)
	} else if !matched {
		t.Fatal("repository was not matched")
	}

	after := analyzerRepositorySnapshot(t, repository)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("repository changed during analysis:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func assertAnalyzerRunner(t *testing.T, analysis RepositoryAnalysis, workdir string) {
	t.Helper()
	if analysis.Runner.Workdir != workdir {
		t.Fatalf("runner workdir = %q, want %q", analysis.Runner.Workdir, workdir)
	}
	wantBuild := []string{"go", "build", "-o", "${artifact}", "."}
	if !reflect.DeepEqual(analysis.Runner.Build, wantBuild) {
		t.Fatalf("runner build = %#v, want %#v", analysis.Runner.Build, wantBuild)
	}
	if !reflect.DeepEqual(analysis.Runner.Run, []string{"${artifact}"}) {
		t.Fatalf("runner run = %#v", analysis.Runner.Run)
	}
}

func newAnalyzerRepository(t *testing.T, name string) string {
	t.Helper()
	repository := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(repository, 0700); err != nil {
		t.Fatal(err)
	}
	return repository
}

func writeAnalyzerFile(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func analyzerRepositorySnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			snapshot[filepath.ToSlash(relative)+"/"] = entry.Type().String()
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snapshot[filepath.ToSlash(relative)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
