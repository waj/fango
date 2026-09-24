package check

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/execcodec"
	"github.com/waj/fango/internal/machine"
	"github.com/waj/fango/internal/types"
)

func TestSharedContractsSurviveObjectsAndExecutionCodec(t *testing.T) {
	for _, fixture := range []string{"native_requests", "native_storage", "native_phantom", "completion_cell_replay", "completion_cell", "coroutine_shared_native", "service_context", "service_nested_pull"} {
		t.Run(fixture, func(t *testing.T) {
			path := filepath.Join("..", "..", "testdata", "run", fixture+".fango")
			cache := newMemoryObjectCache()
			compileEvents(t, path, cache)
			result, _ := compileEvents(t, path, cache)
			lowered, errs := machine.Lower(result.Program, result.Checker.B)
			if len(errs) > 0 {
				t.Fatal(errs)
			}
			encoded, err := execcodec.Encode(&execcodec.Payload{Program: result.Program, Machine: lowered})
			if err != nil {
				t.Fatal(err)
			}
			payload, err := execcodec.Decode(encoded)
			if err != nil {
				t.Fatal(err)
			}
			if errs := core.LintMachineInput(payload.Program, result.Checker.B); len(errs) > 0 {
				t.Fatal(errs)
			}
			if errs := machine.Lint(payload.Machine); len(errs) > 0 {
				t.Fatal(errs)
			}
		})
	}
}

func TestSharedContractsRejectMalformedCore(t *testing.T) {
	for _, tc := range []struct {
		name, fixture, want string
		damage              func(*core.Prog)
	}{
		{"storage metadata", "native_storage", "storage contract", func(p *core.Prog) {
			for _, n := range p.Natives {
				if n.Storage.Kind != "" {
					n.Storage.Kind = ""
					return
				}
			}
		}},
		{"shared representation", "coroutine_shared_native", "shared native resource", func(p *core.Prog) {
			for _, a := range p.ADTs {
				if a.Shared {
					a.Resource = false
					return
				}
			}
		}},
		{"service opt in", "service_context", "metadata disagrees", func(p *core.Prog) {
			for _, e := range p.Effects {
				if e.Service {
					e.Service = false
					return
				}
			}
		}},
		{"service protocol", "service_context", "want () -> ()", func(p *core.Prog) {
			for _, e := range p.Effects {
				if e.Service {
					e.Ops[0].Invocation.Args[0] = e.Ops[0].Invocation.Args[1]
					return
				}
			}
		}},
		{"slot source", "service_context", "checked producer scope", func(p *core.Prog) {
			for _, d := range p.Defs {
				core.Inspect(d.Body, func(e core.Expr) {
					if w, ok := e.(*core.Work); ok && w.Kind == "invocation-slot" {
						w.Args[0].(*core.VarRef).Name = "_action"
					}
				})
			}
		}},
		{"adapter proof", "service_context", "implicit service invocation adapter", func(p *core.Prog) {
			for _, d := range p.Defs {
				core.Inspect(d.Body, func(e core.Expr) {
					if w, ok := e.(*core.Work); ok && w.Kind == "invocation-argument" {
						w.SourceRow = types.Row{}
					}
				})
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, _ := compileEvents(t, filepath.Join("..", "..", "testdata", "run", tc.fixture+".fango"), newMemoryObjectCache())
			tc.damage(result.Program)
			got := fmt.Sprint(core.LintMachineInput(result.Program, result.Checker.B))
			if !strings.Contains(got, tc.want) {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestSharedNativeOptInIsNominal(t *testing.T) {
	source, err := os.ReadFile("../../testdata/run/coroutine_shared_native.fango")
	if err != nil {
		t.Fatal(err)
	}
	sidecar, err := os.ReadFile("../../testdata/run/coroutine_shared_native.native.go")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "Main.fango")
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(source), "shared-resource", "resource")), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Main.native.go"), sidecar, 0600); err != nil {
		t.Fatal(err)
	}
	_, ds, err := (&Session{}).Compile(path)
	if err != nil {
		t.Fatal(err)
	}
	var messages []string
	for _, d := range ds {
		messages = append(messages, d.Title+" "+d.Body)
	}
	if got := strings.Join(messages, "\n"); !strings.Contains(got, "WORK CAPABILITY TRANSFER") {
		t.Fatal(got)
	}
}

func TestSharedTransferInspectsDataDictionariesAndServiceEvidence(t *testing.T) {
	for _, tc := range []struct{ name, source string }{
		{"dictionary and ADT", `effect Counter
    tick : () -> Int
type Box = Box (() -> Int)
class Query a
    query : a -> Int
instance Query Box
    query (Box action) = action()
withBox use = handle use (Box (\_ -> tick())) with current = 0 of
    tick () -> resume current with current + 1
main() = withBox (\box -> Runtime.Coroutine.scope (\scope ->
    child = Runtime.Work.register (Runtime.Coroutine.facet scope) (\_ () -> query box)
    ()))
`},
		{"service hidden context", `effect Counter
    tick : () -> Int
withRead : ((() -> Int) ->{e} a) ->{e} a
withRead use = handle use (\_ -> tick()) with current = 0 of
    tick () -> resume current with current + 1
{-# service #-}
effect Dispatch
    send : () ->{Runtime.Service.Invocation Int ()} ()
type Bound = Bound (() ->{Runtime.Service.Invocation Int ()} ())
bind read = handle Bound (\_ -> send()) of
    send () -> resume (Runtime.Service.invoke (read()))
producer (Bound action) pause () = Runtime.Service.run pause action
main() = withRead (\read ->
    context = bind read
    Runtime.Coroutine.scope (\scope ->
        child = Runtime.Work.register (Runtime.Coroutine.facet scope) (producer context)
        ()))
`},
		{"cursor through ADT", `type Box a = Box a
bad() = Runtime.Coroutine.scope (\scope ->
    sibling = Runtime.Coroutine.create scope (\_ () -> 42)
    box = Box sibling
    child = Runtime.Work.register (Runtime.Coroutine.facet scope) (\_ () -> case box of
        Box cursor -> Runtime.Coroutine.advance cursor ())
    ())
main() = ()
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "Main.fango")
			if err := os.WriteFile(path, []byte("import Runtime.Coroutine\nimport Runtime.Work\nimport Runtime.Service\n"+tc.source), 0600); err != nil {
				t.Fatal(err)
			}
			_, ds, err := (&Session{}).Compile(path)
			if err != nil {
				t.Fatal(err)
			}
			var messages []string
			for _, d := range ds {
				messages = append(messages, d.Title+" "+d.Body)
			}
			if got := strings.Join(messages, "\n"); !strings.Contains(got, "WORK CAPABILITY TRANSFER") {
				t.Fatalf("expected transfer rejection, got %s", got)
			}
		})
	}
}

func TestSharedServiceModuleObject(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "modules", "shared_services", "Main.fango")
	cache := newMemoryObjectCache()
	compileEvents(t, path, cache)
	_, events := compileEvents(t, path, cache)
	if events["checked-cache-hit"]["Context"] != 1 || events["checked-cache-hit"]["Runtime.Service"] != 1 {
		t.Fatalf("missing cached service contracts: %v", events)
	}
}
