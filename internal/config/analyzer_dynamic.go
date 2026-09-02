package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/leo1394/homebrew-conven/internal/model"
)

type NodeServiceAdapter struct{}

func init() {
	RegisterRepositoryAnalyzer(60, NodeServiceAdapter{})
	RegisterRepositoryAnalyzer(70, PythonServiceAdapter{})
	for _, runtimeName := range []string{"asgi-uvicorn", "nestjs", "node-http", "wsgi-gunicorn", "bun-http"} {
		registerDriverRepositoryCertifier(runtimeName, certifyRegistrationEvidence, environmentPolicyCompatible)
	}
}

func (NodeServiceAdapter) Name() string { return "node-service" }

func (NodeServiceAdapter) Analyze(repository RepositoryCandidate) (RepositoryAnalysis, bool, error) {
	packagePath := filepath.Join(repository.Directory, "package.json")
	data, err := os.ReadFile(packagePath)
	if errors.Is(err, os.ErrNotExist) { return RepositoryAnalysis{}, false, nil }
	if err != nil { return RepositoryAnalysis{}, false, fmt.Errorf("read %s: %w", packagePath, err) }
	var declaration struct {
		Scripts map[string]string `json:"scripts"`
		Dependencies map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(data, &declaration); err != nil { return RepositoryAnalysis{}, false, fmt.Errorf("decode %s: %w", packagePath, err) }
	dependencies := make(map[string]string, len(declaration.Dependencies)+len(declaration.DevDependencies))
	for key, value := range declaration.Dependencies { dependencies[key] = value }
	for key, value := range declaration.DevDependencies { dependencies[key] = value }
	source, err := readTextSources(repository.Directory, []string{".js", ".cjs", ".mjs", ".ts"})
	if err != nil { return RepositoryAnalysis{}, false, err }
	source = stripCStyleComments(source)
	framework, runtimeName := nodeFramework(dependencies)
	if framework == "" && strings.Contains(source, "Bun.serve") {
		framework, runtimeName = "bun-serve", "bun-http"
	}
	if framework == "" { return RepositoryAnalysis{}, false, nil }
	if !nodeServerEvidence(framework, source) { return RepositoryAnalysis{}, false, fmt.Errorf("%s repository %q is recognized but no supported server bootstrap call could be proven", framework, repository.Name) }
	if !strings.Contains(source, "process.env.HOST") && !strings.Contains(source, "Bun.env.HOST") {
		return RepositoryAnalysis{}, false, fmt.Errorf("%s service %q does not consume a runtime HOST; pass process.env.HOST to the listener so Conven can enforce loopback", framework, repository.Name)
	}
	manager, prepare, err := nodePackageManager(repository.Directory)
	if err != nil { return RepositoryAnalysis{}, false, err }
	startScript := "start"
	if declaration.Scripts[startScript] == "" && declaration.Scripts["start:prod"] != "" { startScript = "start:prod" }
	if declaration.Scripts[startScript] == "" { return RepositoryAnalysis{}, false, fmt.Errorf("%s service %q requires a deterministic package.json start or start:prod script", framework, repository.Name) }
	build := []string(nil)
	if declaration.Scripts["build"] != "" { build = []string{manager, "run", "build"} }
	run := []string{manager, "run", startScript}
	if manager == "yarn" { run = []string{"yarn", "run", startScript} }
	discovery := dependencyDiscovery(dependencies)
	registrations, err := dynamicRegistrationEvidence(repository.Directory, []string{".js", ".cjs", ".mjs", ".ts"}, discovery, stripCStyleComments)
	if err != nil { return RepositoryAnalysis{}, false, err }
	kinds := []string{RepositoryKindHTTP}
	if framework == "nestjs" {
		kinds = nil
		if strings.Contains(source, "NestFactory.create(") {
			kinds = append(kinds, RepositoryKindHTTP)
		}
		if strings.Contains(source, "Transport.GRPC") {
			kinds = append(kinds, RepositoryKindRPC)
		}
		if len(kinds) == 0 {
			return RepositoryAnalysis{}, false, fmt.Errorf("NestJS service %q has no statically provable HTTP or gRPC listener", repository.Name)
		}
	}
	portKeys := []string{"PORT"}
	if len(kinds) > 1 {
		portKeys = make([]string, 0, len(kinds))
		for _, kind := range kinds { portKeys = append(portKeys, strings.ToUpper(kind)+"_PORT") }
	}
	for _, key := range portKeys {
		if !strings.Contains(source, "process.env."+key) && !strings.Contains(source, "Bun.env."+key) {
			return RepositoryAnalysis{}, false, fmt.Errorf("%s service %q does not consume runtime %s; pass it to the corresponding listener before services --registry", framework, repository.Name, key)
		}
	}
	return RepositoryAnalysis{
		ServiceName: repository.Name,
		Framework: framework,
		Runtime: runtimeName,
		Discovery: discovery,
		Runner: model.Runner{Workdir: ".", Prepare: prepare, Build: build, Run: run},
		Kind: firstKind(kinds),
		Kinds: kinds,
		HealthChecks: tcpHealthChecks(kinds),
		Registrations: registrations,
	}, true, nil
}

func nodeFramework(dependencies map[string]string) (string, string) {
	switch {
	case dependencies["@nestjs/core"] != "": return "nestjs", "nestjs"
	case dependencies["fastify"] != "": return "fastify", "node-http"
	case dependencies["express"] != "": return "express", "node-http"
	case dependencies["elysia"] != "": return "elysia", "bun-http"
	case dependencies["hono"] != "": return "hono", "bun-http"
	default: return "", ""
	}
}

func nodeServerEvidence(framework string, source string) bool {
	switch framework {
	case "nestjs": return strings.Contains(source, "NestFactory.create")
	case "express": return (strings.Contains(source, "express(") || strings.Contains(source, "require('express')") || strings.Contains(source, `require("express")`)) && strings.Contains(source, ".listen(")
	case "fastify": return (strings.Contains(source, "fastify(") || strings.Contains(source, "Fastify(") || strings.Contains(source, "require('fastify')") || strings.Contains(source, `require("fastify")`)) && strings.Contains(source, ".listen(")
	case "elysia": return strings.Contains(source, "new Elysia") && strings.Contains(source, ".listen(")
	case "hono": return strings.Contains(source, "new Hono") && (strings.Contains(source, "Bun.serve") || strings.Contains(source, "serve("))
	case "bun-serve": return strings.Contains(source, "Bun.serve")
	default: return false
	}
}

func nodePackageManager(directory string) (string, []string, error) {
	type candidate struct { name string; files []string; command []string }
	candidates := []candidate{
		{"npm", []string{"package-lock.json"}, []string{"npm", "ci"}},
		{"pnpm", []string{"pnpm-lock.yaml"}, []string{"pnpm", "install", "--frozen-lockfile"}},
		{"yarn", []string{"yarn.lock"}, nil},
		{"bun", []string{"bun.lock", "bun.lockb"}, []string{"bun", "install", "--frozen-lockfile"}},
	}
	found := make([]candidate, 0, 1)
	for _, candidate := range candidates {
		present := false
		for _, file := range candidate.files { if exists, _ := regularFileExists(filepath.Join(directory, file)); exists { present = true } }
		if present { found = append(found, candidate) }
	}
	if len(found) != 1 { return "", nil, fmt.Errorf("Node/Bun service %q requires exactly one supported lockfile ecosystem; found %d", filepath.Base(directory), len(found)) }
	selected := found[0]
	if selected.name == "yarn" {
		if exists, _ := regularFileExists(filepath.Join(directory, ".yarnrc.yml")); exists { selected.command = []string{"yarn", "install", "--immutable"} } else { selected.command = []string{"yarn", "install", "--frozen-lockfile"} }
	}
	return selected.name, selected.command, nil
}

type PythonServiceAdapter struct{}

func (PythonServiceAdapter) Name() string { return "python-service" }

func (PythonServiceAdapter) Analyze(repository RepositoryCandidate) (RepositoryAnalysis, bool, error) {
	source, err := readTextSources(repository.Directory, []string{".py"})
	if err != nil { return RepositoryAnalysis{}, false, err }
	source = stripPythonComments(source)
	framework := ""
	runtimeName := ""
	switch {
	case strings.Contains(source, "FastAPI(") || strings.Contains(source, "Starlette("):
		framework, runtimeName = "fastapi", "asgi-uvicorn"
	case strings.Contains(source, "Flask("):
		framework, runtimeName = "flask", "wsgi-gunicorn"
	case fileExists(filepath.Join(repository.Directory, "manage.py")) && strings.Contains(source, "DJANGO_SETTINGS_MODULE"):
		framework, runtimeName = "django", "wsgi-gunicorn"
	default:
		return RepositoryAnalysis{}, false, nil
	}
	manager, prepare, prefix, err := pythonPackageManager(repository.Directory)
	if err != nil { return RepositoryAnalysis{}, false, err }
	entry, err := pythonEntryPoint(repository.Directory, framework)
	if err != nil { return RepositoryAnalysis{}, false, err }
	server := "uvicorn"
	if runtimeName == "wsgi-gunicorn" { server = "gunicorn" }
	run := append(append([]string(nil), prefix...), server, entry)
	if manager == "pip" {
		run = []string{"${runDir}/venvs/${service}/bin/" + server, entry}
	}
	discovery := pythonDiscovery(source)
	registrations, err := dynamicRegistrationEvidence(repository.Directory, []string{".py"}, discovery, stripPythonComments)
	if err != nil { return RepositoryAnalysis{}, false, err }
	_ = manager
	return RepositoryAnalysis{
		ServiceName: repository.Name,
		Framework: framework,
		Runtime: runtimeName,
		Discovery: discovery,
		Runner: model.Runner{Workdir: ".", Prepare: prepare, Run: run},
		Kind: RepositoryKindHTTP,
		Kinds: []string{RepositoryKindHTTP},
		HealthChecks: tcpHealthChecks([]string{RepositoryKindHTTP}),
		Registrations: registrations,
	}, true, nil
}

func pythonPackageManager(directory string) (string, []string, []string, error) {
	uv := fileExists(filepath.Join(directory, "uv.lock"))
	poetry := fileExists(filepath.Join(directory, "poetry.lock"))
	requirements := fileExists(filepath.Join(directory, "requirements.txt"))
	count := 0
	for _, found := range []bool{uv, poetry, requirements} { if found { count++ } }
	if count != 1 { return "", nil, nil, fmt.Errorf("Python service %q requires exactly one of uv.lock, poetry.lock, or requirements.txt", filepath.Base(directory)) }
	if uv { return "uv", []string{"uv", "sync", "--frozen"}, []string{"uv", "run"}, nil }
	if poetry { return "poetry", []string{"poetry", "install", "--sync", "--no-interaction"}, []string{"poetry", "run"}, nil }
	data, err := os.ReadFile(filepath.Join(directory, "requirements.txt"))
	if err != nil { return "", nil, nil, err }
	if err := validateHashedRequirements(data); err != nil {
		return "", nil, nil, err
	}
	venv := "${runDir}/venvs/${service}"
	prepare := []string{"sh", "-c", `python3 -m venv "$1" && "$1/bin/python" -m pip install --require-hashes -r "$2"`, "sh", venv, filepath.Join(directory, "requirements.txt")}
	return "pip", prepare, []string{venv + "/bin"}, nil
}

func validateHashedRequirements(data []byte) error {
	record := ""
	recordLine := 0
	validate := func() error {
		if record == "" { return nil }
		if !strings.Contains(record, "==") || !strings.Contains(record, "--hash=sha256:") {
			return fmt.Errorf("requirements.txt line %d must pin == and include --hash=sha256 for deterministic installation", recordLine)
		}
		record = ""
		recordLine = 0
		return nil
	}
	for index, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") { continue }
		if recordLine == 0 { recordLine = index + 1 }
		continued := strings.HasSuffix(line, "\\")
		line = strings.TrimSpace(strings.TrimSuffix(line, "\\"))
		record += " " + line
		if continued { continue }
		if err := validate(); err != nil { return err }
	}
	return validate()
}

