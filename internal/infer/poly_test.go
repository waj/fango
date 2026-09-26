package infer_test

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/staging"

	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/parser"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// checkPoly exercises generalization,
// parameterized types, and annotation variables are live.
func checkPoly(t *testing.T, src string) (*infer.Checker, []infer.DeclInfo, []error) {
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
	infos, errs := ck.Module(m)
	var out []error
	for _, e := range errs {
		out = append(out, checkErr{e.Title, e.Span.StartPos().Line})
	}
	return ck, infos, out
}

func TestPolyPositive(t *testing.T) {
	cases := []struct {
		src  string
		want string // "name : scheme" per decl — the generalized types
	}{
		{"id x = x", "id : a -> a"},
		{"const x y = x", "const : a -> b -> a"},
		{"double x = x + x", "double : Num a => a -> a"},
		// Generalization at each binding: two uses at two types both check.
		{"id x = x\na = id 1\nb = id \"s\"", "id : a -> a, a : Num b => b, b : String"},
		// Number vars generalize (doc/design.md, "Type inference"): usable at Int and Float.
		// a's number var is a fresh instantiation, distinct from double's —
		// the shared printer numbers it number2.
		{"double x = x + x\na = double 2\nb = double 1.5", "double : Num a => a -> a, a : Num b => b, b : Float"},
		// Annotated polymorphism, checked by skolemize-and-unify.
		{"id : a -> a\nid x = x", "id : a -> a"},
		{"apply : (a -> b) -> a -> b\napply f x = f x", "apply : (a -> b) -> a -> b"},
		{"double : Num a => a -> a\ndouble x = x + x", "double : Num a => a -> a"},
		// An annotation may be less general than the body.
		{"idInt : Int -> Int\nidInt x = x", "idInt : Int -> Int"},
		// Parameterized ADTs: constructor instantiation per occurrence.
		{"type Opt a = None | Some a\nx = Some 1\ny = Some \"s\"", "x : Num a => Opt a, y : Opt String"},
		{"type Opt a = None | Some a\nn = None", "n : Opt a"},
		// Patterns instantiate constructors too.
		{"type Opt a = None | Some a\nf m = case m of\n    None -> 0\n    Some n -> n + 1", "f : Num a => Opt a -> a"},
		{"type Box a = MkBox a\nunbox b = case b of\n    MkBox x -> x", "unbox : Box a -> a"},
		// An arrow sharing its row tail with an effect-indexed type's
		// argument prints that tail: it is what the value's index makes the
		// arrow perform, not a row the caller is free to choose.
		{"type Foo eff = Foo (() ->{IO | eff} ())\nwrap action = Foo action", "wrap : (() ->{IO | e} ()) -> Foo e"},
		{"type Test eff = TestCase (() ->{eff} ())\nsuite : Test IO\nsuite = TestCase (\\_ -> print ())", "suite : Test {IO}"},
		{"type Test eff = Wrap (Test eff) | Bar (() ->{eff} ())\nmake action = Bar action", "make : (() ->{e} ()) -> Test e"},
		{"effect Expectation\n    abort fail : String -> e\ntype Test eff = TestCase (() ->{Expectation | eff} ())\nrunHelper : Test eff ->{IO | eff} ()\nrunHelper (TestCase action) =\n    handle action() of\n        fail msg -> print msg", "runHelper : Test e ->{IO | e} ()"},
		// Applied types in annotations.
		{"type Opt a = None | Some a\nx : Opt Int\nx = Some 1", "x : Opt Int"},
		{"type Opt a = None | Some a\nf : Opt a -> Opt a\nf m = m", "f : Opt a -> Opt a"},
		// A recursive parameterized type, regular occurrences only.
		{"type Chain a = Empty | Link a (Chain a)\nlen xs = case xs of\n    Empty -> 0\n    Link _ rest -> 1 + len rest", "len : Num b => Chain a -> b"},
		// Local (block) polymorphism: one local used at two types.
		{"v =\n  id2 y = y\n  a = id2 1\n  b = id2 \"s\"\n  a", "v : Num a => a"},
		// Mutually recursive parameterized types, regular.
		{"type A a = MkA (B a) | EndA\ntype B a = MkB (A a)\nf x = MkA (MkB x)", "f : A a -> A a"},
		// An annotated handler wrapper with an open effect-row tail: the
		// annotation's rigid row variable unifies with the fresh row a call
		// site mints, because a label-free open row normalizes to its tail.
		{"effect Ask\n    ask : () -> String\nrun : (() ->{Ask | e} a) ->{e} a\nrun action =\n    handle action() of\n        ask () -> resume \"yes\"", "run : (() ->{Ask | e} a) ->{e} a"},
	}
	for _, c := range cases {
		ck, infos, errs := checkPoly(t, c.src)
		if len(errs) > 0 {
			t.Errorf("%q: unexpected errors %v", c.src, errs)
			continue
		}
		var parts []string
		p := types.NewPrinter()
		for _, info := range infos {
			parts = append(parts, info.Name+" : "+p.Scheme(checkedScheme(ck, info)))
		}
		if got := strings.Join(parts, ", "); got != c.want {
			t.Errorf("%q: got %q, want %q", c.src, got, c.want)
		}
	}
}

