package infer_test

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/elaborate"
	"github.com/waj/fango/internal/types"
)

func TestScopedExtensionUsesExistingContract(t *testing.T) {
	for _, annotation := range []string{
		"withNumber : (Int ->{#s} a) ->{e} a",
	} {
		ck, infos, errs := check(t, annotation+"\nwithNumber action = action 7\nanswer = withNumber { n -> n + 1 }\n")
		if len(errs) != 0 {
			t.Fatal(errs)
		}
		sch := checkedScheme(ck, infos[0])
		if sch.ScopedRow == nil || sch.ScopedArity != 1 || sch.ScopedParam != 0 {
			t.Fatalf("lost contract: %+v", sch)
		}
		if got := types.ShowScheme(sch); got != "(Int ->{#s} a) ->{e} a" {
			t.Fatalf("signature %q", got)
		}
		p, es := elaborate.Module(infos, ck)
		if len(es) != 0 {
			t.Fatal(es)
		}
		if es := core.Lint(p, ck.B); len(es) != 0 {
			t.Fatal(es)
		}
	}
}

func TestScopedExtensionBoundaries(t *testing.T) {
	for _, test := range []struct{ name, src, want string }{
		{"outer permission", "run : (Int ->{#s} a) ->{#s} a\nrun f = f 1\n", "`#s` may occur only in the scoped callback's argument"},
		{"two callbacks", "run : (Int ->{#s} a) -> (Int ->{#s} a) ->{e} a\nrun f g = f 1\n", "Exactly one callback parameter must end its effect row with `#s`"},
		{"field", "type Job e = { run : () ->{#s} Int }\n", "belongs only to the callback of an annotated top-level runner"},
		{"local annotation", "outer x =\n    run : (Int ->{#s} a) ->{e} a\n    run f = f 1\n    run { n -> n }\n", "belongs only to the callback of an annotated top-level runner"},
		{"effect operation", "effect Hold\n    hold : (Int ->{#s} a) -> a\n", "belongs only to the callback of an annotated top-level runner"},
		{"nested binder", "run : (() -> (Int ->{#s} a)) ->{e} a\nrun f = f() 1\n", "Exactly one callback parameter must end its effect row with `#s`"},
		{"native", "run : (Int ->{#s} a) ->{e} a\nrun = native\n", "Native sidecars cannot declare scoped callback contracts"},
		{"recursive", "run : (Int ->{#s} a) ->{e} a\nrun f = run f\n", "SCOPED CALLBACK"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, errs := check(t, test.src)
			var messages []string
			for _, err := range errs {
				messages = append(messages, err.Error()+": "+err.(checkErr).body)
			}
			if got := strings.Join(messages, "\n"); !strings.Contains(got, "SCOPED CALLBACK") || !strings.Contains(got, test.want) {
				t.Fatalf("want SCOPED CALLBACK with %q, got %v", test.want, got)
			}
		})
	}
}

func TestScopedClosedExtension(t *testing.T) {
	ck, infos, errs := check(t, "run : (Int ->{#s} a) -> a\nrun action = action 7\nanswer = run { n -> n + 1 }\n")
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if got := types.ShowScheme(checkedScheme(ck, infos[0])); got != "(Int ->{#s} a) -> a" {
		t.Fatal(got)
	}
}

// A runner that handles an effect its callback names explicitly keeps that
// effect out of an unannotated caller's row, even when the callback also uses
// the runner's ambient row through the scoped reader it receives.
func TestScopedCallbackHandledLabelStaysOutOfAmbient(t *testing.T) {
	const prelude = `effect Boom x
  boom : x -> a
attempt : (() ->{Boom x, e} a) ->{e} Int
attempt act = 1
type Reader e = { read : () ->{e} Int }
`
	for _, runner := range []string{
		"guarded : (Reader {#s} ->{Boom String, #s} a) ->{e} Int",
		"guarded : (Reader e ->{Boom String, e} a) ->{e} Int",
	} {
		for _, body := range []string{
			`if r.read() == 1 then boom "x" else 0`,
			`if 1 == 1 then boom "x" else r.read()`,
		} {
			src := prelude + runner + "\nguarded action = attempt { action { read = { 1 } } }\nrun() = guarded { r -> " + body + " }\n"
			ck, infos, errs := check(t, src)
			if len(errs) != 0 {
				t.Fatalf("%s\n%v", src, errs)
			}
			if got := types.ShowScheme(checkedScheme(ck, infos[len(infos)-1])); got != "() -> Int" {
				t.Errorf("%s\nrun : %s", src, got)
			}
		}
	}
}

// `#s` carries the runner's whole final row, its fixed labels and its tail
// alike, so neither is repeated beside it. Labels written before `#s` are
// granted, and so handled, by the runner: they stay out of the caller's row,
// while the fixed outer effect and the one supplied through the tail reach it.
// The callback's own argument may still mention the ambient row.
func TestScopedRowCarriesRunnerRow(t *testing.T) {
	const src = `effect Outer
    outer : () -> Int
effect Extra
    extra : () -> Int
effect Note
    note : Int -> ()

run : ((() ->{e} Int) ->{Note, #s} a) ->{Outer, e} a
run action = handle action { 1 } on
    note _ -> resume ()

caller() = run { more ->
    note (more())
    outer() + extra() }
`
	ck, infos, errs := check(t, src)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	got := map[string]string{}
	for _, info := range infos {
		got[info.Name] = types.ShowScheme(checkedScheme(ck, info))
	}
	if got["run"] != "((() ->{e} Int) ->{Note, #s} a) ->{Outer, e} a" {
		t.Errorf("run : %s", got["run"])
	}
	// The printed signature reads back with the same scoped binder.
	reparsed, reinfos, errs := check(t, strings.Replace(src, "run : ((() ->{e} Int) ->{Note, #s} a) ->{Outer, e} a", "run : "+got["run"], 1))
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if again := checkedScheme(reparsed, reinfos[0]); again.ScopedRow == nil || types.ShowScheme(again) != got["run"] {
		t.Errorf("reparsed run : %s", types.ShowScheme(again))
	}
	if got["caller"] != "() ->{Extra, Outer} Int" {
		t.Errorf("caller : %s", got["caller"])
	}
	p, es := elaborate.Module(infos, ck)
	if len(es) != 0 {
		t.Fatal(es)
	}
	if es := core.Lint(p, ck.B); len(es) != 0 {
		t.Fatal(es)
	}
}

// A closed final arrow may still perform effects; the callback receives them
// through `#s` without writing them again.
func TestScopedRowClosedRunnerEffects(t *testing.T) {
	const src = `effect Outer
    outer : () -> Int

run : (Int ->{#s} a) ->{Outer} a
run action = action (outer())

caller() = run { n -> n + outer() }
`
	ck, infos, errs := check(t, src)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if got := types.ShowScheme(checkedScheme(ck, infos[len(infos)-1])); got != "() ->{Outer} Int" {
		t.Errorf("caller : %s", got)
	}
	_, _, errs = check(t, `effect Other
    other : () -> Int

run : (Int ->{#s} a) -> a
run action = action 1

caller() = run { n -> n + other() }
`)
	if len(errs) == 0 {
		t.Error("a pure closed runner let its callback perform an effect")
	}
}