func pythonEntryPoint(directory string, framework string) (string, error) {
	if framework == "django" {
		var result string
		_ = filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || entry.Name() != "wsgi.py" { return nil }
			relative, relErr := filepath.Rel(directory, path); if relErr != nil { return relErr }
			result = strings.TrimSuffix(filepath.ToSlash(relative), ".py")
			result = strings.ReplaceAll(result, "/", ".") + ":application"
			return nil
		})
		if result == "" { return "", errors.New("Django service has no unique wsgi.py entry") }
		return result, nil
	}
	pattern := "FastAPI("
	if framework == "fastapi" {
		source, err := readTextSources(directory, []string{".py"})
		if err != nil { return "", err }
		if !strings.Contains(source, pattern) && strings.Contains(source, "Starlette(") { pattern = "Starlette(" }
	}
	if framework == "flask" { pattern = "Flask(" }
	entries := make([]string, 0, 1)
	_ = filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || filepath.Ext(entry.Name()) != ".py" { return nil }
		data, readErr := os.ReadFile(path); if readErr != nil { return readErr }
		text := stripPythonComments(string(data))
		if !strings.Contains(text, pattern) { return nil }
		match := regexp.MustCompile(`(?m)^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*[^\n]*`+regexp.QuoteMeta(pattern)).FindStringSubmatch(text)
		if len(match) != 2 { return nil }
		relative, relErr := filepath.Rel(directory, path); if relErr != nil { return relErr }
		module := strings.TrimSuffix(filepath.ToSlash(relative), ".py")
		entries = append(entries, strings.ReplaceAll(module, "/", ".")+":"+match[1])
		return nil
	})
	if len(entries) != 1 { return "", fmt.Errorf("%s service requires exactly one statically identifiable application object; found %d", framework, len(entries)) }
	return entries[0], nil
}

