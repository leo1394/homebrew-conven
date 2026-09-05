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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

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

type RepositoryRegistrationEvidence struct {
	Provider  string
	File      string
	Protected bool
}

type RepositoryConsumerEvidence struct {
	Driver    string
	File      string
	Protected bool
}

type RepositoryAnalysis struct {
	Analyzer          string
	ServiceName       string
	ModulePath        string
	Framework         string
	Runtime           string
	Discovery         string
	Runner            model.Runner
	Kind              string
	Kinds             []string
	Health            model.Health
	HealthChecks      []model.ServiceHealthCheck
	RPCClientBindings []RPCClientBindingCandidate
	Registrations     []RepositoryRegistrationEvidence
	Consumers         []RepositoryConsumerEvidence
}

func (analysis RepositoryAnalysis) EffectiveKinds() []string {
	if len(analysis.Kinds) > 0 {
		return append([]string(nil), analysis.Kinds...)
	}
	if analysis.Kind != "" && analysis.Kind != RepositoryKindUnknown {
		return []string{analysis.Kind}
	}
	return nil
}

type RepositoryAnalyzer interface {
	Name() string
	Analyze(repository RepositoryCandidate) (RepositoryAnalysis, bool, error)
}

type registeredRepositoryAnalyzer struct {
	priority int
	analyzer RepositoryAnalyzer
}

var repositoryAnalyzerRegistry = struct {
	sync.RWMutex
	entries map[string]registeredRepositoryAnalyzer
}{entries: make(map[string]registeredRepositoryAnalyzer)}

func RegisterRepositoryAnalyzer(priority int, analyzer RepositoryAnalyzer) {
	if analyzer == nil || strings.TrimSpace(analyzer.Name()) == "" {
		panic("repository analyzer must have a name")
	}
	repositoryAnalyzerRegistry.Lock()
	defer repositoryAnalyzerRegistry.Unlock()
	if _, found := repositoryAnalyzerRegistry.entries[analyzer.Name()]; found {
		panic("duplicate repository analyzer " + analyzer.Name())
	}
	repositoryAnalyzerRegistry.entries[analyzer.Name()] = registeredRepositoryAnalyzer{priority: priority, analyzer: analyzer}
}

