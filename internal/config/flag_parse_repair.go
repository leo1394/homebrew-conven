package config

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// FlagParseRepair is a proposed edit, never applied by repository analysis.
type FlagParseRepair struct {
	Service string
	Directory string
	Path string
	Line int
	message string
	source []byte
	offset int
	insert string
}

func (repair *FlagParseRepair) Error() string { return repair.message }

func missingFlagParseRepair(name, directory, path string, source []byte, files *token.FileSet, parsed *ast.File, main *ast.FuncDecl, read token.Pos, message string) *FlagParseRepair {
	standardFlag := false
	for _, entry := range parsed.Imports { if entry.Path.Value == `"flag"` && (entry.Name == nil || entry.Name.Name == "flag") { standardFlag = true } }
	if !standardFlag { return nil }
	unsafe := false
	ast.Inspect(parsed, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.ValueSpec:
			for _, identifier := range value.Names { if identifier.Name == "flag" { unsafe = true } }
		case *ast.AssignStmt:
			for _, expression := range value.Lhs { if identifier, ok := expression.(*ast.Ident); ok && identifier.Name == "flag" { unsafe = true } }
		case *ast.CallExpr:
			if isGoFlagCall(value, "Parse") { unsafe = true }
		}
		return true
	})
	if unsafe { return nil }
	var target ast.Stmt
	for _, statement := range main.Body.List {
		if statement.Pos() > read {
			ast.Inspect(statement, func(node ast.Node) bool {
				if call, ok := node.(*ast.CallExpr); ok { if selector, ok := call.Fun.(*ast.SelectorExpr); ok { if id, ok := selector.X.(*ast.Ident); ok && id.Name == "flag" { unsafe = true } } }
				return true
			})
		}
		if statement.Pos() <= read && read < statement.End() { target = statement }
	}
	if unsafe || target == nil { return nil }
	switch target.(type) { case *ast.ExprStmt, *ast.AssignStmt, *ast.DeclStmt: default: return nil }
	ast.Inspect(target, func(node ast.Node) bool {
		if _, ok := node.(*ast.FuncLit); ok { unsafe = true }
		if call, ok := node.(*ast.CallExpr); ok { if selector, ok := call.Fun.(*ast.SelectorExpr); ok { if id, ok := selector.X.(*ast.Ident); ok && id.Name == "flag" { unsafe = true } } }
		return true
	})
	if unsafe { return nil }
	offset := files.Position(target.Pos()).Offset
	lineStart := bytes.LastIndexByte(source[:offset], '\n') + 1
	indent := string(source[lineStart:offset])
	if strings.Trim(indent, " \t") != "" { return nil }
	newline := "\n"
	if bytes.Contains(source, []byte("\r\n")) { newline = "\r\n" }
	return &FlagParseRepair{Service: name, Directory: directory, Path: path, Line: files.Position(target.Pos()).Line, message: message, source: append([]byte(nil), source...), offset: offset, insert: "flag.Parse()"+newline+indent}
}

func (repair *FlagParseRepair) Apply() error {
	resolved, err := filepath.EvalSymlinks(repair.Path)
	if err != nil { return err }
	rootInfo, err := os.Lstat(repair.Directory)
	if err != nil { return err }
	if !rootInfo.IsDir() { return fmt.Errorf("repository must be a real directory: %s", repair.Directory) }
	root, err := filepath.EvalSymlinks(repair.Directory)
	if err != nil { return err }
	relative, err := filepath.Rel(repair.Directory, repair.Path)
	if err != nil { return err }
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || resolved != filepath.Join(root, relative) { return fmt.Errorf("refusing to edit symlinked source: %s", repair.Path) }
	info, err := os.Lstat(repair.Path)
	if err != nil { return err }
	if !info.Mode().IsRegular() { return fmt.Errorf("source must be a regular file: %s", repair.Path) }
	current, err := os.ReadFile(repair.Path)
	if err != nil { return err }
	if !bytes.Equal(current, repair.source) { return fmt.Errorf("source changed after confirmation was requested: %s; retry services --update", repair.Path) }
	updated := string(current[:repair.offset])+repair.insert+string(current[repair.offset:])
	if _, err := parser.ParseFile(token.NewFileSet(), repair.Path, updated, 0); err != nil { return err }
	temporary, err := os.CreateTemp(filepath.Dir(repair.Path), ".conven-flag-*")
	if err != nil { return err }
	defer os.Remove(temporary.Name())
	if err := temporary.Chmod(info.Mode().Perm()); err != nil { temporary.Close(); return err }
	if _, err := temporary.WriteString(updated); err != nil { temporary.Close(); return err }
	if err := temporary.Close(); err != nil { return err }
	current, err = os.ReadFile(repair.Path)
	if err != nil { return err }
	if !bytes.Equal(current, repair.source) { return fmt.Errorf("source changed before repair: %s", repair.Path) }
	return os.Rename(temporary.Name(), repair.Path)
}
