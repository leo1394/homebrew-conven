package config

import (
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"strings"
)

type rpcTargetGuardError struct {
	message string
	path string
	line int
	start, end int
	receiverStart, receiverEnd int
	repairable bool
	source []byte
}

func (err *rpcTargetGuardError) Error() string { return err.message }

// InspectGoRPCTargetInitialization checks binding-dependent guards around
// client construction using the target-only configuration generated locally.
func InspectGoRPCTargetInitialization(directory, workdir, binding string) error {
	if _, err := InspectGoRPCClientBindings(directory, workdir); err != nil { return err }
	root := workdir
	if !filepath.IsAbs(root) { root = filepath.Join(directory, root) }
	files, declarations, err := scanGoRPCModule(directory, root, map[string]bool{binding: true})
	if err != nil { return err }
	byType := make(map[string]map[string]*goRPCBinding)
	for _, declaration := range declarations {
		key := declaration.directory + "\x00" + declaration.structName
		if byType[key] == nil { byType[key] = make(map[string]*goRPCBinding) }
		byType[key][declaration.fieldName] = declaration
	}
	symbols := collectGoRPCPackageSymbols(files)
	helpers := collectGoRPCGuardHelpers(files, symbols)
	fields := collectGoRPCConfigStructFields(files, declarations)
	for _, file := range files {
		for _, declaration := range file.parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil { continue }
			if err := inspectGoRPCFunction(file, function, byType, helpers[file.directory], symbols[file.directory], nil, fields, nil, true); err != nil { return err }
		}
	}
	return nil
}

func goRPCTargetFieldValue(fields []string, referenced bool) (goRPCBlankValue, bool) {
	if !referenced { return goRPCBlankUnknown, false }
	switch strings.Join(fields, ".") {
	case "Target": return goRPCNonemptyString, true
	case "DiscovType", "Etcd.Key", "Consul.Key", "Consul.Host": return goRPCBlankString, true
	case "Endpoints", "Endpints", "Etcd.Hosts": return goRPCBlankList, true
	default: return goRPCBlankUnknown, true
	}
}

func inspectGoRPCTargetUses(file goRPCSourceFile, function *ast.FuncDecl, parents map[ast.Node]ast.Node, objects map[*ast.Object]map[string]*goRPCBinding, carriers map[*ast.Object]map[string]map[string]*goRPCBinding, aliases map[*ast.Object]*goRPCBinding, helpers map[string]goRPCGuardHelper, allowLen bool) error {
	var failure error
	ast.Inspect(function.Body, func(node ast.Node) bool {
		if failure != nil { return false }
		call, ok := node.(*ast.CallExpr)
		if !ok || goRPCContainingCondition(call, parents) != nil { return true }
		for _, argument := range call.Args {
			binding := goRPCResolveBinding(argument, objects, carriers, aliases)
			if binding == nil { continue }
			for current := ast.Node(call); parents[current] != nil; current = parents[current] {
				guard, ok := parents[current].(*ast.IfStmt)
				if !ok || (current != guard.Body && current != guard.Else) { continue }
				value, referenced := evaluateGoRPCBlank(guard.Cond, func(expression ast.Expr) (goRPCBlankValue, bool) {
					if predicate, ok := expression.(*ast.CallExpr); ok && len(predicate.Args) == 1 {
						if name, ok := predicate.Fun.(*ast.Ident); ok && goRPCResolveBinding(predicate.Args[0], objects, carriers, aliases) == binding {
							if helper, found := helpers[name.Name]; found && (name.Obj == nil || name.Obj == helper.object) { return helper.target, true }
						}
					}
					fields, found := goRPCBindingFieldPath(expression, binding, objects, carriers, aliases)
					return goRPCTargetFieldValue(fields, found)
				}, allowLen)
				if !referenced { continue }
				if (current == guard.Body && value == goRPCBlankTrue) || (current == guard.Else && value == goRPCBlankFalse) { continue }
				position := file.set.Position(guard.Cond.Pos())
				diagnostic := &rpcTargetGuardError{message: fmt.Sprintf("RPC binding %q local target initialization is blocked or cannot be proven at %s:%d; the generated route sets Target and leaves DiscovType empty; include %s.Target != \"\" in the client initialization condition", binding.yamlKey, file.relative, position.Line, binding.fieldName), path: file.path, line: position.Line}
				// Only extend a plain positive discovery guard, without an else or initializer.
				if condition, ok := guard.Cond.(*ast.BinaryExpr); ok && condition.Op == token.NEQ && guard.Init == nil && guard.Else == nil {
					if selector, ok := condition.X.(*ast.SelectorExpr); ok && selector.Sel.Name == "DiscovType" && goRPCResolveBinding(selector.X, objects, carriers, aliases) == binding {
						if literal, ok := condition.Y.(*ast.BasicLit); ok && literal.Kind == token.STRING && literal.Value == "\"\"" {
							diagnostic.repairable = true
							diagnostic.source = file.source
							diagnostic.start = file.set.Position(guard.Cond.Pos()).Offset
							diagnostic.end = file.set.Position(guard.Cond.End()).Offset
							diagnostic.receiverStart = file.set.Position(selector.X.Pos()).Offset
							diagnostic.receiverEnd = file.set.Position(selector.X.End()).Offset
						}
					}
				}
				failure = diagnostic
				return false
			}
		}
		return true
	})
	return failure
}
