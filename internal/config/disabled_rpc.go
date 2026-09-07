package config

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type goRPCSourceFile struct {
	path       string
	relative   string
	directory  string
	set        *token.FileSet
	parsed     *ast.File
	imports    map[string]string
}

type goRPCBinding struct {
	yamlKey    string
	fieldName  string
	structName string
	directory  string
	file       string
	line       int
}

type goRPCGuardHelper struct {
	valid  bool
	object *ast.Object
}

type goRPCConfigCallTarget struct {
	object     *ast.Object
	parameters map[int]*goRPCBinding
}

type goRPCBlankValue int

const (
	goRPCBlankUnknown goRPCBlankValue = iota
	goRPCBlankFalse
	goRPCBlankTrue
	goRPCBlankString
	goRPCBlankList
)

// InspectGoRPCDisableCapabilities proves that requested RPC client bindings are
// either unused or only used below a condition that is false for disabled YAML.
func InspectGoRPCDisableCapabilities(directory, workdir string, bindings []string) (map[string]string, error) {
	repository := filepath.Clean(directory)
	root := workdir
	if !filepath.IsAbs(root) {
		root = filepath.Join(repository, root)
	}
	root = filepath.Clean(root)
	relativeRoot, err := filepath.Rel(repository, root)
	if err != nil || relativeRoot == ".." || strings.HasPrefix(relativeRoot, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("RPC disable analysis workdir %q must stay within repository %q", root, repository)
	}

	requested := make(map[string]bool)
	for _, binding := range bindings {
		if binding = strings.TrimSpace(binding); binding != "" {
			requested[binding] = true
		}
	}
	if len(requested) == 0 {
		return map[string]string{}, nil
	}

	files, declarations, err := scanGoRPCModule(repository, root, requested)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string)
	if len(declarations) == 0 {
		return result, nil
	}
	packageSymbols := collectGoRPCPackageSymbols(files)
	helpers := collectGoRPCGuardHelpers(files, packageSymbols)
	callTargets := collectGoRPCConfigCallTargets(files, declarations)
	configStructFields := collectGoRPCConfigStructFields(files, declarations)
	states := make(map[*goRPCBinding]string)
	for _, binding := range declarations {
		states[binding] = "unused"
	}
	for _, file := range files {
		if err := inspectGoRPCFileUses(file, declarations, helpers, packageSymbols, callTargets, configStructFields, states); err != nil {
			return nil, err
		}
	}
	for _, binding := range declarations {
		if states[binding] == "guarded" {
			result[binding.yamlKey] = "guarded"
		} else if result[binding.yamlKey] == "" {
			result[binding.yamlKey] = "unused"
		}
	}
	return result, nil
}

func collectGoRPCConfigStructFields(files []goRPCSourceFile, declarations []*goRPCBinding) map[string]map[string]bool {
	configTypes := make(map[string]bool)
	for _, binding := range declarations {
		configTypes[binding.directory+"\x00"+binding.structName] = true
	}
	fields := make(map[string]map[string]bool)
	for _, file := range files {
		for _, declaration := range file.parsed.Decls {
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
				key := file.directory + "\x00" + typeSpec.Name.Name
				for _, field := range structure.Fields.List {
					if !configTypes[goRPCTypeKey(file, field.Type)] {
						continue
					}
					if fields[key] == nil {
						fields[key] = make(map[string]bool)
					}
					for _, name := range field.Names {
						fields[key][name.Name] = true
					}
				}
			}
		}
	}
	return fields
}

func collectGoRPCConfigCallTargets(files []goRPCSourceFile, declarations []*goRPCBinding) map[string]goRPCConfigCallTarget {
	bindingsByType := make(map[string]map[string]*goRPCBinding)
	for _, binding := range declarations {
		key := binding.directory + "\x00" + binding.structName
		if bindingsByType[key] == nil {
			bindingsByType[key] = make(map[string]*goRPCBinding)
		}
		bindingsByType[key][binding.fieldName] = binding
	}
	targets := make(map[string]goRPCConfigCallTarget)
	for _, file := range files {
		for _, declaration := range file.parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv != nil || function.Type.Params == nil {
				continue
			}
			target := goRPCConfigCallTarget{object: function.Name.Obj, parameters: make(map[int]*goRPCBinding)}
			index := 0
			for _, parameter := range function.Type.Params.List {
				count := len(parameter.Names)
				if count == 0 {
					count = 1
				}
				members := bindingsByType[goRPCTypeKey(file, parameter.Type)]
				for offset := 0; offset < count; offset++ {
					if members != nil {
						target.parameters[index+offset] = firstGoRPCBinding(members)
					}
				}
				index += count
			}
			targets[file.directory+"\x00"+function.Name.Name] = target
		}
	}
	return targets
}

