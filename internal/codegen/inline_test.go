package codegen

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestReturnCallInliningPreservesEvaluationAndFunctionBoundaries(t *testing.T) {
	const source = `package main
import "fmt"
var order []int
func mark(n int) int { order = append(order, n); return n }
func shadow(x int) int {
 return func(x, y int) int { return func() int { return x*10+y }() }(mark(x+1), mark(x))
}
func boxed() any { return func() int64 { return 7 }() }
func deferred() (n int) {
 return func() int { defer func(){ order = append(order, 9) }(); return 3 }()
}
func recovered() any { return func() any { return recover() }() }
func captures() []func() int {
 var values []func() int
 for i := 0; i < 3; i++ {
  values = append(values, func() func() int {
   return func(value int) func() int { return func() int { return value } }(i)
  }())
 }
 return values
}
func main() {
 fmt.Println(shadow(2), order)
 fmt.Printf("%T %v\n", boxed(), boxed())
 fmt.Println(deferred(), order)
 values := captures()
 fmt.Println(values[0](), values[1](), values[2](), recovered())
}`
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	g := &gen{}
	g.inlineReturnCalls(file)
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "shadow" {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			if _, ok := node.(*ast.FuncLit); ok {
				t.Error("nested return calls in shadow were not flattened")
			}
			return true
		})
	}
	var output bytes.Buffer
	if err := format.Node(&output, token.NewFileSet(), file); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(path, output.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := exec.Command("go", "run", path).CombinedOutput()
	if err != nil {
		t.Fatalf("running flattened Go: %v\n%s\n%s", err, got, output.Bytes())
	}
	const want = "32 [3 2]\nint64 7\n3 [3 2 9]\n0 1 2 <nil>\n"
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