func init() {
	RegisterRepositoryAnalyzer(10, GoRootModuleAdapter{})
	RegisterRepositoryAnalyzer(20, GoSubdirectoryModuleAdapter{})
	registerDriverRepositoryCertifier("go-zero", certifyGoZeroRegistrationEvidence, goZeroPolicyCompatible)
	for _, runtimeName := range []string{"go-generic", "kratos", "hertz", "kitex"} {
		registerDriverRepositoryCertifier(runtimeName, certifyRegistrationEvidence, environmentPolicyCompatible)
	}
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
	repositoryAnalyzerRegistry.RLock()
	defer repositoryAnalyzerRegistry.RUnlock()
	entries := make([]registeredRepositoryAnalyzer, 0, len(repositoryAnalyzerRegistry.entries))
	for _, entry := range repositoryAnalyzerRegistry.entries {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(left int, right int) bool {
		if entries[left].priority == entries[right].priority {
			return entries[left].analyzer.Name() < entries[right].analyzer.Name()
		}
		return entries[left].priority < entries[right].priority
	})
	analyzers := make([]RepositoryAnalyzer, 0, len(entries))
	for _, entry := range entries {
		analyzers = append(analyzers, entry.analyzer)
	}
	return analyzers
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
	analysis := matches[0]
	consumers, err := InspectKafkaConsumerEvidence(candidate.Directory, analysis.Runner.Workdir)
	if err != nil {
		return RepositoryAnalysis{}, false, err
	}
	analysis.Consumers = consumers
	return analysis, true, nil
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
	goSumExists, err := regularFileExists(filepath.Join(moduleDirectory, "go.sum"))
	if err != nil {
		return RepositoryAnalysis{}, false, fmt.Errorf("inspect Go lockfile: %w", err)
	}
	if !goSumExists {
		return RepositoryAnalysis{}, false, fmt.Errorf("Go service %q has go.mod but no go.sum; run go mod tidy and commit go.sum before services --registry", repository.Name)
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
	if !found || goModuleRepositoryName(modulePath) != repository.Name {
		return RepositoryAnalysis{}, false, nil
	}
	kinds, bindings, err := inspectGoModule(repository.Directory, moduleDirectory)
	if err != nil {
		return RepositoryAnalysis{}, false, err
	}
	moduleData, err := os.ReadFile(moduleFile)
	if err != nil {
		return RepositoryAnalysis{}, false, fmt.Errorf("read %s: %w", moduleFile, err)
	}
	moduleSource, err := readTextSources(moduleDirectory, []string{".go"})
	if err != nil {
		return RepositoryAnalysis{}, false, fmt.Errorf("read Go source evidence: %w", err)
	}
	framework, runtimeName, discovery := classifyGoModule(string(moduleData), moduleSource, kinds)
	if len(kinds) == 0 {
		return RepositoryAnalysis{}, false, fmt.Errorf("Go service %q is recognized as %s but no HTTP/RPC listener could be proven; expose a supported listener and consume runtime host/port settings before services --registry", repository.Name, framework)
	}
	if runtimeName == "go-zero" {
		if err := ValidateGoZeroRuntimeConfigSource(repository.Name, repository.Directory, workdir); err != nil {
			return RepositoryAnalysis{}, false, err
		}
	} else {
		if err := validateGoEnvironmentContract(repository.Name, moduleSource, kinds); err != nil {
			return RepositoryAnalysis{}, false, err
		}
	}
	registrations, err := inspectGoRegistrationEvidence(moduleDirectory, discovery)
	if err != nil {
		return RepositoryAnalysis{}, false, err
	}
	kind := RepositoryKindUnknown
	if len(kinds) == 1 {
		kind = kinds[0]
	}
	healthChecks := make([]model.ServiceHealthCheck, 0, len(kinds))
	for _, server := range kinds {
		healthChecks = append(healthChecks, model.ServiceHealthCheck{Server: server, Type: "tcp", Address: "127.0.0.1:${port." + server + "}"})
	}
	return RepositoryAnalysis{
		ServiceName: repository.Name,
		ModulePath:  modulePath,
		Framework:   framework,
		Runtime:     runtimeName,
		Discovery:   discovery,
		Runner: model.Runner{
			Workdir: workdir,
			Prepare: []string{"go", "mod", "download"},
			Build:   []string{"go", "build", "-o", "${artifact}", "."},
			Run:     []string{"${artifact}"},
		},
		Kind:              kind,
		Kinds:             kinds,
		HealthChecks:      healthChecks,
		RPCClientBindings: bindings,
		Registrations:     registrations,
	}, true, nil
}

func ValidateGoZeroRuntimeConfigSource(name string, directory string, workdir string) error {
	if filepath.IsAbs(workdir) {
		relative, err := filepath.Rel(directory, workdir)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("Go service %q workdir is outside its repository: %s", name, workdir)
		}
		workdir = relative
	}
	if err := validateGoLocalModuleReplacements(name, directory, workdir); err != nil {
		return err
	}
	mainFile := filepath.Join(directory, workdir, "main.go")
	source, err := os.ReadFile(mainFile)
	if err != nil {
		return fmt.Errorf("read Go service %q runtime config entry %s: %w", name, goEntryDisplayPath(workdir), err)
	}
	files := token.NewFileSet()
	parsed, err := parser.ParseFile(files, mainFile, source, parser.SkipObjectResolution)
	if err != nil {
		return fmt.Errorf("parse Go service %q runtime config entry %s: %w", name, goEntryDisplayPath(workdir), err)
	}
	configVariables := make(map[string]bool)
	ast.Inspect(parsed, func(node ast.Node) bool {
		switch declaration := node.(type) {
		case *ast.AssignStmt:
			if len(declaration.Lhs) != len(declaration.Rhs) {
				return true
			}
			for index, value := range declaration.Rhs {
				if !isGoConfigFlag(value) {
					continue
				}
				if identifier, ok := declaration.Lhs[index].(*ast.Ident); ok {
					configVariables[identifier.Name] = true
				}
			}
		case *ast.ValueSpec:
			if len(declaration.Names) != len(declaration.Values) {
				return true
			}
			for index, value := range declaration.Values {
				if isGoConfigFlag(value) {
					configVariables[declaration.Names[index].Name] = true
				}
			}
		}
		return true
	})
	if len(configVariables) == 0 {
		return fmt.Errorf("Go service %q does not declare the required -f runtime config flag in %s\n  => Declare it with flag.String(\"f\", <default>, <usage>), call flag.Parse(), then load configuration from the parsed value", name, goEntryDisplayPath(workdir))
	}
	var mainFunction *ast.FuncDecl
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Recv == nil && function.Name.Name == "main" {
			mainFunction = function
			break
		}
	}
	if mainFunction == nil || mainFunction.Body == nil {
		return fmt.Errorf("Go service %q has no main entry function in %s", name, goEntryDisplayPath(workdir))
	}
	var parsePosition token.Pos
	var readPosition token.Pos
	ast.Inspect(mainFunction.Body, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.CallExpr:
			if isGoFlagCall(value, "Parse") && len(value.Args) == 0 && (parsePosition == token.NoPos || value.Pos() < parsePosition) {
				parsePosition = value.Pos()
			}
		case *ast.StarExpr:
			identifier, ok := value.X.(*ast.Ident)
			if ok && configVariables[identifier.Name] && (readPosition == token.NoPos || value.Pos() < readPosition) {
				readPosition = value.Pos()
			}
		}
		return true
	})
	if readPosition == token.NoPos {
		return fmt.Errorf("Go service %q declares -f but does not read its parsed value in %s\n  => Pass the parsed config path to the service configuration loader", name, goEntryDisplayPath(workdir))
	}
	if parsePosition == token.NoPos || parsePosition >= readPosition {
		line := files.Position(readPosition).Line
		return fmt.Errorf("Go service %q does not call flag.Parse() before reading -f at %s:%d\n  => Add flag.Parse() after defining command-line flags and before loading service configuration", name, goEntryDisplayPath(workdir), line)
	}
	return nil
}