func scanGoRPCModule(repository, root string, requested map[string]bool) ([]goRPCSourceFile, []*goRPCBinding, error) {
	files := make([]goRPCSourceFile, 0)
	declarations := make([]*goRPCBinding, 0)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && skippedAnalysisDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			if path != root {
				nestedModule, err := regularFileExists(filepath.Join(path, "go.mod"))
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
		set := token.NewFileSet()
		parsed, err := parser.ParseFile(set, path, nil, 0)
		if err != nil {
			return fmt.Errorf("parse Go source %s for RPC disable analysis: %w", path, err)
		}
		relative, err := filepath.Rel(repository, path)
		if err != nil {
			return fmt.Errorf("resolve RPC disable source %s: %w", path, err)
		}
		imports := make(map[string]string)
		for _, declaration := range parsed.Imports {
			value, err := strconv.Unquote(declaration.Path.Value)
			if err != nil {
				continue
			}
			name := filepath.Base(value)
			if declaration.Name != nil {
				name = declaration.Name.Name
			}
			imports[name] = value
		}
		file := goRPCSourceFile{path: path, relative: filepath.ToSlash(relative), directory: filepath.Dir(path), set: set, parsed: parsed, imports: imports}
		files = append(files, file)
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
					if selectorTypeName(field.Type) != "RpcClientConf" {
						continue
					}
					yamlKey, found := explicitYAMLKey(field)
					if !found || !requested[yamlKey] {
						continue
					}
					for _, name := range field.Names {
						declarations = append(declarations, &goRPCBinding{yamlKey: yamlKey, fieldName: name.Name, structName: typeSpec.Name.Name, directory: file.directory, file: file.relative, line: set.Position(name.Pos()).Line})
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("inspect Go RPC disable capabilities in %s: %w", root, err)
	}
	sort.Slice(files, func(left, right int) bool { return files[left].relative < files[right].relative })
	sort.Slice(declarations, func(left, right int) bool {
		leftKey := declarations[left].yamlKey + "\x00" + declarations[left].file
		rightKey := declarations[right].yamlKey + "\x00" + declarations[right].file
		return leftKey < rightKey
	})
	return files, declarations, nil
}

func collectGoRPCPackageSymbols(files []goRPCSourceFile) map[string]map[string]bool {
	symbols := make(map[string]map[string]bool)
	for _, file := range files {
		if symbols[file.directory] == nil {
			symbols[file.directory] = make(map[string]bool)
		}
		for _, declaration := range file.parsed.Decls {
			switch value := declaration.(type) {
			case *ast.FuncDecl:
				symbols[file.directory][value.Name.Name] = true
			case *ast.GenDecl:
				for _, specification := range value.Specs {
					switch item := specification.(type) {
					case *ast.TypeSpec:
						symbols[file.directory][item.Name.Name] = true
					case *ast.ValueSpec:
						for _, name := range item.Names {
							symbols[file.directory][name.Name] = true
						}
					}
				}
			}
		}
	}
	return symbols
}

func collectGoRPCGuardHelpers(files []goRPCSourceFile, packageSymbols map[string]map[string]bool) map[string]map[string]goRPCGuardHelper {
	helpers := make(map[string]map[string]goRPCGuardHelper)
	for _, file := range files {
		for _, declaration := range file.parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv != nil || function.Body == nil || function.Type.Params == nil || len(function.Type.Params.List) != 1 {
				continue
			}
			parameter := function.Type.Params.List[0]
			if selectorTypeName(parameter.Type) != "RpcClientConf" || len(parameter.Names) != 1 || function.Type.Results == nil || len(function.Type.Results.List) != 1 || goRPCTypeName(function.Type.Results.List[0].Type) != "bool" {
				continue
			}
			valid := false
			if len(function.Body.List) == 1 {
				if returned, ok := function.Body.List[0].(*ast.ReturnStmt); ok && len(returned.Results) == 1 {
					value, referenced := evaluateGoRPCHelperBlank(returned.Results[0], parameter.Names[0].Obj, !packageSymbols[file.directory]["len"])
					valid = referenced && value == goRPCBlankFalse
				}
			}
			if helpers[file.directory] == nil {
				helpers[file.directory] = make(map[string]goRPCGuardHelper)
			}
			helpers[file.directory][function.Name.Name] = goRPCGuardHelper{valid: valid, object: function.Name.Obj}
		}
	}
	return helpers
}

func goRPCTypeName(expression ast.Expr) string {
	if identifier, ok := expression.(*ast.Ident); ok {
		return identifier.Name
	}
	return selectorTypeName(expression)
}

func evaluateGoRPCHelperBlank(expression ast.Expr, parameter *ast.Object, allowBuiltinLen bool) (goRPCBlankValue, bool) {
	return evaluateGoRPCBlank(expression, func(value ast.Expr) (goRPCBlankValue, bool) {
		fields, ok := goRPCSelectorPath(value, parameter)
		if !ok {
			return goRPCBlankUnknown, false
		}
		switch strings.Join(fields, ".") {
		case "DiscovType", "Target", "Etcd.Key":
			return goRPCBlankString, true
		case "Endpoints", "Endpints", "Etcd.Hosts":
			return goRPCBlankList, true
		default:
			return goRPCBlankUnknown, true
		}
	}, allowBuiltinLen)
}

