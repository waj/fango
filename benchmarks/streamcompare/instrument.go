package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// Instrument only generated artifacts, after linking the uninstrumented
// measurement binary. Counter runs are never included in timing samples.
func instrument(root string) error {
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fs := token.NewFileSet()
		f, e := parser.ParseFile(fs, path, nil, parser.ParseComments)
		if e != nil {
			return e
		}
		changed := false
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			var target ast.Expr
			if strings.HasPrefix(fn.Name.Name, "MachineFrame_") {
				target = &ast.SelectorExpr{X: ast.NewIdent("fangort"), Sel: ast.NewIdent("ProbeFrames")}
			}
			if f.Name.Name == "fangort" && (fn.Name.Name == "ImmediateMachine" || fn.Name.Name == "SuspendMachine") {
				target = ast.NewIdent("ProbeFrames")
			}
			if target != nil {
				fn.Body.List = append([]ast.Stmt{&ast.IncDecStmt{X: target, Tok: token.INC}}, fn.Body.List...)
				changed = true
			}
			if f.Name.Name == "fangort" && fn.Name.Name == "ExtendEvidenceRow" {
				count := &ast.IfStmt{Cond: &ast.BinaryExpr{X: &ast.CallExpr{Fun: ast.NewIdent("len"), Args: []ast.Expr{ast.NewIdent("bindings")}}, Op: token.NEQ, Y: &ast.BasicLit{Kind: token.INT, Value: "0"}}, Body: &ast.BlockStmt{List: []ast.Stmt{&ast.IncDecStmt{X: ast.NewIdent("ProbeRows"), Tok: token.INC}}}}
				fn.Body.List = append([]ast.Stmt{count}, fn.Body.List...)
				changed = true
			}
			if f.Name.Name == "fangort" && fn.Name.Name == "runLocal" {
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					block, ok := n.(*ast.BlockStmt)
					if !ok {
						return true
					}
					for i, s := range block.List {
						a, ok := s.(*ast.IncDecStmt)
						if !ok {
							continue
						}
						sel, ok := a.X.(*ast.SelectorExpr)
						if !ok || sel.Sel.Name != "Steps" {
							continue
						}
						list := append([]ast.Stmt(nil), block.List[:i]...)
						list = append(list, &ast.IncDecStmt{X: ast.NewIdent("ProbeSteps"), Tok: token.INC})
						block.List = append(list, block.List[i:]...)
						changed = true
						break
					}
					return true
				})
			}
		}
		if !changed {
			return nil
		}
		var out bytes.Buffer
		if e = format.Node(&out, fs, f); e != nil {
			return e
		}
		return os.WriteFile(path, out.Bytes(), 0644)
	})
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(root, "fangort", "probe_counters.go"), []byte("package fangort\nvar ProbeSteps, ProbeFrames, ProbeRows uint64\n"), 0644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, "entries", "probe", "counter_test.go"), []byte(`package main
import "fangobuild/fangort"
func init(){readCounters=func()(uint64,uint64,uint64){return fangort.ProbeSteps,fangort.ProbeFrames,fangort.ProbeRows}}
`), 0644)
}

func profileName(b build, c int) string {
	return filepath.Join(b.Root, fmt.Sprintf("%s.allocs", names[c]))
}
