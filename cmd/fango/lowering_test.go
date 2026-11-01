package main

import (
	"bytes"
	goast "go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// Local's read resumes with its own snapshot, and the Cell members it hands
// out forward to its Direct activation: neither a store nor the Exit view
// survives in the generated module.
func TestLocalCellReadsStateDirectly(t *testing.T) {
	files := emittedProject(t, filepath.Join("..", "..", "testdata", "run", "reader_short_pulls.fango"))
	file, err := parser.ParseFile(token.NewFileSet(), "Local.go", generatedFile(t, files, "modules/Runtime/Local/module.go"), 0)
	if err != nil {
		t.Fatal(err)
	}
	reads := 0
	goast.Inspect(file, func(n goast.Node) bool {
		if field, ok := n.(*goast.KeyValueExpr); ok {
			if key, ok := field.Key.(*goast.Ident); ok && key.Name == "Op_Runtime_dot_Local_dot_readState" {
				if fn, ok := field.Value.(*goast.FuncLit); ok {
					reads++
					goast.Inspect(fn.Body, func(n goast.Node) bool {
						if sel, ok := n.(*goast.SelectorExpr); ok && sel.Sel.Name == "Store" {
							t.Errorf("readState commits its unchanged snapshot")
						}
						return true
					})
				}
			}
		}
		if sel, ok := n.(*goast.SelectorExpr); ok && strings.HasPrefix(sel.Sel.Name, "Op_") {
			if view, ok := sel.X.(*goast.SelectorExpr); ok && view.Sel.Name == "Exit" {
				if id, ok := view.X.(*goast.Ident); ok && strings.HasPrefix(id.Name, "ev") && !strings.HasPrefix(id.Name, "ev_") {
					t.Errorf("bound member calls %s.Exit.%s", id.Name, sel.Sel.Name)
				}
			}
		}
		return true
	})
	if reads == 0 {
		t.Fatal("no readState clause found")
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

func TestStreamFoldAllocatesNoObjectsPerElement(t *testing.T) {
	if testing.Short() {
		t.Skip("compiled allocation check")
	}
	source := filepath.Join("..", "..", "benchmarks", "existentialcompare", "workload.fango")
	project := filepath.Join(t.TempDir(), "generated")
	cmd := exec.Command(cliBinary(t), "build", "--emit-go", "-o", project, source)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("emit: %v\n%s", err, out)
	}
	test := `package main
import "testing"
var streamAllocationSink int64
func TestStreamFoldAllocationSlope(t *testing.T) {
 for _, n := range []int64{0, 1, 10000} {
  source := V__storage_scalar(false, n)
  allocations := testing.AllocsPerRun(20, func(){streamAllocationSink=V__storage_sum[int64](nil, source)})
  want := n*(n+1)/2
  if streamAllocationSink != want || allocations != 0 {
   t.Fatalf("n=%d: checksum %d want %d; allocations %.0f want 0",n,streamAllocationSink,want,allocations)
  }
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

// Stream.takeStep's annotation fixes its counter to Int. Inferred alone it
// generalizes over Num and Ord, and every step would compare and decrement
// through class dictionaries.
func TestStreamTakeCountsWithoutDictionaries(t *testing.T) {
	files := emittedProject(t, filepath.Join("..", "..", "benchmarks", "existentialcompare", "workload.fango"))
	file, err := parser.ParseFile(token.NewFileSet(), "Stream.go", generatedFile(t, files, "modules/Stream/module.go"), 0)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, decl := range file.Decls {
		fn, ok := decl.(*goast.FuncDecl)
		if !ok || (fn.Name.Name != "V_Stream_dot_takeStep" && fn.Name.Name != "V_Stream_dot_takeStep_exit") {
			continue
		}
		found++
		var params bytes.Buffer
		for _, param := range fn.Type.Params.List {
			goast.Fprint(&params, token.NewFileSet(), param.Type, nil)
		}
		if strings.Contains(params.String(), "_dictionary_") {
			t.Errorf("%s receives class dictionaries", fn.Name.Name)
		}
		if !strings.Contains(params.String(), "int64") {
			t.Errorf("%s does not count with int64", fn.Name.Name)
		}
	}
	if found != 2 {
		t.Fatalf("found %d take workers", found)
	}
}

func TestOptimizedAndGeneralLoweringAgree(t *testing.T) {
	testLoweringAgreement(t, "backend_lowering")
}

func TestClosedAsyncCallbackBodyIsShared(t *testing.T) {
	files := emittedProject(t, filepath.Join("..", "..", "testdata", "run", "async_callback_sharing_io.fango"))
	source := string(entryFile(t, files))
	for _, literal := range []string{`"Hola"`, `"Chau"`, `"unit"`} {
		if count := strings.Count(source, literal); count != 1 {
			t.Errorf("callback body literal %s emitted %d times, want once:\n%s", literal, count, source)
		}
	}
	testLoweringAgreement(t, "async_callback_sharing_io")
}

func TestEffectBodyStrategyAgrees(t *testing.T) {
	testLoweringStrategies(t, "async_callback_sharing", "factory")
}

func testLoweringAgreement(t *testing.T, stem string) {
	testLoweringStrategies(t, stem, "")
}

func testLoweringStrategies(t *testing.T, stem, strategy string) {
	t.Helper()
	if testing.Short() {
		t.Skip("compiled lowering comparison")
	}
	source := filepath.Join("..", "..", "testdata", "run", stem+".fango")
	var diagnostics bytes.Buffer
	result, ok := checkGraph(source, &diagnostics, &compilationSession{noCache: true})
	if !ok {
		t.Fatal(diagnostics.String())
	}
	var units []codegen.Unit
	for _, unit := range result.Graph.Units {
		units = append(units, codegen.Unit{Name: unit.Name, Program: unit.Program, Imports: unit.Imports, Entry: unit.Entry, Source: unit.Source, BodyStrategy: strategy})
	}
	native := emittedProject(t, source)
	runtime, err := build.RuntimeFiles(nil)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("..", "..", "testdata", "run", stem+".expected"))
	if err != nil {
		t.Fatal(err)
	}
	for _, disabled := range []bool{false, true} {
		result.Program.DisableOptimizations = disabled
		files, err := codegen.EmitProject(result.Program, result.Checker.B, units)
		if err != nil {
			t.Fatal(err)
		}
		if !disabled && strategy != "" && stem == "async_callback_sharing" {
			limit := 2
			for _, literal := range []string{`"Hola"`, `"Chau"`, `"unit"`} {
				if count := strings.Count(string(entryFile(t, files)), literal); count < 1 || count > limit {
					t.Errorf("%s: %s emitted %d times, limit %d", strategy, literal, count, limit)
				}
			}
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
		cmd := exec.Command("go", "run", "./entries/"+stem)
		cmd.Dir = project
		out, err := cmd.CombinedOutput()
		if err != nil || !bytes.Equal(out, want) {
			t.Fatalf("disabled=%t: %v\n%s\nwant %s", disabled, err, out, want)
		}
	}
}

func TestSaturatedMethodCallAllocations(t *testing.T) {
	if testing.Short() {
		t.Skip("compiled allocation check")
	}
	source := filepath.Join("..", "..", "benchmarks", "dictcompare", "generic.fango")
	project := filepath.Join(t.TempDir(), "generated")
	cmd := exec.Command(cliBinary(t), "build", "--emit-go", "-o", project, source)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("emit: %v\n%s", err, out)
	}
	test, err := os.ReadFile(filepath.Join("..", "..", "benchmarks", "dictcompare", "generic_test.go.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "entries", "generic", "measure_test.go"), test, 0600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("go", "test", "-count=1", "-run", "TestSaturatedMethodsDoNotAllocate", "./entries/generic")
	cmd.Dir = project
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestHiddenPackageAllocations(t *testing.T) {
	if testing.Short() {
		t.Skip("compiled allocation check")
	}
	source := filepath.Join("..", "..", "benchmarks", "existentialcompare", "compiler.fango")
	project := filepath.Join(t.TempDir(), "generated")
	cmd := exec.Command(cliBinary(t), "build", "--emit-go", "-o", project, source)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("emit: %v\n%s", err, out)
	}
	test, err := os.ReadFile(filepath.Join("..", "..", "benchmarks", "existentialcompare", "compiler_test.go.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "entries", "compiler", "allocation_test.go"), test, 0600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("go", "test", "-count=1", "./entries/compiler")
	cmd.Dir = project
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("allocation check: %v\n%s", err, out)
	}
}

func TestSaturatedMethodCallsUseDictionaryEntries(t *testing.T) {
	source := filepath.Join(t.TempDir(), "probe.fango")
	program := `class Mix a
    mix : a -> a -> a
    stepper : a -> a -> a
    audit : a -> a ->{Console} a
instance Mix Int
    mix x y = x + y
    stepper x =
        doubled = x * 2
        { y -> doubled + y }
    audit x y = x + y
atoms : Mix a => a -> a -> a
atoms x y = mix x y
computed : Mix a => a -> a -> a
computed x y = stepper x (stepper y x)
partial : Mix a => a -> a -> a
partial x =
    applied = mix x
    applied
effectful : Mix a => a -> a ->{Console} a
effectful x y = audit x y
one : Int
one = 1
main() = print (atoms one 2 + computed one 2 + partial one 2 + effectful one 2)
`
	if err := os.WriteFile(source, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	src := string(generatedFile(t, emittedProject(t, source), "entries/probe/main.go"))
	body := func(name string) string {
		start := strings.Index(src, "func "+name+"[")
		if start < 0 {
			t.Fatalf("no %s:\n%s", name, src)
		}
		end := strings.Index(src[start:], "\n}\n")
		return src[start : start+end]
	}
	if got := body("V_atoms"); !strings.Contains(got, ".M0(v_x, v_y)") || strings.Contains(got, ".Direct(") {
		t.Errorf("saturated call does not use the entry:\n%s", got)
	}
	// The inner call's arguments are atoms. The outer call's later argument is
	// not, and must run after the method's first application.
	if got := body("V_computed"); !strings.Contains(got, ".Direct(v_x).Direct(") || !strings.Contains(got, ".M1(v_y, v_x)") {
		t.Errorf("computed argument moved across an intermediate application:\n%s", got)
	}
	if got := body("V_partial"); strings.Contains(got, ".M0(") {
		t.Errorf("partial application uses the entry:\n%s", got)
	}
	if got := body("V_effectful"); strings.Contains(got, ".M2") {
		t.Errorf("effectful method has an entry:\n%s", got)
	}
}

func TestAbortOnlyActivationSharesOneAllocation(t *testing.T) {
	source := filepath.Join(t.TempDir(), "probe.fango")
	program := `effect Stop e
    abort stop : e -> value
run : (() ->{Stop e} a) -> Maybe a
run action =
    handle Just (action()) on
        stop _ -> Nothing
main() = print (run { 1 } == run { stop "no" })
`
	if err := os.WriteFile(source, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	src := string(generatedFile(t, emittedProject(t, source), "entries/probe/main.go"))
	for _, want := range []string{"TargetValue", "OriginArguments", "fangort.NewExit("} {
		if !strings.Contains(src, want) {
			t.Errorf("generated code lacks %s:\n%s", want, src)
		}
	}
	for _, separate := range []string{"= &fangort.ExitTarget{", "&fangort.ExitRequest{"} {
		if strings.Contains(src, separate) {
			t.Errorf("generated code allocates %s separately:\n%s", separate, src)
		}
	}
}

func TestForwardedRowCallbacksBindEvidenceAtTheirCallSite(t *testing.T) {
	const prelude = `import List
effect Tick
    tick : Int -> Int
each : (Int ->{e} Int) -> Int -> Int ->{e} Int
each action n x = if n <= 0 then x else each action (n - 1) (action x)
escape : (Int ->{e} Int) -> Int ->{e} Int
escape action = { x -> action x }
`
	for _, tc := range []struct {
		name, program string
		lookup        bool
	}{
		{"user worker", "go n =\n    handle each { x -> tick x } n 0 on\n        tick x -> resume (x + 1)\nmain() = print (go 1)\n", false},
		{"library fold", "go n =\n    handle List.foldl { v t -> tick v + t } 0 (List.range 1 n) on\n        tick x -> resume (x + 1)\nmain() = print (go 1)\n", false},
		{"nested literals", "go n =\n    handle List.foldl { v t -> t + List.foldl { i s -> tick i + s } 0 [v] } 0 (List.range 1 n) on\n        tick x -> resume (x + 1)\nmain() = print (go 1)\n", false},
		{"escaping", "go n =\n    handle each (escape { x -> tick x }) n 0 on\n        tick x -> resume (x + 1)\nmain() = print (go 1)\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "probe.fango")
			if err := os.WriteFile(source, []byte(prelude+tc.program), 0600); err != nil {
				t.Fatal(err)
			}
			src := string(generatedFile(t, emittedProject(t, source), "entries/probe/main.go"))
			if got := strings.Contains(src, "fangort.RowBinding("); got != tc.lookup {
				t.Errorf("row lookup = %t, want %t:\n%s", got, tc.lookup, src)
			}
		})
	}
}
