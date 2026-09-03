package elaborate

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/parser"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// elabPoly runs the pipeline with the S5 staging flag on and lints the
// result — the flag-on counterpart of TestGoldens, inline until the flag is
// deleted and testdata/core grows real poly programs.
func elabPoly(t *testing.T, src string) *core.Prog {
	t.Helper()
	f := source.NewFile("<test>", []byte(src))
	toks, lexErrs := lexer.Lex(f)
	if len(lexErrs) > 0 {
		t.Fatalf("lex errors: %v", lexErrs)
	}
	m, parseErrs := parser.Parse(toks, f)
	if len(parseErrs) > 0 {
		t.Fatalf("parse errors: %v", parseErrs)
	}
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	ck := infer.NewChecker(sup, b, infer.NewEnv())
	ck.AllowPoly = true
	infos, inferErrs := ck.Module(m)
	if len(inferErrs) > 0 {
		t.Fatalf("infer errors: %v", inferErrs)
	}
	prog, elabErrs := Module(infos, ck)
	if len(elabErrs) > 0 {
		t.Fatalf("elaborate errors: %v", elabErrs)
	}
	if lintErrs := core.Lint(prog, b); len(lintErrs) > 0 {
		t.Fatalf("core lint: %v\n%s", lintErrs, core.Dump(prog))
	}
	return prog
}

func elabPolyErr(t *testing.T, src string) string {
	t.Helper()
	f := source.NewFile("<test>", []byte(src))
	toks, _ := lexer.Lex(f)
	m, _ := parser.Parse(toks, f)
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	ck := infer.NewChecker(sup, b, infer.NewEnv())
	ck.AllowPoly = true
	infos, inferErrs := ck.Module(m)
	if len(inferErrs) > 0 {
		return inferErrs[0].Title
	}
	_, elabErrs := Module(infos, ck)
	if len(elabErrs) == 0 {
		t.Fatalf("%q: expected an elaboration error", src)
	}
	return elabErrs[0].Title
}

// TestPolyGenericWorker pins the §8.4 shapes: TyParams on the def, explicit
// TyArgs on every call — including the self-recursive one — and instantiated
// constructor applications.
func TestPolyGenericWorker(t *testing.T) {
	prog := elabPoly(t, `type List a = Nil | Cons a (List a)

len xs =
    case xs of
        Nil -> 0
        Cons _ rest -> 1 + len rest

main = print (len (Cons 1 Nil))
`)
	dump := core.Dump(prog)
	for _, want := range []string{
		"(type List (params a) (ctor Nil) (ctor Cons a (List a)))",
		// The result generalizes as `number` too — Elm semantics (§7.3).
		"(def len (typarams a number) (params xs) List a -> number",
		// The recursive call instantiates at the def's own type params.
		"(app/worker @[a number] (var len List a -> number)",
		// main's call instantiates at the defaulted ground types.
		"(app/worker @[Int Int] (var len List Int -> Int)",
		"(app/ctor @[Int]",
	} {
		if !strings.Contains(dump, want) {
			t.Errorf("dump missing %q:\n%s", want, dump)
		}
	}
}

// TestPolyNullaryValue: a polymorphic top-level value becomes a nullary
// generic worker; each use is an instantiated zero-arg call.
func TestPolyNullaryValue(t *testing.T) {
	prog := elabPoly(t, `type Maybe a = Nothing | Just a

none = Nothing

check m =
    case m of
        Nothing -> 0
        Just n -> n

main = print (check none)
`)
	dump := core.Dump(prog)
	for _, want := range []string{
		"(def none (typarams a) Maybe a (app/ctor @[a]",
		"(app/worker @[Int] (var none Maybe Int) Maybe Int)",
	} {
		if !strings.Contains(dump, want) {
			t.Errorf("dump missing %q:\n%s", want, dump)
		}
	}
	for i := range prog.Defs {
		if prog.Defs[i].Name == "none" && !prog.Defs[i].IsWorker() {
			t.Error("none should be a nullary generic worker")
		}
	}
}

// TestPolyLiftedLocal: a generalized block binding lambda-lifts to a
// top-level generic def; uses rewrite to instantiated calls; a captured
// enclosing local becomes a leading parameter.
func TestPolyLiftedLocal(t *testing.T) {
	prog := elabPoly(t, `type Box a = MkBox a

v =
    k = 10
    wrap y = MkBox y
    keep y = k
    a = keep (wrap 1.5)
    keep (wrap a)
`)
	dump := core.Dump(prog)
	if len(prog.Defs) != 3 {
		t.Fatalf("want 3 defs (v + 2 lifted), got %d:\n%s", len(prog.Defs), dump)
	}
	for _, want := range []string{
		// The monomorphism restriction keeps the value bindings as Lets…
		"(let k number (int 10 number)",
		// …while the function bindings lift.
		"(def _lift1_wrap (typarams a) (params y) a -> Box a",
		// keep captures k as a leading param; k's type is v's own number
		// var (v generalizes it at top level), so keep quantifies it too.
		"(def _lift2_keep (typarams number a) (params k y) number -> a -> number",
		// Uses instantiate per occurrence: wrap at Float and at number.
		"(app/worker @[Float] (var _lift1_wrap Float -> Box Float)",
		"(app/worker @[number] (var _lift1_wrap number -> Box number)",
	} {
		if !strings.Contains(dump, want) {
			t.Errorf("dump missing %q:\n%s", want, dump)
		}
	}
}

// TestPolyNumberGeneric: Number-kinded quantification (§7.3) — the def
// carries a number typaram; calls at Int and Float instantiate it.
func TestPolyNumberGeneric(t *testing.T) {
	prog := elabPoly(t, `double x = x + x

main =
    a = double 2
    b = double 1.5
    print b
`)
	dump := core.Dump(prog)
	for _, want := range []string{
		"(def double (typarams number) (params x) number -> number",
		"(app/worker @[Int] (var double Int -> Int)",
		"(app/worker @[Float] (var double Float -> Float)",
	} {
		if !strings.Contains(dump, want) {
			t.Errorf("dump missing %q:\n%s", want, dump)
		}
	}
}

// TestPolyInteriorDefaulting: §8.4's internal-unconstrained-variable rule —
// `len Nil` at an undetermined element type defaults it to Unit in the
// instantiation.
func TestPolyInteriorDefaulting(t *testing.T) {
	prog := elabPoly(t, `type List a = Nil | Cons a (List a)

len xs =
    case xs of
        Nil -> 0
        Cons _ rest -> 1 + len rest

main = print (len Nil)
`)
	dump := core.Dump(prog)
	if !strings.Contains(dump, "(app/worker @[() Int] (var len List () -> Int)") {
		t.Errorf("dump missing Unit-defaulted instantiation:\n%s", dump)
	}
}

// TestPolyEqStaged: == at a type variable stays a staged error (§8.6).
func TestPolyEqStaged(t *testing.T) {
	title := elabPolyErr(t, `member x y = x == y
main = print (member 1 2)
`)
	if title != "EQUALITY AT A TYPE VARIABLE" {
		t.Errorf("got %q", title)
	}
}

// TestPolyBadMain: main must be concrete.
func TestPolyBadMain(t *testing.T) {
	title := elabPolyErr(t, `type Maybe a = Nothing | Just a
main = Nothing
`)
	if title != "BAD MAIN" {
		t.Errorf("got %q", title)
	}
}
