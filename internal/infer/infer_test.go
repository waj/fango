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

func check(t *testing.T, src string) (*infer.Checker, []infer.DeclInfo, []error) {
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

type checkErr struct {
	title string
	line  int
}

func TestMissingBlockResultIsCheckedAfterParsing(t *testing.T) {
	for _, src := range []string{
		"value =\n    x = 1\n",
		"value = (\\_ ->\n    x = 1\n)\n",
		"value = x = 1\n",
	} {
		_, _, errs := check(t, src)
		found := false
		for _, err := range errs {
			if err.Error() == "BLOCK RESULT" {
				found = true
			}
		}
		if !found {
			t.Errorf("%q: wanted BLOCK RESULT, got %v", src, errs)
		}
	}
}

func (e checkErr) Error() string { return e.title }

func TestInstallPreludeUsesDeclaredMetadata(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	ck := infer.NewChecker(sup, b, infer.NewEnv())
	staging.Install(ck)
	if errs := ck.InstallPrelude(); len(errs) > 0 {
		t.Fatalf("prelude errors: %v", errs)
	}
	if ck.Natives["Basics.intAdd"] == nil || ck.Methods["Basics.+"].Class != ck.Classes["Basics.Num"] {
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

func TestParserOnlyNominalRecords(t *testing.T) {
	_, _, errs := check(t, `type Box a = { value : a }

unbox : Box a -> a
unbox box = box.value

main = unbox (Box { value = 42 })
`)
	if len(errs) > 0 {
		t.Fatalf("record errors: %v", errs)
	}
}

// TestInferredRecords pins where the nominal type of an inferred `{ ... }` may
// come from, including the shapes no expected type reaches syntactically.
func TestInferredRecords(t *testing.T) {
	_, _, errs := check(t, `type Point = { x : Int, y : Int }
type Pair a = { first : a, second : a }
type L a = Nil | Cons a (L a)

fromAnnotation : Point
fromAnnotation = { x = 1, y = 2 }

fromResult : Int -> Point
fromResult n = { x = n, y = n }

fromField = Pair { first = Point { x = 1, y = 2 }, second = { x = 3, y = 4 } }

sumP : Point -> Int
sumP p = p.x + p.y

fromParameter = sumP { x = 5, y = 6 }

fromConstructorSpine : L Point
fromConstructorSpine = Cons ({ x = 1, y = 2 }) Nil

fromSkolem : a -> Pair a
fromSkolem v = { first = v, second = v }

fromPattern : Pair Int -> Int
fromPattern { first = f } = f
`)
	if len(errs) > 0 {
		t.Fatalf("inferred record errors: %v", errs)
	}
}

// A block-local binding solves its own constraints early. That must not
// finalize an obligation raised by the enclosing declaration, whose deciding
// code the checker has not reached yet.
func TestInferredRecordSurvivesLocalBinding(t *testing.T) {
	_, _, errs := check(t, `type Point = { x : Int, y : Int }

describe : Point -> Int
describe p = p.x

main =
    p = { x = 1, y = 2 }
    label v = v
    describe p + label 0
`)
	if len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
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
		{"saved = readLine", "saved : () ->{IO} Maybe Line"},
		{"main = print (readLine())", "main : ()"},
		{"make : () ->{IO} (() -> ())\nmake() =\n  print \"now\"\n  \\_ -> ()", "make : () ->{IO} () -> ()"},
		{"later : () -> (() ->{IO} ())\nlater() = \\_ -> print \"later\"", "later : () -> () ->{IO} ()"},
		{"add x y = x + y\ninc = add 1", "add : Num a => a -> a -> a, inc : Num b => b -> b"},
		{"values = [1, 2]\nfirstOf xs = case xs of\n  [] -> 0\n  [first | _] -> first", "values : Num a => List a, firstOf : Num b => List b -> b"},
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

func checkedScheme(ck *infer.Checker, info infer.DeclInfo) types.Scheme {
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
		{"main = 1 2", "AMBIGUOUS CONSTRAINT", 1},                // A function-shaped numeric obligation cannot default.
		{"x = if 1 then 2 else 3", "MISSING INSTANCE", 1},        // WhyIfCondition
		{"x = if True then 1 else \"a\"", "MISSING INSTANCE", 1}, // WhyIfBranches
		{"x = 1 == \"a\"", "MISSING INSTANCE", 1},                // WhyCompare
		{"x = -\"a\"", "MISSING INSTANCE", 1},                    // WhyNegate
		{"x = \"a\" / \"b\"", "TYPE MISMATCH", 1},                // WhyOpRequires
		{"x = 1 ++ \"a\"", "MISSING INSTANCE", 1},                // WhyOpRequires ++
		{"x = [1, True]", "MISSING INSTANCE", 1},                 // every list element has one type
		{"x = [1 | True]", "TYPE MISMATCH", 1},                   // tail must be a List
		{"x = Absent", "NAMING ERROR", 1},                        // unknown constructor
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
		{"type R = { value : Int }\nx = R {}", "RECORD FIELDS", 2},
		{"type R = { value : Int }\nget : R -> Int\nget r = r.missing", "UNKNOWN FIELD", 3},
		{"type R = { value : Int }\nget r = r.value", "AMBIGUOUS FIELD", 2},
		{"x = \"text\".value", "NOT A RECORD", 1},
		{"type R = { value : Int, value : Int }\nx = 1", "RECORD FIELDS", 1},
		// An inferred literal no context reaches stays ambiguous even when one
		// record in scope has exactly its labels: labels never choose a type.
		{"type R = { value : Int }\nx = { value = 1 }", "AMBIGUOUS RECORD", 2},
		{"type R = { a : Int, b : Int }\nx : R\nx = { a = 1 }", "RECORD FIELDS", 3},
		{"type R = { a : Int }\nx : R\nx = { a = 1, b = 2 }", "UNKNOWN FIELD", 3},
		{"type R = { a : Int }\nx : R\nx = { a = 1, a = 2 }", "RECORD FIELDS", 3},
		{"type R = { a : Int }\nx : Int\nx = { a = 1 }", "NOT A RECORD", 3},
		// A capitalized name before `{` always names the record, so this is a
		// literal of a record called `Wrap`, not `Wrap` applied to one.
		{"type R = { a : Int }\ntype W = Wrap R\nw = Wrap { a = 1 }", "UNKNOWN RECORD", 3},
		{"type R = { a : Int }\nf r = r.a\ng x = x\nmain : R -> Int\nmain r = f r", "AMBIGUOUS FIELD", 2},
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
