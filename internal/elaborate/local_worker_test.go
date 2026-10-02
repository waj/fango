package elaborate_test

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
)

func TestMonomorphicLocalWorkerTailLoop(t *testing.T) {
	prog := elabPoly(t, `outer : Int -> Int
outer step =
    loop : Int -> Int -> Int
    loop n acc = if n < 1 then acc else loop (n - 1) (acc + step)
    loop 10 0
main = print (outer 3)
`)
	for i := range prog.Defs {
		d := &prog.Defs[i]
		if !strings.HasSuffix(d.Name, "_loop") {
			continue
		}
		if len(d.TyParams) != 0 || len(d.Params) != 3 || d.Params[0] != "step" {
			t.Fatalf("expected monomorphic worker with captured step and two arguments:\n%s", core.Dump(prog))
		}
		loop, ok := core.DetectTailLoop(d)
		if !ok || loop.Mutated["step"] || !loop.Mutated["n"] || !loop.Mutated["acc"] {
			t.Fatalf("expected tail loop mutating only n and acc: %#v\n%s", loop, core.Dump(prog))
		}
		return
	}
	t.Fatalf("missing lifted local worker:\n%s", core.Dump(prog))
}

func TestMonomorphicLocalWorkerCaptureExcludesTailLoop(t *testing.T) {
	prog := elabPoly(t, `type Fns = Done | Push (() -> Int) Fns
outer : Int -> Fns
outer step =
    loop : Int -> Fns -> Fns
    loop n fs =
        if n < 1 then fs
        else
            saved : () -> Int
            saved () = n + step
            loop (n - 1) (Push saved fs)
    loop 3 Done
main = 0
`)
	for i := range prog.Defs {
		d := &prog.Defs[i]
		if strings.HasSuffix(d.Name, "_loop") {
			if _, ok := core.DetectTailLoop(d); ok {
				t.Fatalf("loop mutates n captured by an escaping function:\n%s", core.Dump(prog))
			}
			return
		}
	}
	t.Fatalf("missing lifted local worker:\n%s", core.Dump(prog))
}

func TestLocalWorkerCapturesUnitStatement(t *testing.T) {
	prog := elabPoly(t, `outer : Int ->{IO} Int
outer value =
    saved : () ->{IO} Int
    saved () =
        print value
        42
    saved ()
main = outer 3
`)
	for i := range prog.Defs {
		d := &prog.Defs[i]
		if strings.HasSuffix(d.Name, "_saved") {
			if len(d.Params) != 2 || d.Params[0] != "value" {
				t.Fatalf("expected captured value used only in a Unit statement:\n%s", core.Dump(prog))
			}
			return
		}
	}
	t.Fatalf("missing lifted local worker:\n%s", core.Dump(prog))
}

func TestLocalWorkerTerminalClosureAllowsTailLoop(t *testing.T) {
	prog := elabPoly(t, `outer : Int -> (() -> Int)
outer step =
    loop : Int -> (() -> Int)
    loop n = if n < 1 then { n + step } else loop (n - 1)
    loop 10000
main = print ((outer 3)())
`)
	for i := range prog.Defs {
		d := &prog.Defs[i]
		if strings.HasSuffix(d.Name, "_loop") {
			if _, ok := core.DetectTailLoop(d); !ok {
				t.Fatalf("terminal closure cannot survive another iteration:\n%s", core.Dump(prog))
			}
			return
		}
	}
	t.Fatalf("missing lifted local worker:\n%s", core.Dump(prog))
}