func evaluateGoRPCBlank(expression ast.Expr, lookup func(ast.Expr) (goRPCBlankValue, bool), allowBuiltinLen bool) (goRPCBlankValue, bool) {
	switch value := expression.(type) {
	case *ast.ParenExpr:
		return evaluateGoRPCBlank(value.X, lookup, allowBuiltinLen)
	case *ast.UnaryExpr:
		result, referenced := evaluateGoRPCBlank(value.X, lookup, allowBuiltinLen)
		if value.Op != token.NOT {
			return goRPCBlankUnknown, referenced
		}
		if result == goRPCBlankTrue {
			return goRPCBlankFalse, referenced
		}
		if result == goRPCBlankFalse {
			return goRPCBlankTrue, referenced
		}
		return goRPCBlankUnknown, referenced
	case *ast.BinaryExpr:
		left, leftReferenced := evaluateGoRPCBlank(value.X, lookup, allowBuiltinLen)
		right, rightReferenced := evaluateGoRPCBlank(value.Y, lookup, allowBuiltinLen)
		referenced := leftReferenced || rightReferenced
		switch value.Op {
		case token.LOR:
			if left == goRPCBlankFalse && right == goRPCBlankFalse {
				return goRPCBlankFalse, referenced
			}
			if (left == goRPCBlankTrue || left == goRPCBlankFalse) && (right == goRPCBlankTrue || right == goRPCBlankFalse) {
				return goRPCBlankTrue, referenced
			}
		case token.LAND:
			if left == goRPCBlankTrue && right == goRPCBlankTrue {
				return goRPCBlankTrue, referenced
			}
			if (left == goRPCBlankTrue || left == goRPCBlankFalse) && (right == goRPCBlankTrue || right == goRPCBlankFalse) {
				return goRPCBlankFalse, referenced
			}
		case token.EQL, token.NEQ, token.GTR:
			if (left == goRPCBlankString && right == goRPCBlankString) || (left == goRPCBlankList && right == goRPCBlankList) {
				if value.Op == token.EQL {
					return goRPCBlankTrue, referenced
				}
				return goRPCBlankFalse, referenced
			}
		}
		return goRPCBlankUnknown, referenced
	case *ast.BasicLit:
		if value.Kind == token.STRING {
			text, err := strconv.Unquote(value.Value)
			if err == nil && text == "" {
				return goRPCBlankString, false
			}
		}
		if value.Kind == token.INT && value.Value == "0" {
			return goRPCBlankList, false
		}
	case *ast.CallExpr:
		if identifier, ok := value.Fun.(*ast.Ident); ok && allowBuiltinLen && identifier.Name == "len" && identifier.Obj == nil && len(value.Args) == 1 {
			result, referenced := lookup(value.Args[0])
			if result == goRPCBlankList {
				return goRPCBlankList, referenced
			}
			return goRPCBlankUnknown, referenced
		}
	}
	return lookup(expression)
}

func goRPCSelectorPath(expression ast.Expr, root *ast.Object) ([]string, bool) {
	switch value := expression.(type) {
	case *ast.Ident:
		return nil, value.Obj == root
	case *ast.SelectorExpr:
		path, ok := goRPCSelectorPath(value.X, root)
		if !ok {
			return nil, false
		}
		return append(path, value.Sel.Name), true
	default:
		return nil, false
	}
}

func inspectGoRPCFileUses(file goRPCSourceFile, declarations []*goRPCBinding, helpers map[string]map[string]goRPCGuardHelper, packageSymbols map[string]map[string]bool, callTargets map[string]goRPCConfigCallTarget, configStructFields map[string]map[string]bool, states map[*goRPCBinding]string) error {
	bindingsByType := make(map[string]map[string]*goRPCBinding)
	bindingsByField := make(map[string]*goRPCBinding)
	for _, binding := range declarations {
		key := binding.directory + "\x00" + binding.structName
		if bindingsByType[key] == nil {
			bindingsByType[key] = make(map[string]*goRPCBinding)
		}
		bindingsByType[key][binding.fieldName] = binding
		bindingsByField[binding.fieldName] = binding
	}
	for _, declaration := range file.parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Body != nil {
			if err := inspectGoRPCFunction(file, function, bindingsByType, helpers[file.directory], packageSymbols[file.directory], callTargets, configStructFields, states); err != nil {
				return err
			}
			continue
		}
		generic, ok := declaration.(*ast.GenDecl)
		if !ok || (generic.Tok != token.VAR && generic.Tok != token.CONST) {
			continue
		}
		var unresolved *ast.SelectorExpr
		var binding *goRPCBinding
		ast.Inspect(generic, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if ok && bindingsByField[selector.Sel.Name] != nil {
				unresolved = selector
				binding = bindingsByField[selector.Sel.Name]
				return false
			}
			return unresolved == nil
		})
		if unresolved != nil {
			return goRPCUseError(file, unresolved, binding, "has an unresolved package-level receiver; move the concrete use behind a recognized blank-config guard")
		}
	}
	return nil
}

