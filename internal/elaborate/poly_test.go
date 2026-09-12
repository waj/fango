package elaborate_test

import (
	"github.com/waj/fango/internal/elaborate"
	"github.com/waj/fango/internal/staging"
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/parser"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// elabPoly runs the polymorphic pipeline and lints the
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
	staging.Install(ck)
	if errs := ck.InstallPrelude(); len(errs) > 0 {
		t.Fatalf("prelude errors: %v", errs)
	}
	// Module loading normally groups operator runs; these tests parse
	// directly, so they group against the prelude's table themselves.
	if errs := ck.Fixity.Resolve(m); len(errs) > 0 {
		t.Fatalf("fixity errors: %v", errs)
	}
	infos, inferErrs := ck.Module(m)
	if len(inferErrs) > 0 {
		t.Fatalf("infer errors: %v", inferErrs)
	}
	prog, elabErrs := elaborate.Module(infos, ck)
	if len(elabErrs) > 0 {
		t.Fatalf("elaborate errors: %v", elabErrs)
	}
	if lintErrs := core.Lint(prog, b); len(lintErrs) > 0 {
		t.Fatalf("core lint: %v\n%s", lintErrs, core.Dump(prog))
	}
	return fixtureProgram(prog, ck.PreludeOwners)
}

func elabPolyErr(t *testing.T, src string) string {
	t.Helper()
	f := source.NewFile("<test>", []byte(src))
	toks, _ := lexer.Lex(f)
	m, _ := parser.Parse(toks, f)
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	ck := infer.NewChecker(sup, b, infer.NewEnv())
	staging.Install(ck)
	if errs := ck.InstallPrelude(); len(errs) > 0 {
		t.Fatalf("prelude errors: %v", errs)
	}
	// Module loading normally groups operator runs; these tests parse
	// directly, so they group against the prelude's table themselves.
	if errs := ck.Fixity.Resolve(m); len(errs) > 0 {
		t.Fatalf("fixity errors: %v", errs)
	}
	infos, inferErrs := ck.Module(m)
	if len(inferErrs) > 0 {
		return inferErrs[0].Title
	}
	_, elabErrs := elaborate.Module(infos, ck)
	if len(elabErrs) == 0 {
		t.Fatalf("%q: expected an elaboration error", src)
	}
	return elabErrs[0].Title
}

// TestPolyGenericWorker pins the doc/design.md, "Go backend and runtime" shapes: TyParams on the def, explicit
// TyArgs on every call — including the self-recursive one — and instantiated
// constructor applications.
func TestPolyGenericWorker(t *testing.T) {
	prog := elabPoly(t, `type Chain a = Empty | Link a (Chain a)

len xs =
    case xs of
        Empty -> 0
        Link _ rest -> 1 + len rest

main = print (len (Link 1 Empty))
`)
	dump := core.Dump(prog)
	for _, want := range []string{
		"(type Chain (params a) (ctor Empty) (ctor Link a (Chain a)))",
		// The result generalizes as `number` too — Elm semantics (doc/design.md, "Type inference").
		"(def len (typarams a b) (params _dict0 xs) _dictionary_Num b -> Chain a -> b",
		// The recursive call instantiates at the def's own type params.
		"(app/worker @[a b] (var len _dictionary_Num b -> Chain a -> b)",
		// main's call instantiates at the defaulted ground types.
		"(app/worker @[Int Int] (var len _dictionary_Num Int -> Chain Int -> Int)",
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
	prog := elabPoly(t, `type Opt a = None | Some a

none = None

check m =
    case m of
        None -> 0
        Some n -> n

main = print (check none)
`)
	dump := core.Dump(prog)
	for _, want := range []string{
		"(def none (typarams a) Opt a (app/ctor @[a]",
		"(app/worker @[Int] (var none Opt Int) Opt Int)",
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
		"(let k a (app/value",
		// …while the function bindings lift.
		"(def _lift1_wrap (typarams a b) (params _dict0 y) _dictionary_Num a -> b -> Box b",
		// keep captures k as a leading param; k's type is v's own number
		// var (v generalizes it at top level), so keep quantifies it too.
		"(def _lift2_keep (typarams a b) (params _dict0 k y) _dictionary_Num a -> a -> b -> a",
		// Uses instantiate per occurrence: wrap at Float and at number.
		"(app/worker @[a Float] (var _lift1_wrap _dictionary_Num a -> Float -> Box Float)",
		"(app/worker @[a a] (var _lift1_wrap _dictionary_Num a -> a -> Box a)",
	} {
		if !strings.Contains(dump, want) {
			t.Errorf("dump missing %q:\n%s", want, dump)
		}
	}
}

// TestPolyNumberGeneric: Number-kinded quantification (doc/design.md, "Type inference") — the def
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
		"(def double (typarams a) (params _dict0 x) _dictionary_Num a -> a -> a",
		"(app/worker (var _scalar_Int_646f75626c65 Int -> Int)",
		"(app/worker (var _scalar_Float_646f75626c65 Float -> Float)",
	} {
		if !strings.Contains(dump, want) {
			t.Errorf("dump missing %q:\n%s", want, dump)
		}
	}
}

// TestPolyInteriorDefaulting pins the internal-unconstrained-variable rule
// from doc/design.md, "Go backend and runtime":
// `len Empty` at an undetermined element type defaults it to Unit in the
// instantiation.
func TestPolyInteriorDefaulting(t *testing.T) {
	prog := elabPoly(t, `type Chain a = Empty | Link a (Chain a)

len xs =
    case xs of
        Empty -> 0
        Link _ rest -> 1 + len rest

main = print (len Empty)
`)
	dump := core.Dump(prog)
	if !strings.Contains(dump, "(app/worker @[() Int] (var len _dictionary_Num Int -> Chain () -> Int)") {
		t.Errorf("dump missing Unit-defaulted instantiation:\n%s", dump)
	}
}

// TestPolyEqStaged pins the unsupported == at a type variable diagnostic;
// see doc/design.md, "Type inference".
func TestPolyEqEvidence(t *testing.T) {
	prog := elabPoly(t, `member x y = x == y
main = print (member 1 2)
`)
	if !strings.Contains(core.Dump(prog), "_dictionary_Eq a -> a -> a -> Bool") {
		t.Errorf("generic equality lacks dictionary evidence: %s", core.Dump(prog))
	}
}

// TestPolyBadMain: main never generalizes — a function-valued main is
// rejected (its underdetermined variables default, so `main = id` lands at
// () -> () and hits the function-typed-main error).
func TestPolyBadMain(t *testing.T) {
	title := elabPolyErr(t, `id x = x
main = id
`)
	if title != "BAD MAIN" {
		t.Errorf("got %q", title)
	}
}

// TestPolyMainDefaults: main's unconstrained type variables default like
// interior ones (`main = None` is an Opt () program), keeping
// `main = 1 + 2` an Int program.
func TestPolyMainDefaults(t *testing.T) {
	prog := elabPoly(t, `type Opt a = None | Some a
main = None
`)
	dump := core.Dump(prog)
	if !strings.Contains(dump, "(def main Opt ()") {
		t.Errorf("main should default to Opt ():\n%s", dump)
	}
}
