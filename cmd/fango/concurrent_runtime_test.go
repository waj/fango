package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/eval"
	"github.com/waj/fango/internal/types"
	"github.com/waj/fango/runtime/fangort"
)

// The same published Fango List is extended by independent interpreter
// invocations and by emitted Direct workers. The generated leg
// always uses the race detector, even when the parent test did not.
func TestConcurrentSharedListBackends(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "run", "c6d_shared_list.fango")
	var diagnostics bytes.Buffer
	program, _, ok := compileFile(path, &diagnostics)
	if !ok {
		t.Fatalf("compile: %s", diagnostics.String())
	}
	var extend *core.Def
	for i := range program.Defs {
		if program.Defs[i].Name == "extend" {
			extend = &program.Defs[i]
			break
		}
	}
	if extend == nil {
		t.Fatal("missing extend worker")
	}
	fn := extend.Type.(*types.TFun)
	env := eval.NewEnv()
	env.DefineProg(program)
	const children = 64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range children {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			call := &core.App{CalleeKind: core.Worker, Callee: &core.VarRef{Name: extend.Name, Ty: extend.Type},
				Args: []core.Expr{&core.IntLit{Val: int64(i), Ty: fn.Arg}}, Ty: fn.Ret,
				Control: core.ArrowControl(extend.Type, 1)}
			value, err := eval.Eval(context.Background(), call, env, io.Discard)
			if err != nil {
				t.Errorf("interpreter child %d: %v", i, err)
				return
			}
			list, ok := value.(fangort.List[any])
			if !ok || list.IsEmpty() || list.Head() != int64(i) || list.Tail().Head() != int64(1) {
				t.Errorf("interpreter child %d: %v", i, value)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if testing.Short() {
		return
	}

	project := filepath.Join(t.TempDir(), "emitted")
	cmd := exec.Command(cliBinary(t), "build", "--emit-go", "-o", project, path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("emit project: %v\n%s", err, output)
	}
	entry := filepath.Join(project, "entries", "c6d_shared_list", "concurrent_test.go")
	if err := os.WriteFile(entry, []byte(generatedConcurrentListTest), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("go", "test", "-race", "-count=1", "./entries/c6d_shared_list")
	cmd.Dir = project
	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOTOOLCHAIN=local")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("emitted race fixture: %v\n%s", err, output)
	}
}

const generatedConcurrentListTest = `package main

import (
    "sync"
    "testing"
)

func TestConcurrentSharedList(t *testing.T) {
    const children = 64
    var wg sync.WaitGroup
    start := make(chan struct{})
    for i := range children {
        wg.Add(1)
        go func(i int) {
            defer wg.Done()
            <-start
            direct := V_extend(int64(i))
            if direct.IsEmpty() || direct.Head() != int64(i) || direct.Tail().Head() != 1 {
                t.Errorf("child %d has wrong list", i)
            }
        }(i)
    }
    close(start)
    wg.Wait()
    if V_base.Head() != 1 || V_base.Tail().Head() != 2 {
        t.Fatal("shared tail changed")
    }
}
`
