package infer_test

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/elaborate"
)

// These are historical task representation probes, not an implementation of Async.
// In particular, wrapping a value below does not implement completion storage.
func TestAsyncFeasibilityOrdinaryTypedPackaging(t *testing.T) {
	ck, infos, errs := check(t, `
type Task a e = { read : () ->{e} a }
type Job e = { run : () ->{e} () }
type Pair a b = Pair a b

observe : Task a e ->{e} a
observe task = task.read()

package : a -> (a ->{e} ()) -> Pair (Task a e) (Job e)
package value publish = Pair (Task { read = { _ -> value } }) (Job { run = { _ -> publish value } })

useHelper task = observe task
useStored task =
    callback = { _ -> useHelper task }
    callback()

mixed =
    case package 42 { _ -> () } of
        Pair intTask intJob ->
            case package "answer" { _ -> () } of
                Pair stringTask stringJob ->
                    jobs = [intJob, stringJob]
                    (useStored intTask, useStored stringTask, jobs)
`)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	prog, elabErrs := elaborate.Module(infos, ck)
	if len(elabErrs) != 0 {
		t.Fatal(elabErrs)
	}
	if errs := core.Lint(prog, ck.B); len(errs) != 0 {
		t.Fatal(errs)
	}
}

func TestAsyncFeasibilityCurrentLanguageBoundaries(t *testing.T) {
	for _, test := range []struct{ name, source, diagnostic string }{
		{"polymorphic operation", `
effect Scheduling
    start : (() -> a) -> a
bad = start { _ -> 1 }
`, "OPERATION POLYMORPHISM NOT READY"},
		{"confused result", `
type Task a e = { read : () ->{e} a }
observe : Task a e ->{e} a
observe task = task.read()
wrong : Task Int e ->{e} String
wrong task = observe task
`, "TYPE MISMATCH"},
		{"unindexed polymorphic native", `
store : a -> a
store = native
`, "NATIVE STORAGE"},
		{"retained native callback", `
register : (() -> ()) -> ()
register = native
`, "NATIVE DECLARATION"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, errs := check(t, test.source)
			var messages []string
			for _, err := range errs {
				messages = append(messages, err.Error())
			}
			got := strings.Join(messages, "\n")
			if test.diagnostic == "" && got != "" || test.diagnostic != "" && !strings.Contains(got, test.diagnostic) {
				t.Fatalf("diagnostics = %q, want %q", got, test.diagnostic)
			}
		})
	}
}

// Only the public arrow shapes are being checked here. deferWork stores an
// action until observe; it is deliberately not independently scheduled work.
// The missing scoped-package/admission proof is modeled in internal/feasibility.
const nullarySchedulingShapes = `
effect Scheduling
    signal : Int -> ()

type Task a e = { read : () ->{Scheduling | e} a }

deferWork : (() ->{Scheduling | e} a) ->{Scheduling | e} Task a e
deferWork action =
    signal 0
    Task { read = action }

observe : Task a e ->{Scheduling | e} a
observe task = task.read()

offer : () ->{Scheduling} ()
offer() = signal 1
`

func TestAsyncFeasibilityNullarySchedulingShapes(t *testing.T) {
	ck, infos, errs := check(t, nullarySchedulingShapes+`
integer() =
    offer()
    42

word() =
    print "child"
    "answer"

helper action = deferWork action
stored task =
    callback = { _ -> observe task }
    callback()

mixed() =
    first = helper integer
    second = helper word
    (stored first, stored second)
`)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	prog, elabErrs := elaborate.Module(infos, ck)
	if len(elabErrs) != 0 {
		t.Fatal(elabErrs)
	}
	if errs := core.Lint(prog, ck.B); len(errs) != 0 {
		t.Fatal(errs)
	}
}

func TestAsyncFeasibilityUnawaitedWorkStillChargesItsRow(t *testing.T) {
	_, _, errs := check(t, nullarySchedulingShapes+`
forgotten : () ->{Scheduling} ()
forgotten() =
    pending = deferWork { _ -> print "child" }
    ()
`)
	for _, err := range errs {
		if err.Error() == "EFFECT MISMATCH" {
			return
		}
	}
	t.Fatalf("unobserved deferred IO lost its row: %v", errs)
}
