package infer

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/parser"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

func check(t *testing.T, src string) (*Checker, []DeclInfo, []error) {
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
	if errs := ck.InstallPrelude(); len(errs) > 0 {
		t.Fatalf("prelude errors: %v", errs)
	}
	infos, errs := ck.Module(m)
	var out []error
	for _, e := range errs {
		out = append(out, checkErr{e.Title, e.Span.StartPos().Line})
	}
	return ck, infos, out
}

type checkErr struct {
	title string
	line  int
}

func (e checkErr) Error() string { return e.title }

func TestInstallPreludeUsesDeclaredMetadata(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	ck := NewChecker(sup, b, NewEnv())
	if errs := ck.InstallPrelude(); len(errs) > 0 {
		t.Fatalf("prelude errors: %v", errs)
	}
	if ck.Natives["Basics.add"] == nil {
		t.Fatal("embedded Basics.add native metadata was not installed")
	}
	print := ck.Operations["print"]
	if print == nil || print.Native == nil || print.Native.Name != "IO.print" {
		t.Fatalf("ambient print does not reference its declared native: %+v", print)
	}
	if len(print.Scheme.Preds) != 1 || print.Scheme.Preds[0].Class != "Show" {
		t.Fatalf("print predicates = %+v, want one Show obligation", print.Scheme.Preds)
	}
	write := ck.Operations["IO.write"]
	if write == nil || write.Native == nil || write.Native.Name != "IO.write" {
		t.Fatalf("IO.write does not reference its declared native: %+v", write)
	}
}

func TestPositive(t *testing.T) {
	cases := []struct {
		src  string
		want string // "name : type" per decl, comma-separated, zonked
	}{
		{"x = 1", "x : number"},
		{"x = 1 + 2 * 3", "x : number"},
		// Generalization at each binding: later declarations instantiate
		// fresh number vars, which the shared test printer numbers.
		{"x = 40\ny = x + 2", "x : number, y : number2"},
		{"x = 1\ny = x\nmain = y - x", "x : number, y : number2, main : number3"},
		{"x = 1.5", "x : Float"},
		{"x = 1 + 0.5", "x : Float"},
		{"f = 1 / 2", "f : Float"}, // number literals unify with Float (Elm)
		{"s = \"a\" ++ \"b\"", "s : String"},
		{"b = 1 <= 2", "b : Bool"},
		{"t = True", "t : Bool"},
		{"x = if True then 1 else 2", "x : number"},
		{"x = -5", "x : number"},
		{"x = -2.5", "x : Float"},
		{"main = print (1 + 2)", "main : ()"},
		{"x =\n  a = 1\n  b = a + 2\n  a * b", "x : number"},
		{"x =\n  r = 2.0\n  r * r", "x : Float"},
		{"x : Int\nx = 1", "x : Int"},
		{"x : Float\nx = 1", "x : Float"}, // annotation forces the literal
		{"add x y = x + y", "add : number -> number -> number"},
		{"inc n = n + 1\nmain = inc 41", "inc : number -> number, main : number2"},
		{"fib n = if n < 2 then n else fib (n - 1) + fib (n - 2)", "fib : number -> number"},
		{"f = \\x -> x + 1", "f : number -> number"},
		{"add : Int -> Int -> Int\nadd x y = x + y", "add : Int -> Int -> Int"},
		{"pure() = 1", "pure : () -> number"},
		{"saved = readLine", "saved : () ->{IO} String"},
		{"main = print (readLine())", "main : ()"},
		{"make : () ->{IO} (() -> ())\nmake() =\n  print \"now\"\n  \\_ -> ()", "make : () ->{IO} () -> ()"},
		{"later : () -> (() ->{IO} ())\nlater() = \\_ -> print \"later\"", "later : () -> () ->{IO} ()"},
		{"add x y = x + y\ninc = add 1", "add : number -> number -> number, inc : number2 -> number2"},
		// Generalization: uses no longer pin the definition.
		{"id x = x\nmain = id 1 + 1", "id : a -> a, main : number"},
		{"v =\n  go n = if n < 1 then 0 else go (n - 1)\n  go 3", "v : number"},
	}
	for _, c := range cases {
		ck, infos, errs := check(t, c.src)
		if len(errs) > 0 {
			t.Errorf("%q: unexpected errors %v", c.src, errs)
			continue
		}
		var parts []string
		p := types.NewPrinter()
		for _, info := range infos {
			parts = append(parts, info.Name+" : "+p.Type(ck.Sub.Apply(info.Type)))
		}
		if got := strings.Join(parts, ", "); got != c.want {
			t.Errorf("%q: got %q, want %q", c.src, got, c.want)
		}
	}
}