func validateGoLocalModuleReplacements(name string, directory string, workdir string) error {
	moduleFile := filepath.Join(directory, workdir, "go.mod")
	source, err := os.ReadFile(moduleFile)
	if err != nil {
		return fmt.Errorf("read Go service %q module file %s: %w", name, filepath.ToSlash(filepath.Join(workdir, "go.mod")), err)
	}
	pattern := regexp.MustCompile(`(?m)^\s*(?:replace\s+)?[^\s]+(?:\s+v[^\s]+)?\s+=>\s+((?:\.{1,2}/|/)[^\s]+)\s*(?://.*)?$`)
	for _, match := range pattern.FindAllSubmatch(source, -1) {
		replacement := string(match[1])
		target := replacement
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(moduleFile), filepath.FromSlash(target))
		}
		info, statErr := os.Stat(filepath.Join(filepath.Clean(target), "go.mod"))
		if statErr == nil && info.Mode().IsRegular() {
			continue
		}
		return fmt.Errorf("Go service %q has an unavailable local module replacement %s in %s\n  => Run git -C %q submodule update --init --recursive, then retry", name, replacement, filepath.ToSlash(filepath.Join(workdir, "go.mod")), directory)
	}
	return nil
}

func isGoConfigFlag(expression ast.Expr) bool {
	call, ok := expression.(*ast.CallExpr)
	if !ok || !isGoFlagCall(call, "String") || len(call.Args) == 0 {
		return false
	}
	literal, ok := call.Args[0].(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return false
	}
	value, err := strconv.Unquote(literal.Value)
	return err == nil && value == "f"
}