func inspectGoRPCFunction(file goRPCSourceFile, function *ast.FuncDecl, bindingsByType map[string]map[string]*goRPCBinding, helpers map[string]goRPCGuardHelper, packageSymbols map[string]bool, callTargets map[string]goRPCConfigCallTarget, configStructFields map[string]map[string]bool, states map[*goRPCBinding]string) error {
	configObjects := make(map[*ast.Object]map[string]*goRPCBinding)
	collect := func(fields *ast.FieldList) {
		if fields == nil {
			return
		}
		for _, field := range fields.List {
			key := goRPCTypeKey(file, field.Type)
			members := bindingsByType[key]
			if members == nil {
				continue
			}
			for _, name := range field.Names {
				configObjects[name.Obj] = members
			}
		}
	}
	collect(function.Type.Params)
	collect(function.Recv)

	aliases := make(map[*ast.Object]*goRPCBinding)
	carriers := make(map[*ast.Object]map[string]map[string]*goRPCBinding)
	carrierDefinitions := make(map[*ast.Object]ast.Node)
	exempt := make(map[ast.Node]bool)
	mutations := make(map[*goRPCBinding]ast.Node)
	aliasDefinitions := make(map[*ast.Object]ast.Node)
	configDefinitions := make(map[*ast.Object]ast.Node)
	for changed := true; changed; {
		changed = false
		ast.Inspect(function.Body, func(node ast.Node) bool {
			switch statement := node.(type) {
			case *ast.AssignStmt:
				for index := range statement.Lhs {
					if index >= len(statement.Rhs) {
						continue
					}
					left, ok := statement.Lhs[index].(*ast.Ident)
					if !ok || left.Obj == nil {
						continue
					}
					if carrier := goRPCCompositeCarrier(file, statement.Rhs[index], configStructFields, configObjects, carriers); carrier != nil && carriers[left.Obj] == nil {
						carriers[left.Obj] = carrier
						carrierDefinitions[left.Obj] = statement
						changed = true
					}
					if identifier, ok := goRPCUnparen(statement.Rhs[index]).(*ast.Ident); ok && carriers[identifier.Obj] != nil && carriers[left.Obj] == nil {
						carriers[left.Obj] = carriers[identifier.Obj]
						carrierDefinitions[left.Obj] = statement
						changed = true
					}
					if members := goRPCConfigMembers(statement.Rhs[index], configObjects, carriers); members != nil && configObjects[left.Obj] == nil {
						configObjects[left.Obj] = members
						configDefinitions[left.Obj] = statement
						changed = true
					}
					if binding := goRPCResolveBinding(statement.Rhs[index], configObjects, carriers, aliases); binding != nil {
						if statement.Tok == token.DEFINE && aliases[left.Obj] == nil {
							aliases[left.Obj] = binding
							aliasDefinitions[left.Obj] = statement
							exempt[left] = true
							exempt[statement.Rhs[index]] = true
							changed = true
						} else if aliases[left.Obj] != nil && aliasDefinitions[left.Obj] != statement {
							mutations[aliases[left.Obj]] = left
						}
					}
				}
			case *ast.ValueSpec:
				for index, left := range statement.Names {
					if index >= len(statement.Values) || left.Obj == nil {
						continue
					}
					if carrier := goRPCCompositeCarrier(file, statement.Values[index], configStructFields, configObjects, carriers); carrier != nil && carriers[left.Obj] == nil {
						carriers[left.Obj] = carrier
						carrierDefinitions[left.Obj] = statement
						changed = true
					}
					if identifier, ok := goRPCUnparen(statement.Values[index]).(*ast.Ident); ok && carriers[identifier.Obj] != nil && carriers[left.Obj] == nil {
						carriers[left.Obj] = carriers[identifier.Obj]
						carrierDefinitions[left.Obj] = statement
						changed = true
					}
					if members := goRPCConfigMembers(statement.Values[index], configObjects, carriers); members != nil && configObjects[left.Obj] == nil {
						configObjects[left.Obj] = members
						configDefinitions[left.Obj] = statement
						changed = true
					}
					if binding := goRPCResolveBinding(statement.Values[index], configObjects, carriers, aliases); binding != nil && aliases[left.Obj] == nil {
						aliases[left.Obj] = binding
						aliasDefinitions[left.Obj] = statement
						exempt[left] = true
						exempt[statement.Values[index]] = true
						changed = true
					}
				}
			}
			return true
		})
	}
	ast.Inspect(function.Body, func(node ast.Node) bool {
		statement, ok := node.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, expression := range statement.Lhs {
			identifier, ok := expression.(*ast.Ident)
			if !ok || identifier.Obj == nil || aliases[identifier.Obj] == nil || aliasDefinitions[identifier.Obj] == statement {
				continue
			}
			mutations[aliases[identifier.Obj]] = identifier
		}
		return true
	})
	parents := make(map[ast.Node]ast.Node)
	stack := make([]ast.Node, 0)
	ast.Inspect(function.Body, func(node ast.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		if len(stack) > 0 {
			parents[node] = stack[len(stack)-1]
		}
		stack = append(stack, node)
		return true
	})
	var configFailure error
	ast.Inspect(function.Body, func(node ast.Node) bool {
		if configFailure != nil {
			return false
		}
			switch value := node.(type) {
			case *ast.AssignStmt:
				for _, expression := range value.Lhs {
					members := goRPCConfigMembers(expression, configObjects, carriers)
					if members == nil {
						continue
					}
					if identifier, ok := goRPCUnparen(expression).(*ast.Ident); ok && configDefinitions[identifier.Obj] == value {
						continue
					}
					configFailure = goRPCUseError(file, expression, firstGoRPCBinding(members), "is invalidated by whole-config reassignment; keep disabled RPC configuration immutable")
					return false
				}
				for _, expression := range value.Lhs {
					identifier, ok := goRPCUnparen(expression).(*ast.Ident)
					if !ok || carriers[identifier.Obj] == nil || carrierDefinitions[identifier.Obj] == value {
						continue
					}
					configFailure = goRPCUseError(file, expression, firstGoRPCCarrierBinding(carriers[identifier.Obj]), "is invalidated by whole-carrier reassignment; keep config carriers immutable")
					return false
				}
			case *ast.UnaryExpr:
				members := goRPCConfigMembers(value.X, configObjects, carriers)
				if value.Op == token.AND && members != nil {
					configFailure = goRPCUseError(file, value, firstGoRPCBinding(members), "is exposed through a pointer to its containing config; avoid whole-config mutation or escape")
					return false
				}
				if value.Op == token.AND {
					if fields := goRPCCarrierFields(value.X, carriers); fields != nil {
						configFailure = goRPCUseError(file, value, firstGoRPCCarrierBinding(fields), "is exposed through a pointer to a config carrier; keep the carrier immutable and pass it only by value")
						return false
					}
				}
			case *ast.CallExpr:
				for index, argument := range value.Args {
					if goRPCAddressExpression(argument, configObjects, carriers) != nil {
						continue
					}
					members := goRPCConfigMembers(argument, configObjects, carriers)
					if members != nil && !goRPCSafeConfigCall(file, value, index, members, callTargets) {
						configFailure = goRPCUseError(file, argument, firstGoRPCBinding(members), "escapes through an unknown whole-config call; pass only the required non-RPC fields")
						return false
					}
				}
				for _, argument := range value.Args {
					if identifier, ok := goRPCUnparen(argument).(*ast.Ident); ok && carriers[identifier.Obj] != nil {
						configFailure = goRPCUseError(file, argument, firstGoRPCCarrierBinding(carriers[identifier.Obj]), "escapes through an unknown config-carrier call; pass only the required non-config fields")
						return false
					}
				}
				if selector, ok := value.Fun.(*ast.SelectorExpr); ok {
					if members := goRPCConfigMembers(selector.X, configObjects, carriers); members != nil {
						configFailure = goRPCUseError(file, selector.X, firstGoRPCBinding(members), "escapes through an unknown whole-config method call; use direct field checks without mutating the config")
						return false
					}
					if fields := goRPCCarrierFields(selector.X, carriers); fields != nil {
						configFailure = goRPCUseError(file, selector.X, firstGoRPCCarrierBinding(fields), "escapes through an unknown config-carrier method call; keep config carriers immutable")
						return false
					}
				}
			case *ast.CompositeLit:
				allowedFields := configStructFields[goRPCTypeKey(file, value.Type)]
				for _, element := range value.Elts {
					if keyed, ok := element.(*ast.KeyValueExpr); ok {
						members := goRPCConfigMembers(keyed.Value, configObjects, carriers)
						if members == nil {
							continue
						}
						name, named := keyed.Key.(*ast.Ident)
						if named && allowedFields[name.Name] {
							continue
						}
						configFailure = goRPCUseError(file, keyed.Value, firstGoRPCBinding(members), "escapes through an unrecognized whole-config composite; use a declared in-module config field")
						return false
					}
					if expression, ok := element.(ast.Expr); ok {
						if members := goRPCConfigMembers(expression, configObjects, carriers); members != nil {
							configFailure = goRPCUseError(file, expression, firstGoRPCBinding(members), "escapes through an unrecognized whole-config composite; use a keyed declared in-module config field")
							return false
						}
					}
				}
			}
		return true
	})
	if configFailure != nil {
		return configFailure
	}

	for binding, node := range mutations {
		return goRPCUseError(file, node, binding, "is mutated after aliasing; keep disabled RPC configuration immutable")
	}

	seen := make(map[ast.Node]bool)
	var failure error
	ast.Inspect(function.Body, func(node ast.Node) bool {
		if failure != nil || node == nil || exempt[node] || seen[node] {
			return failure == nil
		}
		binding := goRPCResolveBinding(node, configObjects, carriers, aliases)
		if binding == nil {
			if call, ok := node.(*ast.CallExpr); ok {
				if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "FieldByName" && len(call.Args) == 1 {
					if fieldName := goStringLiteral(call.Args[0]); fieldName != "" {
						for _, members := range bindingsByType {
							if reflected := members[fieldName]; reflected != nil {
								failure = goRPCUseError(file, node, reflected, "is accessed through reflection; use direct field access behind a recognized guard")
								return false
							}
						}
					}
				}
			}
			if selector, ok := node.(*ast.SelectorExpr); ok {
				for _, members := range bindingsByType {
					if unresolved := members[selector.Sel.Name]; unresolved != nil {
						failure = goRPCUseError(file, node, unresolved, "has an unresolved receiver; pass the declared config field directly or through a local alias")
						return false
					}
				}
			}
			return true
		}
		seen[node] = true
		if goRPCAssignmentTarget(node, parents) {
			failure = goRPCUseError(file, node, binding, "is mutated; keep disabled RPC configuration immutable")
			return false
		}
		if condition := goRPCContainingCondition(node, parents); condition != nil {
			value, referenced := evaluateGoRPCBindingCondition(condition, binding, configObjects, carriers, aliases, helpers, !packageSymbols["len"])
			if referenced && value == goRPCBlankFalse {
				return true
			}
			failure = goRPCUseError(file, node, binding, "has an unknown guard; use a direct blank-config predicate or a locally proven helper")
			return false
		}
		if goRPCUseGuarded(node, binding, parents, configObjects, carriers, aliases, helpers, !packageSymbols["len"]) {
			states[binding] = "guarded"
			return true
		}
		if goRPCEscapes(node, parents) {
			failure = goRPCUseError(file, node, binding, "escapes through an unrecognized value path; guard the concrete use directly")
		} else {
			failure = goRPCUseError(file, node, binding, "has an unguarded executable use; wrap every use in a blank-config guard")
		}
		return false
	})
	return failure
}

