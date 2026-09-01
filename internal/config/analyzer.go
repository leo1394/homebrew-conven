package config

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/model"
)

const (
	RepositoryKindUnknown = "unknown"
	RepositoryKindHTTP    = "http"
	RepositoryKindRPC     = "rpc"
)

type RepositoryCandidate struct {
	Name      string
	Directory string
}

type RPCClientBindingCandidate struct {
	File       string
	StructName string
	FieldName  string
	YAMLKey    string
}

type RepositoryAnalysis struct {
	Analyzer          string
	ServiceName       string
	ModulePath        string
	Framework         string
	Discovery         string
	Runner            model.Runner
	Kind              string
	Health            model.Health
	RPCClientBindings []RPCClientBindingCandidate
}

type RepositoryAnalyzer interface {
	Name() string
	Analyze(repository RepositoryCandidate) (RepositoryAnalysis, bool, error)
}

type GoRootModuleAdapter struct{}

func (GoRootModuleAdapter) Name() string {
	return "go-root-module"
}

func (GoRootModuleAdapter) Analyze(repository RepositoryCandidate) (RepositoryAnalysis, bool, error) {
	return analyzeGoModule(repository, ".")
}

type GoSubdirectoryModuleAdapter struct{}

func (GoSubdirectoryModuleAdapter) Name() string {
	return "go-subdirectory-module"
}

func (GoSubdirectoryModuleAdapter) Analyze(repository RepositoryCandidate) (RepositoryAnalysis, bool, error) {
	return analyzeGoModule(repository, "go")
}

func BuiltinRepositoryAnalyzers() []RepositoryAnalyzer {
	return []RepositoryAnalyzer{
		GoRootModuleAdapter{},
		GoSubdirectoryModuleAdapter{},
		JavaGradleSpringBootAdapter{},
	}
}

func AnalyzeRepository(repository string, analyzers ...RepositoryAnalyzer) (RepositoryAnalysis, bool, error) {
	directory, err := resolveCwd(repository)
	if err != nil {
		return RepositoryAnalysis{}, false, err
	}
	candidate := RepositoryCandidate{
		Name:      filepath.Base(directory),
		Directory: directory,
	}
	if !validServiceName(candidate.Name) {
		return RepositoryAnalysis{}, false, nil
	}
	if len(analyzers) == 0 {
		analyzers = BuiltinRepositoryAnalyzers()
	}
	matches := make([]RepositoryAnalysis, 0, 1)
	for _, analyzer := range analyzers {
		if analyzer == nil {
			return RepositoryAnalysis{}, false, fmt.Errorf("repository analyzer is nil")
		}
		analyzerName := strings.TrimSpace(analyzer.Name())
		if analyzerName == "" {
			return RepositoryAnalysis{}, false, fmt.Errorf("repository analyzer name is empty")
		}
		analysis, matched, err := analyzer.Analyze(candidate)
		if err != nil {
			return RepositoryAnalysis{}, false, fmt.Errorf("analyze repository %q with %s: %w", candidate.Name, analyzerName, err)
		}
		if !matched {
			continue
		}
		analysis.Analyzer = analyzerName
		matches = append(matches, analysis)
	}
	if len(matches) == 0 {
		return RepositoryAnalysis{}, false, nil
	}
	if len(matches) > 1 {
		names := make([]string, 0, len(matches))
		for _, match := range matches {
			names = append(names, match.Analyzer)
		}
		sort.Strings(names)
		return RepositoryAnalysis{}, false, fmt.Errorf("repository %q matched multiple analyzers: %s", candidate.Name, strings.Join(names, ", "))
	}
	return matches[0], true, nil
}