func isGoFlagCall(call *ast.CallExpr, name string) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != name {
		return false
	}
	identifier, ok := selector.X.(*ast.Ident)
	return ok && identifier.Name == "flag"
}

func goEntryDisplayPath(workdir string) string {
	if workdir == "" || workdir == "." {
		return "main.go"
	}
	return filepath.ToSlash(filepath.Join(workdir, "main.go"))
}

func goModuleRepositoryName(modulePath string) string {
	cleaned := strings.TrimSuffix(modulePath, "/")
	base := path.Base(cleaned)
	if regexp.MustCompile(`^v[2-9][0-9]*$`).MatchString(base) {
		return path.Base(path.Dir(cleaned))
	}
	return base
}

func validateGoEnvironmentContract(name string, source string, kinds []string) error {
	readEnvironment := func(key string) bool {
		pattern := regexp.MustCompile(`(?m)\b(?:os\.)?(?:Getenv|LookupEnv)\s*\(\s*["']` + regexp.QuoteMeta(key) + `["']\s*\)`)
		return pattern.MatchString(source)
	}
	missing := make([]string, 0)
	if !readEnvironment("HOST") {
		missing = append(missing, "HOST")
	}
	if len(kinds) == 1 {
		if !readEnvironment("PORT") {
			missing = append(missing, "PORT")
		}
	} else {
		for _, kind := range kinds {
			key := strings.ToUpper(strings.ReplaceAll(kind, "-", "_")) + "_PORT"
			if !readEnvironment(key) {
				missing = append(missing, key)
			}
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("Go service %q has a supported listener but its runtime environment contract is incomplete; missing os.Getenv/os.LookupEnv consumption for %s; wire HOST and the listener port environment into the server address before services --registry", name, strings.Join(missing, ", "))
}

func inspectGoRegistrationEvidence(moduleDirectory string, discovery string) ([]RepositoryRegistrationEvidence, error) {
	if discovery == "passive" || discovery == "kubernetes-dns" {
		return nil, nil
	}
	evidence := make([]RepositoryRegistrationEvidence, 0)
	err := filepath.WalkDir(moduleDirectory, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if filePath != moduleDirectory && skippedAnalysisDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		data, err := os.ReadFile(filePath)
		if err != nil {
			return err
		}
		evidenceInSource, err := goRegistrationEvidenceInSource(filePath, data)
		if err != nil {
			return err
		}
		if len(evidenceInSource) == 0 {
			return nil
		}
		relative, err := filepath.Rel(moduleDirectory, filePath)
		if err != nil {
			return err
		}
		for _, registration := range evidenceInSource {
			evidence = append(evidence, RepositoryRegistrationEvidence{Provider: discovery, File: fmt.Sprintf("%s:%d", filepath.ToSlash(relative), registration.Line), Protected: registration.Protected})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("inspect Go registration source: %w", err)
	}
	return evidence, nil
}

var regexpGoRegistrationGuard = regexp.MustCompile(`(?s)(?:SERVICE_REGISTRATION_ENABLED|ServiceRegistrationEnabled).{0,300}(?:==\s*false|!\s*[A-Za-z0-9_.]+).{0,300}return`)

type goRegistrationCallEvidence struct {
	Line      int
	Protected bool
}

func goRegistrationEvidenceInSource(path string, source []byte) ([]goRegistrationCallEvidence, error) {
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, path, source, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse Go registration source %s: %w", path, err)
	}
	result := make([]goRegistrationCallEvidence, 0)
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		start := fileSet.Position(function.Body.Lbrace).Offset + 1
		end := fileSet.Position(function.Body.Rbrace).Offset
		if start < 0 || end < start || end > len(source) {
			continue
		}
		body := string(source[start:end])
		guards := regexpGoRegistrationGuard.FindAllStringIndex(body, -1)
		guardedBlocks := goRegistrationGuardedBlocks(fileSet, source, function.Body, start)
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || !goRegistryRegistrationCall(call) {
				return true
			}
			position := fileSet.Position(call.Lparen)
			offset := position.Offset - start
			protected := false
			for _, guard := range guards {
				if guard[1] <= offset {
					protected = true
					break
				}
			}
			for _, block := range guardedBlocks {
				if block[0] <= offset && offset <= block[1] {
					protected = true
					break
				}
			}
			result = append(result, goRegistrationCallEvidence{Line: position.Line, Protected: protected})
			return true
		})
	}
	return result, nil
}

