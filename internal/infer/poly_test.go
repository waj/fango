package infer

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/parser"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// checkPoly exercises generalization,
// parameterized types, and annotation variables are live.
func checkPoly(t *testing.T, src string) (*Checker, []DeclInfo, []error) {
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
	ck := NewChecker(sup, b, NewEnv())
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
		{"double x = x + x", "double : number -> number"},
		// Generalization at each binding: two uses at two types both check.
		{"id x = x\na = id 1\nb = id \"s\"", "id : a -> a, a : number, b : String"},
		// Number vars generalize (doc/design.md, "Type inference"): usable at Int and Float.
		// a's number var is a fresh instantiation, distinct from double's —
		// the shared printer numbers it number2.
		{"double x = x + x\na = double 2\nb = double 1.5", "double : number -> number, a : number2, b : Float"},
		// Annotated polymorphism, checked by skolemize-and-unify.
		{"id : a -> a\nid x = x", "id : a -> a"},
		{"apply : (a -> b) -> a -> b\napply f x = f x", "apply : (a -> b) -> a -> b"},
		// An annotation may be less general than the body.
		{"idInt : Int -> Int\nidInt x = x", "idInt : Int -> Int"},
		// Parameterized ADTs: constructor instantiation per occurrence.
		{"type Maybe a = Nothing | Just a\nx = Just 1\ny = Just \"s\"", "x : Maybe number, y : Maybe String"},
		{"type Maybe a = Nothing | Just a\nn = Nothing", "n : Maybe a"},
		// Patterns instantiate constructors too.
		{"type Maybe a = Nothing | Just a\nf m = case m of\n    Nothing -> 0\n    Just n -> n + 1", "f : Maybe number -> number"},
		{"type Box a = MkBox a\nunbox b = case b of\n    MkBox x -> x", "unbox : Box a -> a"},
		// Applied types in annotations.
		{"type Maybe a = Nothing | Just a\nx : Maybe Int\nx = Just 1", "x : Maybe Int"},
		{"type Maybe a = Nothing | Just a\nf : Maybe a -> Maybe a\nf m = m", "f : Maybe a -> Maybe a"},
		// A recursive parameterized type, regular occurrences only.
		{"type List a = Nil | Cons a (List a)\nlen xs = case xs of\n    Nil -> 0\n    Cons _ rest -> 1 + len rest", "len : List a -> number"},
		// Local (block) polymorphism: one local used at two types.
		{"v =\n  id2 y = y\n  a = id2 1\n  b = id2 \"s\"\n  a", "v : number"},
		// Mutually recursive parameterized types, regular.
		{"type A a = MkA (B a) | EndA\ntype B a = MkB (A a)\nf x = MkA (MkB x)", "f : A a -> A a"},
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
			parts = append(parts, info.Name+" : "+p.Type(ck.Sub.Apply(info.Scheme.Body)))
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
		{"f : a -> a\nf x = x + 1", "TYPE MISMATCH", 2},
		// Polymorphic recursion is rejected by construction: the recursive
		// occurrence is the pre-bound monomorphic self, so nesting occurs.
		{"type Box a = MkBox a\nf b = f (MkBox b)", "TYPE MISMATCH", 2},
		// ... and an annotation does not re-enable it (self stays the meta;
		// the occurs failure surfaces at the annotation unification).
		{"type Box a = MkBox a\nf : Box a -> Int\nf b = f (MkBox b)", "TYPE MISMATCH", 2},
		// Non-regular recursive types.
		{"type Pair a b = MkPair a b\ntype T a = Leaf | Node (T (Pair a a))", "NON-REGULAR TYPE", 2},
		{"type A a = MkA (B (A a)) | EndA\ntype B a = MkB (A a)", "NON-REGULAR TYPE", 1},
		// Type-application arity.
		{"type Maybe a = Nothing | Just a\nx : Maybe\nx = Nothing", "TYPE ARITY", 2},
		{"type Maybe a = Nothing | Just a\nx : Maybe Int Int\nx = Nothing", "TYPE ARITY", 2},
		{"x : Int Int\nx = 1", "TYPE ARITY", 1},
		// Constructor fields resolve in the closed parameter scope.
		{"type T a = MkT b", "NAMING ERROR", 1},
		{"type T a a = MkT a", "SHADOWING", 1},
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
	if len(constSch.Vars) != 4 {
		t.Fatalf("const: want 2 type vars and 2 row vars, got %d", len(constSch.Vars))
	}
	wantKinds := []types.VarKind{types.General, types.RowVar, types.General, types.RowVar}
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
	if len(doubleSch.Vars) != 2 || !doubleSch.Vars[0].Rigid || doubleSch.Vars[0].Kind != types.Number || doubleSch.Vars[1].Kind != types.RowVar {
		t.Errorf("double: want a rigid Number and RowVar, got %+v", doubleSch.Vars)
	}
}
