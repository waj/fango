package main

import (
	"bytes"
	goast "go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegexLiteralsAreModuleGlobals(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "testdata", "run", "stdlib_regex.fango")
	first := emittedProject(t, path)
	second := emittedProject(t, path)
	if len(first) != len(second) {
		t.Fatal("nondeterministic project size")
	}
	for i := range first {
		if first[i].Path != second[i].Path || !bytes.Equal(first[i].Data, second[i].Data) {
			t.Fatalf("nondeterministic file %s", first[i].Path)
		}
	}
	data := entryFile(t, first)
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", data, 0)
	if err != nil {
		t.Fatal(err)
	}
	initializers := 0
	for _, declaration := range file.Decls {
		function, isFunction := declaration.(*goast.FuncDecl)
		goast.Inspect(declaration, func(node goast.Node) bool {
			call, ok := node.(*goast.CallExpr)
			if !ok {
				return true
			}
			selection, ok := call.Fun.(*goast.SelectorExpr)
			if !ok || selection.Sel.Name != "RegexLiteral" {
				return true
			}
			if isFunction {
				t.Errorf("literal initialization inside function %s", function.Name)
			}
			initializers++
			return true
		})
	}
	if initializers == 0 {
		t.Fatal("no literal globals emitted")
	}
	if strings.Count(string(data), `RegexLiteral("(?P<word>é二)(x)?()")`) != 1 {
		t.Fatal("function literal was not initialized exactly once")
	}
}