func TestNegative(t *testing.T) {
	cases := []struct {
		src       string
		wantTitle string
		wantLine  int
	}{
		{"x = y", "NAMING ERROR", 1},
		{"x = 1\nx = 2", "MULTIPLE DEFINITIONS", 2},
		{"x = z + 1\nmain = x", "NAMING ERROR", 1},
		// Use-before-define is a naming error: source-order scoping.
		{"main = x\nx = 1", "NAMING ERROR", 1},
		{"x = 1 + \"a\"", "TYPE MISMATCH", 1},                 // WhyOperand
		{"x = 1 2", "TYPE MISMATCH", 1},                       // WhyCall: not a function
		{"x = if 1 then 2 else 3", "TYPE MISMATCH", 1},        // WhyIfCondition
		{"x = if True then 1 else \"a\"", "TYPE MISMATCH", 1}, // WhyIfBranches
		{"x = 1 == \"a\"", "TYPE MISMATCH", 1},                // WhyCompare
		{"x = -\"a\"", "TYPE MISMATCH", 1},                    // WhyNegate
		{"x = \"a\" / \"b\"", "TYPE MISMATCH", 1},             // WhyOpRequires
		{"x = 1 ++ \"a\"", "TYPE MISMATCH", 1},                // WhyOpRequires ++
		{"x = Just", "NAMING ERROR", 1},                       // unknown constructor
		{"x = print 1\nmain = x", "UNHANDLED EFFECT", 1},
		{"x = 1\ny =\n  x = 2\n  x + 1", "SHADOWING", 3},
		{"y =\n  a = 1\n  a = 2\n  a", "SHADOWING", 3},
		{"y =\n  a = b + 1\n  b = 2\n  a", "NAMING ERROR", 2},    // use-before-define in block
		{"x : String\nx = 1", "TYPE MISMATCH", 2},                // WhyAnnotation
		{"x : Foo\nx = 1", "NAMING ERROR", 1},                    // unknown type name
		{"x : a\nx = 1", "TYPE MISMATCH", 2},                     // annotation more general than the number body
		{"f : Int -> Int\nf = 1", "TYPE MISMATCH", 2},            // arrow annotation resolves, body mismatches
		{"saved : String\nsaved = readLine", "TYPE MISMATCH", 2}, // bare Unit function is not forced
		{"main x = x", "MAIN TAKES NO PARAMETERS", 1},
		{"f x x = x", "SHADOWING", 1},           // duplicate params
		{"f f = f", "SHADOWING", 1},             // param shadows the function itself
		{"x = 1\nf x = x + 1", "SHADOWING", 2},  // param shadows a top-level name
		{"f = \\x -> \\x -> x", "SHADOWING", 1}, // lambda param shadowing
		{"f x = f", "TYPE MISMATCH", 1},         // occurs check via the recursion var
	}
	for _, c := range cases {
		_, _, errs := check(t, c.src)
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

func TestEffectRows(t *testing.T) {
	src := "effect Console\n    write : String -> ()\n\nsay text = write text"
	ck, infos, errs := check(t, src)
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(infos) != 1 {
		t.Fatalf("want one value declaration, got %d", len(infos))
	}
	if got := types.Show(ck.Sub.Apply(infos[0].Type)); got != "String ->{Console} ()" {
		t.Fatalf("say type = %s, want String ->{Console} ()", got)
	}
	_, _, errs = check(t, "effect Console\n    write : String -> ()\n\nsay : String ->{Console | e} ()\nsay text = write text")
	if len(errs) > 0 {
		t.Fatalf("open effect annotation: %v", errs)
	}

	_, _, errs = check(t, "effect Console\n    write : String -> ()\n\nx = write \"hello\"")
	if len(errs) == 0 || errs[0].(checkErr).title != "UNHANDLED EFFECT" {
		t.Fatalf("top-level operation call: want UNHANDLED EFFECT, got %v", errs)
	}

	_, _, errs = check(t, "effect Console\n    write : String -> ()\n\nsay : String -> ()\nsay text = write text")
	if len(errs) == 0 || errs[0].(checkErr).title != "EFFECT MISMATCH" {
		t.Fatalf("pure annotation: want EFFECT MISMATCH, got %v", errs)
	}
	_, _, errs = check(t, "effect Console\n    write : String -> ()\n\nsay : String ->{Console} String\nsay text = text")
	if len(errs) == 0 || errs[0].(checkErr).title != "EFFECT MISMATCH" {
		t.Fatalf("overstated annotation: want EFFECT MISMATCH, got %v", errs)
	}

	_, _, errs = check(t, "effect Fail e\n    throw : e -> a\n\nfailString text = throw text")
	if len(errs) == 0 || errs[0].(checkErr).title != "OPERATION POLYMORPHISM NOT READY" {
		t.Fatalf("parameterized effect runtime staging: %v", errs)
	}

	ck, infos, errs = check(t, "effect Db\n    query : String -> Int -> String\n\nrun sql count = query sql count")
	if len(errs) > 0 {
		t.Fatalf("curried operation: %v", errs)
	}
	if got := types.Show(ck.Sub.Apply(infos[0].Type)); got != "String -> Int ->{Db} String" {
		t.Fatalf("run type = %s", got)
	}
}

func TestOpenRowUnification(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	e1, e2 := sup.FreshVar(types.RowVar), sup.FreshVar(types.RowVar)
	a := types.EffLabel{Unique: 10, Name: "A"}
	bb := types.EffLabel{Unique: 11, Name: "B"}
	sub := Subst{}
	if m := unify(types.Row{Labels: []types.EffLabel{a}, Tail: e1}, types.Row{Labels: []types.EffLabel{bb}, Tail: e2}, sub, b, sup); m != nil {
		t.Fatalf("unify open rows: %v", m)
	}
	left := sub.Apply(types.Row{Labels: []types.EffLabel{a}, Tail: e1})
	right := sub.Apply(types.Row{Labels: []types.EffLabel{bb}, Tail: e2})
	if !types.Equal(left, right) {
		t.Fatalf("rows did not converge: %v != %v", left, right)
	}
}

// Direct unifier tests for constraint-solver paths awkward to isolate through
// surface syntax:
// Number-kind rejection and the occurs check.
func TestUnifyNumberKind(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	sub := Subst{}

	n := sup.FreshVar(types.Number)
	if m := unify(n, b.Int, sub, b, &types.Supply{}); m != nil {
		t.Errorf("number ~ Int should unify: %v", m.note)
	}

	n2 := sup.FreshVar(types.Number)
	if m := unify(n2, b.String, sub, b, &types.Supply{}); m == nil {
		t.Error("number ~ String should fail")
	}

	// A general var unified with a number var must keep the Number kind.
	n3 := sup.FreshVar(types.Number)
	g := sup.FreshVar(types.General)
	if m := unify(g, n3, sub, b, &types.Supply{}); m != nil {
		t.Fatalf("general ~ number should unify")
	}
	if m := unify(g, b.Bool, sub, b, &types.Supply{}); m == nil {
		t.Error("after merging with a number var, Bool should be rejected")
	}
}

func TestUnifyOccurs(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	sub := Subst{}
	v := sup.FreshVar(types.General)
	fn := &types.TFun{Arg: v, Ret: b.Int}
	if m := unify(v, fn, sub, b, &types.Supply{}); m == nil {
		t.Error("occurs check should reject v ~ (v -> Int)")
	}
}

func TestUnifyTConIdentityIsUnique(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	sub := Subst{}
	// Same name, different unique — a redefined REPL type must not unify.
	otherInt := &types.TCon{Unique: sup.NextUnique(), Name: "Int"}
	if m := unify(b.Int, otherInt, sub, b, &types.Supply{}); m == nil {
		t.Error("TCons with equal names but different uniques must not unify")
	}
}