func goRPCAddressExpression(expression ast.Expr, objects map[*ast.Object]map[string]*goRPCBinding, carriers map[*ast.Object]map[string]map[string]*goRPCBinding) map[string]*goRPCBinding {
	expression = goRPCUnparen(expression)
	address, ok := expression.(*ast.UnaryExpr)
	if !ok || address.Op != token.AND {
		return nil
	}
	return goRPCConfigMembers(address.X, objects, carriers)
}

func goRPCSafeConfigCall(file goRPCSourceFile, call *ast.CallExpr, argumentIndex int, members map[string]*goRPCBinding, targets map[string]goRPCConfigCallTarget) bool {
	key := ""
	var object *ast.Object
	switch function := call.Fun.(type) {
	case *ast.Ident:
		key = file.directory + "\x00" + function.Name
		object = function.Obj
	case *ast.SelectorExpr:
		qualifier, ok := function.X.(*ast.Ident)
		if !ok {
			return false
		}
		directory := goRPCImportDirectory(file, file.imports[qualifier.Name])
		key = directory + "\x00" + function.Sel.Name
	}
	target, found := targets[key]
	if !found || (object != nil && object != target.object) {
		return false
	}
	return target.parameters[argumentIndex] == firstGoRPCBinding(members)
}

func firstGoRPCBinding(members map[string]*goRPCBinding) *goRPCBinding {
	var first *goRPCBinding
	for _, binding := range members {
		if first == nil || binding.yamlKey < first.yamlKey {
			first = binding
		}
	}
	return first
}