func readTextSources(directory string, extensions []string) (string, error) {
	allowed := make(map[string]bool, len(extensions)); for _, extension := range extensions { allowed[extension] = true }
	var result strings.Builder
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil { return walkErr }
		if entry.IsDir() {
			if path != directory && (skippedAnalysisDirectory(entry.Name()) || entry.Name() == "node_modules" || entry.Name() == "dist" || entry.Name() == "build" || strings.HasPrefix(entry.Name(), ".")) { return filepath.SkipDir }
			return nil
		}
		if !allowed[filepath.Ext(entry.Name())] { return nil }
		info, err := entry.Info(); if err != nil { return err }; if info.Size() > 2<<20 { return nil }
		data, err := os.ReadFile(path); if err != nil { return err }
		result.Write(data); result.WriteByte('\n')
		return nil
	})
	return result.String(), err
}

func stripPythonComments(source string) string {
	result := []byte(source)
	quote := byte(0)
	escaped := false
	for index := 0; index < len(result); index++ {
		character := result[index]
		if quote != 0 {
			if escaped { escaped = false; continue }
			if character == '\\' { escaped = true; continue }
			if character == quote { quote = 0 }
			continue
		}
		if character == '\'' || character == '"' { quote = character; continue }
		if character != '#' { continue }
		for index < len(result) && result[index] != '\n' {
			result[index] = ' '
			index++
		}
	}
	return string(result)
}