func analyzeGoModule(repository RepositoryCandidate, workdir string) (RepositoryAnalysis, bool, error) {
	moduleDirectory := repository.Directory
	if workdir != "." {
		moduleDirectory = filepath.Join(moduleDirectory, workdir)
	}
	moduleFile := filepath.Join(moduleDirectory, "go.mod")
	mainFile := filepath.Join(moduleDirectory, "main.go")
	moduleExists, err := regularFileExists(moduleFile)
	if err != nil {
		return RepositoryAnalysis{}, false, fmt.Errorf("inspect %s: %w", moduleFile, err)
	}
	mainExists, err := regularFileExists(mainFile)
	if err != nil {
		return RepositoryAnalysis{}, false, fmt.Errorf("inspect %s: %w", mainFile, err)
	}
	if !moduleExists || !mainExists {
		return RepositoryAnalysis{}, false, nil
	}
	mainSource, err := os.ReadFile(mainFile)
	if err != nil {
		return RepositoryAnalysis{}, false, fmt.Errorf("read %s: %w", mainFile, err)
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), mainFile, mainSource, parser.PackageClauseOnly)
	if err != nil || parsed.Name == nil || parsed.Name.Name != "main" {
		return RepositoryAnalysis{}, false, nil
	}
	modulePath, found, err := moduleName(moduleFile)
	if err != nil {
		return RepositoryAnalysis{}, false, err
	}
	if !found || path.Base(strings.TrimSuffix(modulePath, "/")) != repository.Name {
		return RepositoryAnalysis{}, false, nil
	}
	kind, bindings, err := inspectGoModule(repository.Directory, moduleDirectory)
	if err != nil {
		return RepositoryAnalysis{}, false, err
	}
	return RepositoryAnalysis{
		ServiceName: repository.Name,
		ModulePath:  modulePath,
		Runner: model.Runner{
			Workdir: workdir,
			Build:   []string{"go", "build", "-o", "${artifact}", "."},
			Run:     []string{"${artifact}"},
		},
		Kind:              kind,
		RPCClientBindings: bindings,
	}, true, nil
}

func inspectGoModule(repositoryDirectory string, moduleDirectory string) (string, []RPCClientBindingCandidate, error) {
	hasHTTPServer := false
	hasRPCServer := false
	bindings := make([]RPCClientBindingCandidate, 0)
	err := filepath.WalkDir(moduleDirectory, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if filePath != moduleDirectory && skippedAnalysisDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			if filePath != moduleDirectory {
				nestedModule, err := regularFileExists(filepath.Join(filePath, "go.mod"))
				if err != nil {
					return err
				}
				if nestedModule {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), filePath, nil, parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("parse Go source %s: %w", filePath, err)
		}
		relative, err := filepath.Rel(repositoryDirectory, filePath)
		if err != nil {
			return fmt.Errorf("resolve analyzed Go source %s: %w", filePath, err)
		}
		for _, declaration := range parsed.Decls {
			generic, ok := declaration.(*ast.GenDecl)
			if !ok || generic.Tok != token.TYPE {
				continue
			}
			for _, specification := range generic.Specs {
				typeSpec, ok := specification.(*ast.TypeSpec)
				if !ok {
					continue
				}
				structure, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, field := range structure.Fields.List {
					typeName := selectorTypeName(field.Type)
					switch typeName {
					case "RestConf":
						hasHTTPServer = true
					case "RpcServerConf":
						hasRPCServer = true
					case "RpcClientConf":
						yamlKey, found := explicitYAMLKey(field)
						if !found {
							continue
						}
						for _, name := range field.Names {
							bindings = append(bindings, RPCClientBindingCandidate{
								File:       filepath.ToSlash(relative),
								StructName: typeSpec.Name.Name,
								FieldName:  name.Name,
								YAMLKey:    yamlKey,
							})
						}
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return RepositoryKindUnknown, nil, fmt.Errorf("inspect Go module %s: %w", moduleDirectory, err)
	}
	sort.Slice(bindings, func(left int, right int) bool {
		leftKey := bindings[left].File + "\x00" + bindings[left].StructName + "\x00" + bindings[left].FieldName + "\x00" + bindings[left].YAMLKey
		rightKey := bindings[right].File + "\x00" + bindings[right].StructName + "\x00" + bindings[right].FieldName + "\x00" + bindings[right].YAMLKey
		return leftKey < rightKey
	})
	kind := RepositoryKindUnknown
	if hasHTTPServer && !hasRPCServer {
		kind = RepositoryKindHTTP
	}
	if hasRPCServer && !hasHTTPServer {
		kind = RepositoryKindRPC
	}
	return kind, bindings, nil
}

func skippedAnalysisDirectory(name string) bool {
	switch name {
	case ".git", "testdata", "vendor":
		return true
	default:
		return false
	}
}

func selectorTypeName(expression ast.Expr) string {
	for {
		switch value := expression.(type) {
		case *ast.ParenExpr:
			expression = value.X
		case *ast.StarExpr:
			expression = value.X
		case *ast.SelectorExpr:
			return value.Sel.Name
		default:
			return ""
		}
	}
}

func explicitYAMLKey(field *ast.Field) (string, bool) {
	if field.Tag == nil {
		return "", false
	}
	value, err := strconv.Unquote(field.Tag.Value)
	if err != nil {
		return "", false
	}
	tag := reflect.StructTag(value).Get("yaml")
	key, _, _ := strings.Cut(tag, ",")
	if key == "" || key == "-" {
		return "", false
	}
	return key, true
}
