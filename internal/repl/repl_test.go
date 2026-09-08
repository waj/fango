package repl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/testutil"
)

// Transcript goldens: each testdata/repl/*.in is a scripted session; the
// golden *.txt is the complete output stream, prompts included.
func TestTranscripts(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "testdata", "repl", "*.in"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no testdata/repl/*.in files")
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			script, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var out strings.Builder
			Run(strings.NewReader(string(script)), &out)
			testutil.Golden(t, strings.TrimSuffix(path, ".in")+".txt", out.String())
		})
	}
}

// Session resilience: errors mid-session must not kill it or corrupt state.
func TestErrorRecovery(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader("x = 40\nnope\nx = )\nx + 2\n:quit\n"), &out)
	got := out.String()
	if !strings.Contains(got, "NAMING ERROR") {
		t.Errorf("expected a NAMING ERROR for `nope`:\n%s", got)
	}
	if !strings.Contains(got, "42 : Int") {
		t.Errorf("x should still evaluate after mid-session errors:\n%s", got)
	}
}

func TestRedefinition(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader("x = 1\ny = x + 1\ny\nx = 10\nx + 1\ny\n:quit\n"), &out)
	got := out.String()
	// y was forced at 2 before x's redefinition; the memoized cell stays.
	if strings.Count(got, "2 : Int") < 2 {
		t.Errorf("expected memoized y = 2 before and after redefinition:\n%s", got)
	}
	if !strings.Contains(got, "11 : Int") {
		t.Errorf("expected x + 1 = 11 after redefinition:\n%s", got)
	}
}

func TestPromptAndReadLineShareReader(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader("readLine ()\nhello\n:quit\n"), &out)
	if !strings.Contains(out.String(), `Just Line { text = hello`) {
		t.Fatalf("readLine did not consume the line following the prompt expression:\n%s", out.String())
	}
}

func TestNominalRecords(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader("type Box a = { value : a }\nBox { value = 42 }.value\n:quit\n"), &out)
	if !strings.Contains(out.String(), "Box : record") || !strings.Contains(out.String(), "42 : Num a => a") {
		t.Fatalf("record construction and projection failed in the REPL:\n%s", out.String())
	}
}

func TestListSyntax(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader("[1, 2]\n:quit\n"), &out)
	if !strings.Contains(out.String(), "[1, 2] : Num a => List a") {
		t.Fatalf("list construction or display failed in the REPL:\n%s", out.String())
	}
}

func TestClassInstanceTransactions(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader(`class Twice a
    twice : a -> a

instance Twice Int
    twice x = "wrong"

instance Twice Int
    twice x = x + x

answer : Int
answer = twice 21
answer
class Twice a
    twice : a -> a

answer
type T = T (Int -> Int) deriving (Show)
type T = T Int deriving (Show)
T 42
:quit
`), &out)
	got := out.String()
	if !strings.Contains(got, "TYPE MISMATCH") || !strings.Contains(got, "MULTIPLE DEFINITIONS") || !strings.Contains(got, "MISSING INSTANCE") {
		t.Fatalf("missing intended diagnostics: %s", got)
	}
	if strings.Contains(got, "OVERLAPPING INSTANCE") || !strings.Contains(got, "T 42 : T") || strings.Count(got, "42 : Int") != 2 {
		t.Fatalf("failed declaration poisoned the session: %s", got)
	}
}

func TestBlanketInstanceTransactions(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader(`class Label a
    label : a -> String

class Other a
    other : a -> String

instance Other a => Label a
    label x = other x

instance Label a => Other a
    other x = label x

instance Other a
    other x = "fallback"

instance Label String
    label x = "special"

forward x = label x
forward "secret"
forward True
forward 42
:type forward
:quit
`), &out)
	got := out.String()
	for _, want := range []string{"INSTANCE CONTEXT", "Other -> Label -> Other", "special", "fallback", "Label a => a -> String"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"OVERLAPPING INSTANCE", "MISSING INSTANCE", "AMBIGUOUS CONSTRAINT"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("failed declaration poisoned session:\n%s", got)
		}
	}
}

func TestContextPrecedenceTransactions(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader(`class Inspect a
    inspect : a -> String

instance Inspect a
    inspect x = "fallback"

old() = inspect True
forward x = inspect x
instance Show a => Inspect a
    inspect x = 42

instance Show a => Inspect a
    inspect x = "show"

old()
forward True
instance Eq a => Inspect a
    inspect x = "eq"

old()
forward True
instance Show b => Inspect b
    inspect x = "duplicate"

forward True
:quit
`), &out)
	got := out.String()
	for _, want := range []string{"MISSING INSTANCE", "OVERLAPPING INSTANCE", "Inspect a => a -> String"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
	for value, count := range map[string]int{"fallback : String": 2, "show : String": 1, "eq : String": 2} {
		if strings.Count(got, value) != count {
			t.Fatalf("wrong count for %q:\n%s", value, got)
		}
	}
}

func TestEffectfulPromptDeclarationRejected(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader("x = print 1\n40 + 2\n:quit\n"), &out)
	if !strings.Contains(out.String(), "EFFECTFUL PROMPT DECLARATION") {
		t.Fatalf("effectful declaration was installed:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "42 : Num a => a") {
		t.Fatalf("session did not recover after rejecting declaration:\n%s", out.String())
	}
}

func TestEffectfulInstanceConstructionRollsBack(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader(`class C a
    c : a -> a

instance C Int
    c =
        print "must not run"
        \value -> value

instance C Int
    c value = value

answer : Int
answer = c 42
answer
:quit
`), &out)
	got := out.String()
	if !strings.Contains(got, "UNHANDLED EFFECT") || strings.Contains(got, "OVERLAPPING INSTANCE") || !strings.Contains(got, "42 : Int") {
		t.Fatalf("failed instance construction poisoned the session: %s", got)
	}
	if strings.Contains(got, "\n> must not run\n") {
		t.Fatal("instance construction executed IO")
	}
}

func TestPromptNullaryFunctionRunsOnlyWhenCalled(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader("say : () ->{IO} ()\nsay() = print \"ok\"\n:type say\nsay()\nsay()\n:quit\n"), &out)
	if strings.Count(out.String(), "ok\n") != 2 {
		t.Fatalf("nullary function should run only on its two explicit calls:\n%s", out.String())
	}
}
