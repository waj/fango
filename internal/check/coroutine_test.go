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

func TestOrdinaryHandlerStaysDirectAcrossOwnedCoroutine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Main.fango")
	source := `import Coroutine
effect Ask
    ask : () -> Int
askOne : () ->{Ask} Int
askOne _ = ask ()
main =
    handle
        (Coroutine.with
            (\pause initial -> pause (askOne () + initial))
            (\work -> Coroutine.advance work 1)) of
        ask () -> resume 2
`
	if err := os.WriteFile(path, []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	result, _ := compileEvents(t, path, newMemoryObjectCache())
	seenHandler, seenOperation := false, false
	for _, def := range result.Program.Defs {
		if def.Name == result.Program.Entry {
			handler, ok := def.Body.(*core.Handle)
			if !ok {
				t.Fatalf("main is %T", def.Body)
			}
			if handler.Control != (types.Control{Transport: types.Direct}) || handler.Effect.Control != (types.Control{Transport: types.Direct}) {
				t.Fatalf("ordinary handler gained control transport: %+v %+v", handler.Control, handler.Effect.Control)
			}
			seenHandler = true
		}
		if def.Name == "askOne" || strings.HasSuffix(def.Name, ".askOne") {
			if def.Control.Transport != types.Direct {
				t.Fatalf("ordinary operation gained control transport: %+v", def.Control)
			}
			seenOperation = true
		}
	}
	if !seenHandler || !seenOperation {
		t.Fatalf("missing handler=%v operation=%v", seenHandler, seenOperation)
	}
	program, errs := machine.Lower(result.Program, result.Checker.B)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	for _, worker := range program.Workers {
		if worker.Name == result.Program.Entry {
			t.Fatal("ordinary direct handler/operation acquired a Machine worker", worker.Name)
		}
	}
}

func TestCoroutineContractsSurviveCodecsAndRejectStaleProofs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Main.fango")
	if err := os.WriteFile(path, []byte("import Coroutine\nmain = Coroutine.with (\\pause initial -> pause initial) (\\work -> Coroutine.advance work 42)\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cache := newMemoryObjectCache()
	compileEvents(t, path, cache)
	result, events := compileEvents(t, path, cache)
	if events["checked-cache-hit"]["Coroutine"] != 1 {
		t.Fatalf("Coroutine cache miss: %#v", events)
	}
	lowered, errs := machine.Lower(result.Program, result.Checker.B)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	encoded, err := execcodec.Encode(&execcodec.Payload{Program: result.Program, Machine: lowered})
	if err != nil {
		t.Fatal(err)
	}
	restore := func(t *testing.T) *execcodec.Payload {
		t.Helper()
		p, err := execcodec.Decode(encoded)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := restore(t)
	if errs := core.Lint(p.Program, result.Checker.B); len(errs) != 0 {
		t.Fatal(errs)
	}
	if errs := machine.Lint(p.Machine); len(errs) != 0 {
		t.Fatal(errs)
	}
	for _, test := range []struct {
		name, worker, want string
		damage             func(core.Expr)
	}{
		{"scope identity", types.CoroutineWithName, "invalid or reused coroutine owner", func(e core.Expr) { e.(*core.IteratorScope).Scope = 0 }},
		{"scope evidence", types.CoroutineWithName, "invalid coroutine control owner", func(e core.Expr) { e.(*core.IteratorScope).Yield.Captures = types.CaptureSet{} }},
		{"producer protocol", types.CoroutineWithName, "inconsistent request/reply/result protocol", func(e core.Expr) { s := e.(*core.IteratorScope); s.Producer = s.Consumer }},
		{"typed reply", types.CoroutineAdvanceName, "reply type disagrees", func(e core.Expr) { n := e.(*core.IteratorNext); n.Reply = n.Cursor }},
		{"Step descriptor", types.CoroutineAdvanceName, "invalid coroutine Step result", func(e core.Expr) { e.(*core.IteratorNext).Result = nil }},
		{"exclusive access", types.CoroutineAdvanceName, "lacks exclusive access proof", func(e core.Expr) { e.(*core.IteratorNext).Access = 0 }},
		{"close protocol", types.CoroutineCloseName, "invalid coroutine close protocol", func(e core.Expr) { n := e.(*core.IteratorNext); n.Reply = n.Cursor }},
	} {
		t.Run("Core/"+test.name, func(t *testing.T) {
			p := restore(t)
			found := false
			for i := range p.Program.Defs {
				if p.Program.Defs[i].Name == test.worker {
					test.damage(p.Program.Defs[i].Body)
					found = true
				}
			}
			if !found {
				t.Fatal("missing intrinsic", test.worker)
			}
			if got := fmt.Sprint(core.Lint(p.Program, result.Checker.B)); !strings.Contains(got, test.want) {
				t.Fatalf("got %s, want %s", got, test.want)
			}
		})
	}
	for _, test := range []struct {
		name, want string
		damage     func(machine.Term) bool
	}{
		{"scope identity", "invalid or reused cursor scope", func(term machine.Term) bool {
			n, ok := term.(*machine.CursorOpen)
			if ok {
				n.Scope = 0
			}
			return ok
		}},
		{"scope evidence", "invalid coroutine owner", func(term machine.Term) bool {
			n, ok := term.(*machine.CursorOpen)
			if ok {
				n.Yield.Captures = types.CaptureSet{}
			}
			return ok
		}},
		{"producer protocol", "invalid coroutine owner/producer type", func(term machine.Term) bool {
			n, ok := term.(*machine.CursorOpen)
			if ok {
				n.Producer = &core.VarRef{Local: true, Name: n.Cursor.Name, Ty: n.Cursor.Ty}
			}
			return ok
		}},
		{"typed reply", "reply type disagrees", func(term machine.Term) bool {
			n, ok := term.(*machine.CursorAdvance)
			if ok && !n.Close {
				n.Reply = n.Cursor
				return true
			}
			return false
		}},
		{"Step descriptor", "invalid coroutine Step result", func(term machine.Term) bool {
			n, ok := term.(*machine.CursorAdvance)
			if ok && !n.Close {
				n.Result = nil
				return true
			}
			return false
		}},
		{"exclusive access", "lacks exclusive access proof", func(term machine.Term) bool {
			n, ok := term.(*machine.CursorAdvance)
			if ok {
				n.Access = 0
			}
			return ok
		}},
		{"current evidence", "row argument", func(term machine.Term) bool {
			n, ok := term.(*machine.CursorAdvance)
			if ok {
				n.Row = nil
			}
			return ok
		}},
		{"close protocol", "invalid coroutine close protocol", func(term machine.Term) bool {
			n, ok := term.(*machine.CursorAdvance)
			if ok && n.Close {
				n.Reply = n.Cursor
				return true
			}
			return false
		}},
	} {
		t.Run("Machine/"+test.name, func(t *testing.T) {
			p := restore(t)
			found := false
			for i := range p.Machine.Workers {
				for _, block := range p.Machine.Workers[i].Blocks {
					if !found {
						found = test.damage(block.Term)
					}
				}
			}
			if !found {
				t.Fatal("missing protocol instruction")
			}
			if got := fmt.Sprint(machine.Lint(p.Machine)); !strings.Contains(got, test.want) {
				t.Fatalf("got %s, want %s", got, test.want)
			}
		})
	}
}
