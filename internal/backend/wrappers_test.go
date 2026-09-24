package backend

import (
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/machine"
	"strings"
	"testing"
)

const wrapperLibrary = `module Read exposing (read)
import Runtime.Coroutine exposing (Coroutine, Drive, Step(..))
read : Coroutine Int () () e ->{Drive | e} Maybe Int
read work = case Runtime.Coroutine.advance work () of
    Suspended value -> RESULT
    Finished _ -> Nothing
    Closed -> Nothing
`

func TestWrapperExpansionAcrossModuleBoundary(t *testing.T) {
	p := newProject(t)
	p.write(t, "Main.fango", `import Read
import Runtime.Coroutine
main() = print (Runtime.Coroutine.with (\pause _ -> pause 42) (\work -> Read.read work))
`)
	p.entry = p.dir + "/Main.fango"
	p.write(t, "Read.fango", strings.ReplaceAll(wrapperLibrary, "RESULT", "Just value"))
	result := p.check(t)
	optimized, errs := machine.Lower(result.Program, result.Checker.B)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	matches := 0
	for i := range optimized.Workers {
		w := &optimized.Workers[i]
		for _, block := range w.Blocks {
			if a, ok := block.Term.(*machine.CursorAdvance); ok && machine.AdvanceMatch(w, a) != nil {
				matches++
			}
		}
	}
	if matches == 0 {
		t.Fatal("imported advancement wrapper did not expose its match")
	}
	result.Program.DisableOptimizations = true
	plain, errs := machine.Lower(result.Program, result.Checker.B)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	calls := 0
	for _, w := range plain.Workers {
		for _, block := range w.Blocks {
			if c, ok := block.Term.(*machine.Call); ok && c.Callee == "Runtime.Coroutine.advance" {
				calls++
			}
		}
	}
	if calls == 0 {
		t.Fatal("disabled path did not retain advancement call")
	}
	p.build(t)
	p.build(t)
	if p.events["emitted-cache-miss"]["<entry>"] != 0 {
		t.Fatal("unchanged entry missed cache")
	}
	p.write(t, "Read.fango", strings.ReplaceAll(wrapperLibrary, "RESULT", "Nothing"))
	p.build(t)
	if p.events["emitted-cache-miss"]["<entry>"] != 1 {
		t.Fatal("inline template body edit did not invalidate importer")
	}
}

func TestWorkAdvanceWrapperPreservesOwnerCheckAndExposesMatch(t *testing.T) {
	p := newProject(t)
	p.write(t, "Main.fango", `import Async
import Async.Cooperative
main() = print (Async.Cooperative.run (\_ ->
    task = Async.spawn (\_ ->
        Async.yield()
        42)
    Async.await task))
`)
	p.entry = p.dir + "/Main.fango"
	result := p.check(t)
	lowered, errs := machine.Lower(result.Program, result.Checker.B)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	found := false
	for i := range lowered.Workers {
		w := &lowered.Workers[i]
		if w.Name != "Async.stepDriver" {
			continue
		}
		for _, block := range w.Blocks {
			advance, ok := block.Term.(*machine.CursorAdvance)
			if !ok {
				continue
			}
			open, ok := advance.Cursor.(*core.Work)
			if !ok || open.Kind != "open" || len(open.Args) != 2 {
				t.Fatal("advance lost its checked owner/package opening")
			}
			if machine.AdvanceMatch(w, advance) == nil {
				t.Fatal("advance result still requires packaging")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("scheduler did not expand the Work advancement wrapper")
	}
	p.build(t)
}