func goRegistryRegistrationCall(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch selector.Sel.Name {
	case "ServiceRegister", "ServiceDeregister", "RegisterService", "DeregisterService", "RegisterInstance", "DeregisterInstance":
		return true
	case "Register", "Deregister":
		registryReceiver := false
		ast.Inspect(selector.X, func(node ast.Node) bool {
			identifier, ok := node.(*ast.Ident)
			if !ok {
				return true
			}
			name := strings.ToLower(identifier.Name)
			for _, marker := range []string{"consul", "nacos", "eureka", "etcd", "registry", "registrar", "discovery"} {
				if strings.Contains(name, marker) {
					registryReceiver = true
					return false
				}
			}
			return true
		})
		return registryReceiver
	}
	return false
}

func goRegistrationGuardedBlocks(fileSet *token.FileSet, source []byte, body *ast.BlockStmt, bodyStart int) [][2]int {
	blocks := make([][2]int, 0)
	ast.Inspect(body, func(node ast.Node) bool {
		statement, ok := node.(*ast.IfStmt)
		if !ok || statement.Body == nil {
			return true
		}
		conditionStart := fileSet.Position(statement.Cond.Pos()).Offset
		conditionEnd := fileSet.Position(statement.Cond.End()).Offset
		if conditionStart < 0 || conditionEnd < conditionStart || conditionEnd > len(source) {
			return true
		}
		condition := strings.ReplaceAll(strings.ToLower(string(source[conditionStart:conditionEnd])), " ", "")
		if !strings.Contains(condition, "service_registration_enabled") && !strings.Contains(condition, "serviceregistrationenabled") {
			return true
		}
		enabled := strings.Contains(condition, `!="false"`) || strings.Contains(condition, "!=false") || strings.Contains(condition, `=="true"`) || strings.Contains(condition, "==true") || strings.Contains(condition, "!strings.equalfold(")
		if !enabled {
			return true
		}
		start := fileSet.Position(statement.Body.Lbrace).Offset + 1 - bodyStart
		end := fileSet.Position(statement.Body.Rbrace).Offset - bodyStart
		blocks = append(blocks, [2]int{start, end})
		return true
	})
	return blocks
}

