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
		"withNumber : (Int ->{s | e} a) ->{e} a",
		"{-# scoped s #-}\nwithNumber : (Int ->{s} a) ->{e} a",
	} {
		ck, infos, errs := check(t, annotation+"\nwithNumber action = action 7\nanswer = withNumber { n -> n + 1 }\n")
		if len(errs) != 0 {
			t.Fatal(errs)
		}
		sch := checkedScheme(ck, infos[0])
		if sch.ScopedRow == nil || sch.ScopedArity != 1 || sch.ScopedParam != 0 {
			t.Fatalf("lost contract: %+v", sch)
		}
		if got := types.ShowScheme(sch); got != "(Int ->{s | e} a) ->{e} a" {
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
	for _, test := range []struct{ name, src string }{
		{"different ambient", "run : (Int ->{s | e} a) ->{f} a\nrun f = f 1\n"},
		{"no ambient", "run : (Int ->{s | e} a) -> a\nrun f = f 1\n"},
		{"outer permission", "run : (Int ->{s | e} a) ->{s | e} a\nrun f = f 1\n"},
		{"two callbacks", "run : (Int ->{s | e} a) -> (Int ->{s | e} a) ->{e} a\nrun f g = f 1\n"},
		{"row union in type", "type Job e = { run : () ->{s | e} Int }\n"},
		{"bare binder", "type Reader e = { read : () ->{e} Int }\nrun : (Reader s ->{s | e} a) ->{e} a\nrun f = f { read = { 1 } }\n"},
		{"bare arrow", "type Reader e = { read : () ->{e} Int }\nrun : (Reader {s | e} ->{s} a) ->{e} a\nrun f = f { read = { 1 } }\n"},
		{"nested binder", "run : (() -> (Int ->{s | e} a)) ->{e} a\nrun f = f() 1\n"},
		{"native", "run : (Int ->{s | e} a) ->{e} a\nrun = native\n"},
		{"recursive", "run : (Int ->{s | e} a) ->{e} a\nrun f = run f\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, errs := check(t, test.src)
			for _, err := range errs {
				if strings.Contains(err.Error(), "SCOPED CALLBACK") {
					return
				}
			}
			t.Fatalf("expected SCOPED CALLBACK, got %v", errs)
		})
	}
}

func TestScopedClosedExtension(t *testing.T) {
	ck, infos, errs := check(t, "run : (Int ->{s | {}} a) -> a\nrun action = action 7\nanswer = run { n -> n + 1 }\n")
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if got := types.ShowScheme(checkedScheme(ck, infos[0])); got != "(Int ->{s | {}} a) -> a" {
		t.Fatal(got)
	}
	_, _, errs = check(t, "run : (Int ->{s | {}} a) ->{e} a\nrun action = action 7\n")
	if len(errs) == 0 {
		t.Fatal("accepted closed extension with open ambient row")
	}
}
