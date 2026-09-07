package config

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// InspectGoRPCClientBindings returns RpcClientConf fields declared by the Go
// module rooted at workdir.
func InspectGoRPCClientBindings(directory, workdir string) ([]RPCClientBindingCandidate, error) {
	repository := filepath.Clean(directory)
	root := workdir
	if !filepath.IsAbs(root) {
		root = filepath.Join(repository, root)
	}
	root = filepath.Clean(root)
	relativeRoot, err := filepath.Rel(repository, root)
	if err != nil || relativeRoot == ".." || strings.HasPrefix(relativeRoot, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("RPC client binding inspection workdir %q must stay within repository %q", root, repository)
	}
	_, bindings, err := inspectGoModule(repository, root)
	return bindings, err
}

// InspectGoRPCClientEndpointYAMLKey proves the YAML key consumed by the
// RpcClientConf Endpoints field in the module's local go-zero replacement.
func InspectGoRPCClientEndpointYAMLKey(directory, workdir string) (string, error) {
	repository := filepath.Clean(directory)
	root := workdir
	if !filepath.IsAbs(root) {
		root = filepath.Join(repository, root)
	}
	root = filepath.Clean(root)
	relativeRoot, err := filepath.Rel(repository, root)
	if err != nil || relativeRoot == ".." || strings.HasPrefix(relativeRoot, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("RPC client endpoint inspection workdir %q must stay within repository %q", root, repository)
	}
	moduleFile := filepath.Join(root, "go.mod")
	modulePath, err := inspectGoRPCClientModulePath(root)
	if err != nil {
		return "", err
	}
	replacement, err := inspectLocalRPCModuleReplacement(moduleFile, modulePath)
	if err != nil {
		return "", err
	}
	replacement = filepath.FromSlash(replacement)
	if !filepath.IsAbs(replacement) {
		replacement = filepath.Join(filepath.Dir(moduleFile), replacement)
	}
	configFile := filepath.Join(filepath.Clean(replacement), "zrpc", "config.go")
	parsed, err := parser.ParseFile(token.NewFileSet(), configFile, nil, 0)
	if err != nil {
		return "", fmt.Errorf("parse local go-zero zrpc config: %w", err)
	}
	for _, declaration := range parsed.Decls {
		generic, ok := declaration.(*ast.GenDecl)
		if !ok || generic.Tok != token.TYPE {
			continue
		}
		for _, specification := range generic.Specs {
			typeSpec, ok := specification.(*ast.TypeSpec)
			if !ok || typeSpec.Name.Name != "RpcClientConf" {
				continue
			}
			structure, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				break
			}
			for _, field := range structure.Fields.List {
				if len(field.Names) != 1 || field.Names[0].Name != "Endpoints" {
					continue
				}
				key, found := explicitYAMLKey(field)
				if !found || strings.TrimSpace(key) == "" {
					return "", fmt.Errorf("local go-zero RpcClientConf.Endpoints has no explicit YAML key")
				}
				return key, nil
			}
		}
	}
	return "", fmt.Errorf("local go-zero replacement does not declare RpcClientConf.Endpoints")
}

func inspectLocalRPCModuleReplacement(moduleFile, modulePath string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// -json is read-only and does not resolve dependencies or rewrite go.mod.
	command := exec.CommandContext(ctx, "go", "mod", "edit", "-json", moduleFile)
	command.Dir = filepath.Dir(moduleFile)
	command.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOWORK=off", "GOPROXY=off", "GOSUMDB=off")
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("cannot inspect go.mod replacements with go mod edit -json: %w", err)
	}
	type moduleRef struct {
		Path string
		Version string
	}
	var document struct {
		Replace []struct {
			Old moduleRef
			New moduleRef
		}
	}
	if err := json.Unmarshal(output, &document); err != nil {
		return "", fmt.Errorf("decode go.mod replacement metadata: %w", err)
	}
	replacement := ""
	for _, entry := range document.Replace {
		if entry.Old.Path != modulePath {
			continue
		}
		if entry.Old.Version != "" || replacement != "" {
			return "", fmt.Errorf("version-specific or multiple replacements for the imported go-zero module cannot safely verify the Endpoints YAML key")
		}
		if entry.New.Version != "" || !(filepath.IsAbs(entry.New.Path) || strings.HasPrefix(entry.New.Path, "./") || strings.HasPrefix(entry.New.Path, "../")) {
			return "", fmt.Errorf("local go-zero module replacement is required to verify the Endpoints YAML key")
		}
		replacement = entry.New.Path
	}
	if replacement == "" {
		return "", fmt.Errorf("local go-zero module replacement is required to verify the Endpoints YAML key")
	}
	return replacement, nil
}

func inspectGoRPCClientModulePath(root string) (string, error) {
	modules := make(map[string]bool)
	err := filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if filePath != root && skippedAnalysisDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			if filePath != root {
				nested, err := regularFileExists(filepath.Join(filePath, "go.mod"))
				if err != nil {
					return err
				}
				if nested {
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
		imports := make(map[string]string)
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
					selector, ok := field.Type.(*ast.SelectorExpr)
					if !ok || selector.Sel.Name != "RpcClientConf" {
						continue
					}
					identifier, ok := selector.X.(*ast.Ident)
					if !ok {
						continue
					}
					importPath := imports[identifier.Name]
					if strings.HasSuffix(importPath, "/zrpc") {
						modules[strings.TrimSuffix(importPath, "/zrpc")] = true
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("inspect RpcClientConf imports in %s: %w", root, err)
	}
	if len(modules) != 1 {
		return "", fmt.Errorf("expected one RpcClientConf zrpc module, found %d", len(modules))
	}
	for module := range modules {
		return module, nil
	}
	return "", fmt.Errorf("RpcClientConf zrpc module was not found")
}