func TestPolyNegative(t *testing.T) {
	cases := []struct {
		src       string
		wantTitle string
		wantLine  int
	}{
		// Annotation more general than the body: rigid vars are atomic.
		{"f : a -> b\nf x = x", "TYPE MISMATCH", 2},
		{"f : a -> Int\nf x = x", "TYPE MISMATCH", 2},
		// A Number obligation cannot narrow a General annotation variable.
		{"f : a -> a\nf x = x + 1", "MISSING CONSTRAINT", 1},
		// Polymorphic recursion is rejected by construction: the recursive
		// occurrence is the pre-bound monomorphic self, so nesting occurs.
		{"type Box a = MkBox a\nf b = f (MkBox b)", "TYPE MISMATCH", 2},
		// ... and an annotation does not re-enable it (self stays the meta;
		// the occurs failure surfaces at the annotation unification).
		{"type Box a = MkBox a\nf : Box a -> Int\nf b = f (MkBox b)", "TYPE MISMATCH", 3},
		// Non-regular recursive types.
		{"type Pair a b = MkPair a b\ntype T a = Leaf | Node (T (Pair a a))", "NON-REGULAR TYPE", 2},
		{"type A a = MkA (B (A a)) | EndA\ntype B a = MkB (A a)", "NON-REGULAR TYPE", 1},
		// Type-application arity.
		{"type Opt a = None | Some a\nx : Opt\nx = None", "TYPE ARITY", 2},
		{"type Opt a = None | Some a\nx : Opt Int Int\nx = None", "TYPE ARITY", 2},
		{"x : Int Int\nx = 1", "TYPE ARITY", 1},
		// Constructor fields resolve in the closed parameter scope.
		{"type T a = MkT b", "NAMING ERROR", 1},
		{"type T a a = MkT a", "SHADOWING", 1},
		{"type Inner eff = Inner (() ->{IO | eff} ())\ntype Bad a = Bad (Inner a) a", "KIND MISMATCH", 2},
		// A row literal fills a row-kinded parameter and nothing else.
		{"x : Maybe {IO}\nx = Nothing", "KIND MISMATCH", 1},
		{"type Box a = MkBox a\ntype Bad = MkBad (Box {IO})", "KIND MISMATCH", 2},
		// A generalized block binding's annotation must not claim a variable
		// the enclosing definition pins down. (Value bindings don't
		// generalize — the monomorphism restriction — so the check applies
		// to function and lambda bindings.)
		{"outer x =\n  y : a -> a\n  y = \\z -> x\n  y", "ANNOTATION TOO GENERAL", 2},
	}
	for _, c := range cases {
		_, _, errs := checkPoly(t, c.src)
		if len(errs) == 0 {
			t.Errorf("%q: expected an error", c.src)
			continue
		}
		e := errs[0].(checkErr)
		if e.title != c.wantTitle || e.line != c.wantLine {
			t.Errorf("%q: got %s at line %d, want %s at line %d",
				c.src, e.title, e.line, c.wantTitle, c.wantLine)
		}
	}
}

// TestPolySchemeVars pins the scheme representation: quantified vars are
// rigid, in first-occurrence order, with kinds preserved — the Go
// type-parameter order at codegen.
func TestPolySchemeVars(t *testing.T) {
	_, infos, errs := checkPoly(t, "const x y = x\ndouble n = n + n")
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	constSch := infos[0].Scheme
	if len(constSch.Vars) != 2 {
		t.Fatalf("const: want 2 type vars, got %d", len(constSch.Vars))
	}
	wantKinds := []types.VarKind{types.General, types.General}
	for i, v := range constSch.Vars {
		if !v.Rigid || v.Kind != wantKinds[i] {
			t.Errorf("const var %d: want rigid kind %v, got %+v", i, wantKinds[i], v)
		}
	}
	// First-occurrence order: the result var (x's) is Vars[0].
	body, ok := constSch.Body.(*types.TFun)
	if !ok {
		t.Fatal("const scheme body is not a function")
	}
	if !types.Equal(body.Arg, constSch.Vars[0]) {
		t.Errorf("const: Vars[0] is not the first parameter's var")
	}

	doubleSch := infos[1].Scheme
	if len(doubleSch.Vars) != 1 || !doubleSch.Vars[0].Rigid || doubleSch.Vars[0].Kind != types.General || len(doubleSch.Preds) != 1 || doubleSch.Preds[0].Class != "Basics.Num" {
		t.Errorf("double: want one ordinary variable constrained by Num, got %+v", doubleSch)
	}
}
