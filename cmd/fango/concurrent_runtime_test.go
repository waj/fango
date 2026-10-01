package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/eval"
	"github.com/waj/fango/internal/nativehost"
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

// Tasks inherit a stateful handler activation from their parent and update
// its cell concurrently. The cell locks only once it has been published, so
// the generated leg always runs under the race detector; the interpreter leg
// runs in-process and is covered when the parent test runs with -race.
func TestConcurrentSharedHandlerStateBackends(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "run", "async_shared_handler_state.fango")
	expected, err := os.ReadFile(strings.TrimSuffix(path, ".fango") + ".expected")
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	program, _, _, _, sources, ok := compileFileGraph(path, &diagnostics)
	if !ok {
		t.Fatalf("compile: %s", diagnostics.String())
	}
	env := eval.NewEnv()
	env.DefineProg(program)
	workerSources := make([]nativehost.Source, len(sources))
	for i, source := range sources {
		workerSources[i] = nativehost.Source{Module: source.Module, Content: source.Content}
	}
	executor, err := nativehost.New(workerSources)
	if err != nil {
		t.Fatal(err)
	}
	defer executor.Close()
	var out bytes.Buffer
	ioctx := eval.NewIOContext(strings.NewReader(""), &out)
	ioctx.Natives = executor
	if _, err := eval.ForceIO(context.Background(), program.Entry, env, ioctx); err != nil {
		t.Fatalf("interpreter: %v", err)
	}
	if out.String() != string(expected) {
		t.Fatalf("interpreter output %q, want %q", out.String(), expected)
	}
	if testing.Short() {
		return
	}

	project := filepath.Join(t.TempDir(), "emitted")
	cmd := exec.Command(cliBinary(t), "build", "--emit-go", "-o", project, path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("emit project: %v\n%s", err, output)
	}
	entry := filepath.Join(project, "entries", "async_shared_handler_state", "concurrent_test.go")
	if err := os.WriteFile(entry, []byte(generatedConcurrentHandlerTest), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("go", "test", "-race", "-count=1", "./entries/async_shared_handler_state")
	cmd.Dir = project
	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOTOOLCHAIN=local")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("emitted race fixture: %v\n%s", err, output)
	}
}

const generatedConcurrentHandlerTest = `package main

import "testing"

func TestConcurrentSharedHandlerState(t *testing.T) {
    for range 4 {
        V_main()
    }
}
`
