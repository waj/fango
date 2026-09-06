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
	if ck.Natives["Basics.intAdd"] == nil || ck.Methods["Basics.add"].Class != ck.Classes["Basics.Num"] {
		t.Fatal("embedded scalar natives and Num methods were not installed")
	}
	print, ok := ck.Env.Lookup("print")
	if !ok || ck.Operations["print"] != nil {
		t.Fatal("ambient print must be an ordinary constrained function")
	}
	if len(print.Preds) != 1 || print.Preds[0].Class != "Basics.Show" {
		t.Fatalf("print predicates = %+v, want one Show obligation", print.Preds)
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
		{"x = 1", "x : Num a => a"},
		{"x = 1 + 2 * 3", "x : Num a => a"},
		// Generalization at each binding: later declarations instantiate
		// fresh number vars, which the shared test printer numbers.
		{"x = 40\ny = x + 2", "x : Num a => a, y : Num b => b"},
		{"x = 1\ny = x\nmain = y - x", "x : Num a => a, y : Num b => b, main : Int"},
		{"x = 1.5", "x : Float"},
		{"x = 1 + 0.5", "x : Float"},
		{"f = 1 / 2", "f : Float"}, // number literals unify with Float (Elm)
		{"s = \"a\" ++ \"b\"", "s : String"},
		{"b = 1 <= 2", "b : Bool"},
		{"t = True", "t : Bool"},
		{"x = if True then 1 else 2", "x : Num a => a"},
		{"x = -5", "x : Num a => a"},
		{"x = -2.5", "x : Float"},
		{"main = print (1 + 2)", "main : ()"},
		{"x =\n  a = 1\n  b = a + 2\n  a * b", "x : Num a => a"},
		{"x =\n  r = 2.0\n  r * r", "x : Float"},
		{"x : Int\nx = 1", "x : Int"},
		{"x : Float\nx = 1", "x : Float"}, // annotation forces the literal
		{"add x y = x + y", "add : Num a => a -> a -> a"},
		{"inc n = n + 1\nmain = inc 41", "inc : Num a => a -> a, main : Int"},
		{"fib n = if n < 2 then n else fib (n - 1) + fib (n - 2)", "fib : (Num a, Ord a) => a -> a"},
		{"f = \\x -> x + 1", "f : Num a => a -> a"},
		{"add : Int -> Int -> Int\nadd x y = x + y", "add : Int -> Int -> Int"},
		{"pure() = 1", "pure : Num a => () -> a"},
		{"saved = readLine", "saved : () ->{IO} String"},
		{"main = print (readLine())", "main : ()"},
		{"make : () ->{IO} (() -> ())\nmake() =\n  print \"now\"\n  \\_ -> ()", "make : () ->{IO} () -> ()"},
		{"later : () -> (() ->{IO} ())\nlater() = \\_ -> print \"later\"", "later : () -> () ->{IO} ()"},
		{"add x y = x + y\ninc = add 1", "add : Num a => a -> a -> a, inc : Num b => b -> b"},
		// Generalization: uses no longer pin the definition.
		{"id x = x\nmain = id 1 + 1", "id : a -> a, main : Int"},
		{"v =\n  go n = if n < 1 then 0 else go (n - 1)\n  go 3", "v : Num a => a"},
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
			parts = append(parts, info.Name+" : "+p.Scheme(checkedScheme(ck, info)))
		}
		if got := strings.Join(parts, ", "); got != c.want {
			t.Errorf("%q: got %q, want %q", c.src, got, c.want)
		}
	}
}

func checkedScheme(ck *Checker, info DeclInfo) types.Scheme {
	s := info.Scheme
	s.Body = ck.Sub.Apply(s.Body)
	s.Preds = ck.NormalizePreds(s.Preds)
	return s
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
		{"x = 1 + \"a\"", "MISSING INSTANCE", 1},                 // WhyOperand
		{"x = 1 2", "MISSING INSTANCE", 1},                       // WhyCall: not a function
		{"x = if 1 then 2 else 3", "MISSING INSTANCE", 1},        // WhyIfCondition
		{"x = if True then 1 else \"a\"", "MISSING INSTANCE", 1}, // WhyIfBranches
		{"x = 1 == \"a\"", "MISSING INSTANCE", 1},                // WhyCompare
		{"x = -\"a\"", "MISSING INSTANCE", 1},                    // WhyNegate
		{"x = \"a\" / \"b\"", "TYPE MISMATCH", 1},                // WhyOpRequires
		{"x = 1 ++ \"a\"", "MISSING INSTANCE", 1},                // WhyOpRequires ++
		{"x = Just", "NAMING ERROR", 1},                          // unknown constructor
		{"x = print 1\nmain = x", "UNHANDLED EFFECT", 1},
		{"x = 1\ny =\n  x = 2\n  x + 1", "SHADOWING", 3},
		{"y =\n  a = 1\n  a = 2\n  a", "SHADOWING", 3},
		{"y =\n  a = b + 1\n  b = 2\n  a", "NAMING ERROR", 2},    // use-before-define in block
		{"x : String\nx = 1", "MISSING INSTANCE", 2},             // WhyAnnotation
		{"x : Foo\nx = 1", "NAMING ERROR", 1},                    // unknown type name
		{"x : a\nx = 1", "MISSING CONSTRAINT", 1},                // annotation more general than the number body
		{"f : Int -> Int\nf = 1", "MISSING INSTANCE", 2},         // arrow annotation resolves, body mismatches
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

// Class constraints are independent of unification's ordinary type kind.
func TestOrdinaryVariablesHaveNoNumericKind(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	for _, ty := range []types.Type{b.Int, b.Float, b.String, b.Bool} {
		if m := unify(sup.FreshVar(types.General), ty, Subst{}, b, sup); m != nil {
			t.Errorf("ordinary variable should unify with %s", types.Show(ty))
		}
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