func firstGoRPCCarrierBinding(fields map[string]map[string]*goRPCBinding) *goRPCBinding {
	var first *goRPCBinding
	for _, members := range fields {
		binding := firstGoRPCBinding(members)
		if binding != nil && (first == nil || binding.yamlKey < first.yamlKey) {
			first = binding
		}
	}
	return first
}

func goRPCCarrierFields(expression ast.Expr, carriers map[*ast.Object]map[string]map[string]*goRPCBinding) map[string]map[string]*goRPCBinding {
	switch value := expression.(type) {
	case *ast.Ident:
		return carriers[value.Obj]
	case *ast.ParenExpr:
		return goRPCCarrierFields(value.X, carriers)
	case *ast.StarExpr:
		return goRPCCarrierFields(value.X, carriers)
	}
	return nil
}

func goRPCCompositeCarrier(file goRPCSourceFile, expression ast.Expr, configStructFields map[string]map[string]bool, objects map[*ast.Object]map[string]*goRPCBinding, carriers map[*ast.Object]map[string]map[string]*goRPCBinding) map[string]map[string]*goRPCBinding {
	expression = goRPCUnparen(expression)
	if address, ok := expression.(*ast.UnaryExpr); ok && address.Op == token.AND {
		expression = goRPCUnparen(address.X)
	}
	literal, ok := expression.(*ast.CompositeLit)
	if !ok {
		return nil
	}
	allowed := configStructFields[goRPCTypeKey(file, literal.Type)]
	if allowed == nil {
		return nil
	}
	result := make(map[string]map[string]*goRPCBinding)
	for _, element := range literal.Elts {
		keyed, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		name, ok := keyed.Key.(*ast.Ident)
		if !ok || !allowed[name.Name] {
			continue
		}
		if members := goRPCConfigMembers(keyed.Value, objects, carriers); members != nil {
			result[name.Name] = members
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func goRPCTypeKey(file goRPCSourceFile, expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.Ident:
		return file.directory + "\x00" + value.Name
	case *ast.StarExpr:
		return goRPCTypeKey(file, value.X)
	case *ast.SelectorExpr:
		qualifier, ok := value.X.(*ast.Ident)
		if !ok {
			return ""
		}
		importPath := file.imports[qualifier.Name]
		if directory := goRPCImportDirectory(file, importPath); directory != "" {
			return directory + "\x00" + value.Sel.Name
		}
	}
	return ""
}

func goRPCImportDirectory(file goRPCSourceFile, importPath string) string {
	if importPath == "" {
		return ""
	}
	for directory := file.directory; ; directory = filepath.Dir(directory) {
		moduleFile := filepath.Join(directory, "go.mod")
		contents, err := os.ReadFile(moduleFile)
		if err == nil {
			fields := strings.Fields(string(contents))
			for index := 0; index+1 < len(fields); index++ {
				if fields[index] == "module" && strings.HasPrefix(importPath, fields[index+1]) {
					relative := strings.TrimPrefix(importPath, fields[index+1])
					return filepath.Join(directory, filepath.FromSlash(strings.TrimPrefix(relative, "/")))
				}
			}
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return ""
		}
	}
}

func goRPCConfigMembers(expression ast.Expr, objects map[*ast.Object]map[string]*goRPCBinding, carriers map[*ast.Object]map[string]map[string]*goRPCBinding) map[string]*goRPCBinding {
	switch value := expression.(type) {
	case *ast.Ident:
		return objects[value.Obj]
	case *ast.ParenExpr:
		return goRPCConfigMembers(value.X, objects, carriers)
	case *ast.StarExpr:
		return goRPCConfigMembers(value.X, objects, carriers)
	case *ast.UnaryExpr:
		if value.Op == token.AND {
			return goRPCConfigMembers(value.X, objects, carriers)
		}
	case *ast.SelectorExpr:
		if identifier, ok := goRPCUnparen(value.X).(*ast.Ident); ok && carriers[identifier.Obj] != nil {
			return carriers[identifier.Obj][value.Sel.Name]
		}
	}
	return nil
}

func goRPCUnparen(expression ast.Expr) ast.Expr {
	for {
		parenthesized, ok := expression.(*ast.ParenExpr)
		if !ok {
			return expression
		}
		expression = parenthesized.X
	}
}

func goRPCConfigIdentifier(expression ast.Expr) *ast.Ident {
	switch value := expression.(type) {
	case *ast.Ident:
		return value
	case *ast.ParenExpr:
		return goRPCConfigIdentifier(value.X)
	case *ast.StarExpr:
		return goRPCConfigIdentifier(value.X)
	case *ast.UnaryExpr:
		if value.Op == token.AND {
			return goRPCConfigIdentifier(value.X)
		}
	}
	return nil
}

func goRPCDirectConfigMembers(expression ast.Expr, objects map[*ast.Object]map[string]*goRPCBinding, carriers map[*ast.Object]map[string]map[string]*goRPCBinding) map[string]*goRPCBinding {
	if members := goRPCConfigMembers(expression, objects, carriers); members != nil {
		return members
	}
	if identifier := goRPCConfigIdentifier(expression); identifier != nil {
		return objects[identifier.Obj]
	}
	return nil
}

func goRPCResolveBinding(node ast.Node, configObjects map[*ast.Object]map[string]*goRPCBinding, carriers map[*ast.Object]map[string]map[string]*goRPCBinding, aliases map[*ast.Object]*goRPCBinding) *goRPCBinding {
	switch value := node.(type) {
	case *ast.Ident:
		return aliases[value.Obj]
	case *ast.ParenExpr:
		return goRPCResolveBinding(value.X, configObjects, carriers, aliases)
	case *ast.SelectorExpr:
		if members := goRPCDirectConfigMembers(value.X, configObjects, carriers); members != nil {
			return members[value.Sel.Name]
		}
	}
	return nil
}

func evaluateGoRPCBindingCondition(expression ast.Expr, binding *goRPCBinding, configObjects map[*ast.Object]map[string]*goRPCBinding, carriers map[*ast.Object]map[string]map[string]*goRPCBinding, aliases map[*ast.Object]*goRPCBinding, helpers map[string]goRPCGuardHelper, allowBuiltinLen bool) (goRPCBlankValue, bool) {
	if call, ok := expression.(*ast.CallExpr); ok && len(call.Args) == 1 {
		if identifier, ok := call.Fun.(*ast.Ident); ok {
			helper, found := helpers[identifier.Name]
			if found && helper.valid && (identifier.Obj == nil || identifier.Obj == helper.object) && goRPCResolveBinding(call.Args[0], configObjects, carriers, aliases) == binding {
				return goRPCBlankFalse, true
			}
		}
	}
	return evaluateGoRPCBlank(expression, func(value ast.Expr) (goRPCBlankValue, bool) {
		fields, ok := goRPCBindingFieldPath(value, binding, configObjects, carriers, aliases)
		if !ok {
			return goRPCBlankUnknown, false
		}
		switch strings.Join(fields, ".") {
		case "DiscovType", "Target", "Etcd.Key":
			return goRPCBlankString, true
		case "Endpoints", "Endpints", "Etcd.Hosts":
			return goRPCBlankList, true
		default:
			return goRPCBlankUnknown, true
		}
	}, allowBuiltinLen)
}

func goRPCBindingFieldPath(expression ast.Expr, binding *goRPCBinding, configObjects map[*ast.Object]map[string]*goRPCBinding, carriers map[*ast.Object]map[string]map[string]*goRPCBinding, aliases map[*ast.Object]*goRPCBinding) ([]string, bool) {
	if goRPCResolveBinding(expression, configObjects, carriers, aliases) == binding {
		return nil, true
	}
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return nil, false
	}
	path, ok := goRPCBindingFieldPath(selector.X, binding, configObjects, carriers, aliases)
	if !ok {
		return nil, false
	}
	return append(path, selector.Sel.Name), true
}

func goRPCContainingCondition(node ast.Node, parents map[ast.Node]ast.Node) ast.Expr {
	for current := node; parents[current] != nil; current = parents[current] {
		parent := parents[current]
		if statement, ok := parent.(*ast.IfStmt); ok && current == statement.Cond {
			return statement.Cond
		}
		if _, ok := parent.(*ast.FuncLit); ok {
			return nil
		}
	}
	return nil
}

func goRPCUseGuarded(node ast.Node, binding *goRPCBinding, parents map[ast.Node]ast.Node, configObjects map[*ast.Object]map[string]*goRPCBinding, carriers map[*ast.Object]map[string]map[string]*goRPCBinding, aliases map[*ast.Object]*goRPCBinding, helpers map[string]goRPCGuardHelper, allowBuiltinLen bool) bool {
	for current := node; parents[current] != nil; current = parents[current] {
		parent := parents[current]
		if _, ok := parent.(*ast.FuncLit); ok {
			return false
		}
		statement, ok := parent.(*ast.IfStmt)
		if !ok {
			continue
		}
		value, referenced := evaluateGoRPCBindingCondition(statement.Cond, binding, configObjects, carriers, aliases, helpers, allowBuiltinLen)
		if !referenced {
			continue
		}
		if current == statement.Body && value == goRPCBlankFalse {
			return true
		}
		if current == statement.Else && value == goRPCBlankTrue {
			return true
		}
	}
	return false
}

func goRPCAssignmentTarget(node ast.Node, parents map[ast.Node]ast.Node) bool {
	for current := node; parents[current] != nil; current = parents[current] {
		parent := parents[current]
		switch statement := parent.(type) {
		case *ast.AssignStmt:
			for _, left := range statement.Lhs {
				if current == left || (node.Pos() >= left.Pos() && node.End() <= left.End()) {
					return true
				}
			}
			return false
		case *ast.IncDecStmt:
			return true
		}
	}
	return false
}

func goRPCEscapes(node ast.Node, parents map[ast.Node]ast.Node) bool {
	for current := node; parents[current] != nil; current = parents[current] {
		switch parent := parents[current].(type) {
		case *ast.ReturnStmt, *ast.SendStmt, *ast.GoStmt, *ast.DeferStmt:
			return true
		case *ast.UnaryExpr:
			if parent.Op == token.AND {
				return true
			}
		case *ast.CallExpr:
			name := ""
			switch function := parent.Fun.(type) {
			case *ast.Ident:
				name = function.Name
			case *ast.SelectorExpr:
				name = function.Sel.Name
			}
			return !strings.Contains(strings.ToLower(name), "newclient")
		case ast.Stmt:
			return false
		}
	}
	return true
}

func goRPCUseError(file goRPCSourceFile, node ast.Node, binding *goRPCBinding, detail string) error {
	position := file.set.Position(node.Pos())
	return fmt.Errorf("RPC binding %q (%s.%s declared at %s:%d) %s at %s:%d", binding.yamlKey, binding.structName, binding.fieldName, binding.file, binding.line, detail, file.relative, position.Line)
}
