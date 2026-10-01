package main

import (
	"bytes"
	goast "go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/waj/fango/internal/build"
	"github.com/waj/fango/internal/codegen"
)

func TestLoweredFoldHasNoIntermediateCallable(t *testing.T) {
	files := emittedProject(t, filepath.Join("..", "..", "testdata", "run", "backend_lowering.fango"))
	file, err := parser.ParseFile(token.NewFileSet(), "List.go", generatedFile(t, files, "modules/List/module.go"), 0)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, decl := range file.Decls {
		fn, ok := decl.(*goast.FuncDecl)
		if !ok || (fn.Name.Name != "V_List_dot_foldl" && fn.Name.Name != "V_List_dot_foldl_exit") {
			continue
		}
		found++
		flat := false
		for _, param := range fn.Type.Params.List {
			for _, name := range param.Names {
				if name.Name == "v_combine" {
					typ, ok := param.Type.(*goast.FuncType)
					flat = ok && len(typ.Params.List) == 3
				}
			}
		}
		if !flat {
			t.Fatalf("%s lacks saturated callback parameter", fn.Name.Name)
		}
		goast.Inspect(fn.Body, func(n goast.Node) bool {
			if _, ok := n.(*goast.FuncLit); ok {
				t.Errorf("%s constructs a closure in its loop", fn.Name.Name)
			}
			if sel, ok := n.(*goast.SelectorExpr); ok {
				if id, ok := sel.X.(*goast.Ident); ok && id.Name == "v_combine" {
					t.Errorf("%s selects a callback member", fn.Name.Name)
				}
			}
			return true
		})
	}
	if found != 2 {
		t.Fatalf("found %d fold workers", found)
	}
}

func TestLoweredCallbacksAllocateNoObjectsPerElement(t *testing.T) {
	if testing.Short() {
		t.Skip("compiled allocation check")
	}
	source := filepath.Join("..", "..", "benchmarks", "loweringcompare", "workload.fango")
	project := filepath.Join(t.TempDir(), "generated")
	cmd := exec.Command(cliBinary(t), "build", "--emit-go", "-o", project, source)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("emit: %v\n%s", err, out)
	}
	test := `package main
import "testing"
var allocationSink int64
func TestAllocationSlope(t *testing.T) {
 small, large := V_input(1), V_input(1000)
 for _, probe := range []struct{name string; run func() int64; want int64}{
  {"small fold", func() int64{return V_fold(small)}, 1},
  {"large fold", func() int64{return V_fold(large)}, 500500},
  {"unary loop", func() int64{return V_unary(1000)}, 1000},
 } {
  allocations := testing.AllocsPerRun(20, func(){allocationSink=probe.run()})
  if allocationSink != probe.want || allocations != 0 {t.Fatalf("%s: checksum %d want %d; allocations %.0f want 0",probe.name,allocationSink,probe.want,allocations)}
 }
}
`
	if err := os.WriteFile(filepath.Join(project, "entries", "workload", "allocation_test.go"), []byte(test), 0600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("go", "test", "-count=1", "./entries/workload")
	cmd.Dir = project
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("allocation check: %v\n%s", err, out)
	}
}

func TestOptimizedAndGeneralLoweringAgree(t *testing.T) {
	if testing.Short() {
		t.Skip("compiled lowering comparison")
	}
	source := filepath.Join("..", "..", "testdata", "run", "backend_lowering.fango")
	var diagnostics bytes.Buffer
	result, ok := checkGraph(source, &diagnostics, &compilationSession{noCache: true})
	if !ok {
		t.Fatal(diagnostics.String())
	}
	var units []codegen.Unit
	for _, unit := range result.Graph.Units {
		units = append(units, codegen.Unit{Name: unit.Name, Program: unit.Program, Imports: unit.Imports, Entry: unit.Entry})
	}
	native := emittedProject(t, source)
	runtime, err := build.RuntimeFiles()
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("..", "..", "testdata", "run", "backend_lowering.expected"))
	if err != nil {
		t.Fatal(err)
	}
	for _, disabled := range []bool{false, true} {
		result.Program.DisableOptimizations = disabled
		files, err := codegen.EmitProject(result.Program, result.Checker.B, units, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range native {
			if len(file.Path) >= 7 && file.Path[:7] == "native/" {
				files = append(files, file)
			}
		}
		files = append(files, runtime...)
		project := t.TempDir()
		for _, file := range files {
			if _, err := build.WriteIfChanged(filepath.Join(project, file.Path), file.Data); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command("go", "run", "./entries/backend_lowering")
		cmd.Dir = project
		out, err := cmd.CombinedOutput()
		if err != nil || !bytes.Equal(out, want) {
			t.Fatalf("disabled=%t: %v\n%s\nwant %s", disabled, err, out, want)
		}
	}
}