func dependencyDiscovery(dependencies map[string]string) string {
	for dependency := range dependencies {
		lower := strings.ToLower(dependency)
		switch {
		case strings.Contains(lower, "consul"): return "consul"
		case strings.Contains(lower, "nacos"): return "nacos"
		case strings.Contains(lower, "eureka"): return "eureka"
		case strings.Contains(lower, "etcd"): return "etcd"
		}
	}
	return "passive"
}

func pythonDiscovery(source string) string {
	lower := strings.ToLower(source)
	for _, driver := range []string{"consul", "nacos", "eureka", "etcd"} { if strings.Contains(lower, driver) { return driver } }
	return "passive"
}

func registrationEvidenceFromText(file string, source string, discovery string) []RepositoryRegistrationEvidence {
	if discovery == "passive" || discovery == "kubernetes-dns" { return nil }
	calls := regexp.MustCompile(`(?i)(?:\.(?:register|deregister)\s*\(|\b(?:registerService|deregisterService|register_service|deregister_service|register_instance|deregister_instance)\s*\()`).FindAllStringIndex(source, -1)
	guards := neutralRegistrationGuardRanges(source)
	result := make([]RepositoryRegistrationEvidence, 0, len(calls))
	for _, call := range calls {
		protected := false
		for _, guard := range guards {
			if call[0] >= guard[0] && call[1] <= guard[1] { protected = true; break }
		}
		result = append(result, RepositoryRegistrationEvidence{Provider: discovery, File: fmt.Sprintf("%s:%d", file, 1+strings.Count(source[:call[0]], "\n")), Protected: protected})
	}
	return result
}