func inspectGoModule(repositoryDirectory string, moduleDirectory string) ([]string, []RPCClientBindingCandidate, error) {
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
		imports := make(map[string]string)
		hasHTTPFrameworkImport := false
		for _, declaration := range parsed.Imports {
			value, err := strconv.Unquote(declaration.Path.Value)
			if err != nil {
				continue
			}
			name := path.Base(value)
			if declaration.Name != nil {
				name = declaration.Name.Name
			}
			imports[name] = value
			if strings.Contains(value, "gin-gonic/gin") || strings.Contains(value, "labstack/echo") || strings.Contains(value, "gofiber/fiber") || strings.Contains(value, "go-chi/chi") {
				hasHTTPFrameworkImport = true
			}
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			identifier, directSelector := selector.X.(*ast.Ident)
			importPath := ""
			if directSelector {
				importPath = imports[identifier.Name]
			}
			switch {
			case importPath == "net/http" && selector.Sel.Name == "ListenAndServe":
				hasHTTPServer = true
			case strings.Contains(importPath, "google.golang.org/grpc") && selector.Sel.Name == "NewServer":
				hasRPCServer = true
			case strings.Contains(importPath, "go-kratos/kratos") && strings.Contains(importPath, "/transport/http") && selector.Sel.Name == "NewServer":
				hasHTTPServer = true
			case strings.Contains(importPath, "go-kratos/kratos") && strings.Contains(importPath, "/transport/grpc") && selector.Sel.Name == "NewServer":
				hasRPCServer = true
			case strings.Contains(importPath, "cloudwego/hertz") && (selector.Sel.Name == "Default" || selector.Sel.Name == "New"):
				hasHTTPServer = true
			case strings.Contains(importPath, "cloudwego/kitex") && selector.Sel.Name == "NewServer":
				hasRPCServer = true
			case hasHTTPFrameworkImport && (selector.Sel.Name == "Run" || selector.Sel.Name == "Start" || selector.Sel.Name == "Listen"):
				hasHTTPServer = true
			}
			return true
		})
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
		return nil, nil, fmt.Errorf("inspect Go module %s: %w", moduleDirectory, err)
	}
	sort.Slice(bindings, func(left int, right int) bool {
		leftKey := bindings[left].File + "\x00" + bindings[left].StructName + "\x00" + bindings[left].FieldName + "\x00" + bindings[left].YAMLKey
		rightKey := bindings[right].File + "\x00" + bindings[right].StructName + "\x00" + bindings[right].FieldName + "\x00" + bindings[right].YAMLKey
		return leftKey < rightKey
	})
	kinds := make([]string, 0, 2)
	if hasHTTPServer {
		kinds = append(kinds, RepositoryKindHTTP)
	}
	if hasRPCServer {
		kinds = append(kinds, RepositoryKindRPC)
	}
	return kinds, bindings, nil
}

func classifyGoModule(module string, source string, kinds []string) (string, string, string) {
	evidence := module + "\n" + source
	framework := "go"
	runtimeName := "go-generic"
	switch {
	case strings.Contains(evidence, "github.com/zeromicro/go-zero") || strings.Contains(evidence, "github.com/tal-tech/go-zero"):
		framework = "go-zero"
		runtimeName = "go-zero"
	case strings.Contains(evidence, "github.com/go-kratos/kratos"):
		framework = "kratos"
		runtimeName = "kratos"
	case strings.Contains(evidence, "github.com/cloudwego/hertz"):
		framework = "hertz"
		runtimeName = "hertz"
	case strings.Contains(evidence, "github.com/cloudwego/kitex"):
		framework = "kitex"
		runtimeName = "kitex"
	case strings.Contains(evidence, "github.com/gin-gonic/gin"):
		framework = "gin"
	case strings.Contains(evidence, "github.com/labstack/echo"):
		framework = "echo"
	case strings.Contains(evidence, "github.com/gofiber/fiber"):
		framework = "fiber"
	case strings.Contains(evidence, "github.com/go-chi/chi"):
		framework = "chi"
	case strings.Contains(evidence, "google.golang.org/grpc") && len(kinds) > 0:
		framework = "grpc-go"
	}
	discovery := "passive"
	switch {
	case strings.Contains(evidence, "github.com/hashicorp/consul") || strings.Contains(evidence, "consul-api"):
		discovery = "consul"
	case strings.Contains(evidence, "github.com/nacos-group/nacos-sdk-go"):
		discovery = "nacos"
	case strings.Contains(evidence, "go.etcd.io/etcd/client"):
		discovery = "etcd"
	}
	if runtimeName == "go-zero" && discovery == "passive" {
		discovery = "consul"
	}
	return framework, runtimeName, discovery
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