func neutralRegistrationGuardedText(source string) bool {
	return len(neutralRegistrationGuardRanges(source)) > 0
}

func neutralRegistrationGuardRanges(source string) [][]int {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?s)if\s*\([^)]*(?:process\.env|Bun\.env)\.SERVICE_REGISTRATION_ENABLED[^)]*\)\s*\{[^}]*(?:(?:\.(?:register|deregister)\s*\()|(?:registerService|deregisterService)\s*\()[^}]*\}`),
	}
	result := pythonRegistrationGuardRanges(source)
	for _, pattern := range patterns {
		result = append(result, pattern.FindAllStringIndex(source, -1)...)
	}
	return result
}

func pythonRegistrationGuardRanges(source string) [][]int {
	lines := strings.SplitAfter(source, "\n")
	offsets := make([]int, len(lines)+1)
	for index, line := range lines { offsets[index+1] = offsets[index] + len(line) }
	result := make([][]int, 0)
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "if ") || !strings.Contains(trimmed, "SERVICE_REGISTRATION_ENABLED") || !strings.HasSuffix(strings.TrimSuffix(trimmed, "#"), ":") { continue }
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		end := index + 1
		for end < len(lines) {
			candidate := lines[end]
			if strings.TrimSpace(candidate) == "" { end++; continue }
			candidateIndent := len(candidate) - len(strings.TrimLeft(candidate, " \t"))
			if candidateIndent <= indent { break }
			end++
		}
		result = append(result, []int{offsets[index], offsets[end]})
	}
	return result
}

func dynamicRegistrationEvidence(directory string, extensions []string, discovery string, strip func(string) string) ([]RepositoryRegistrationEvidence, error) {
	if discovery == "passive" || discovery == "kubernetes-dns" { return nil, nil }
	allowed := make(map[string]bool, len(extensions)); for _, extension := range extensions { allowed[extension] = true }
	result := make([]RepositoryRegistrationEvidence, 0)
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil { return walkErr }
		if entry.IsDir() {
			if path != directory && (skippedAnalysisDirectory(entry.Name()) || entry.Name() == "node_modules" || entry.Name() == "dist" || entry.Name() == "build" || strings.HasPrefix(entry.Name(), ".")) { return filepath.SkipDir }
			return nil
		}
		if !allowed[filepath.Ext(entry.Name())] { return nil }
		data, err := os.ReadFile(path); if err != nil { return err }
		relative, err := filepath.Rel(directory, path); if err != nil { return err }
		result = append(result, registrationEvidenceFromText(filepath.ToSlash(relative), strip(string(data)), discovery)...)
		return nil
	})
	return result, err
}

func tcpHealthChecks(kinds []string) []model.ServiceHealthCheck {
	result := make([]model.ServiceHealthCheck, 0, len(kinds))
	for _, kind := range kinds { result = append(result, model.ServiceHealthCheck{Server: kind, Type: "tcp", Address: "127.0.0.1:${port."+kind+"}"}) }
	return result
}

func firstKind(kinds []string) string { if len(kinds) == 1 { return kinds[0] }; return RepositoryKindUnknown }

func fileExists(path string) bool { exists, _ := regularFileExists(path); return exists }

func sortedKeys(values map[string]string) []string { result := make([]string, 0, len(values)); for key := range values { result = append(result, key) }; sort.Strings(result); return result }
