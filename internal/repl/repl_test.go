package repl

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/testutil"
)

// Transcript goldens: each testdata/repl/*.in is a scripted session; the
// golden *.txt is the complete output stream, prompts included. A directory
// beside the script with the same base name is the session's source root,
// holding the modules its imports name.
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
			base := strings.TrimSuffix(path, ".in")
			var opts Options
			if info, err := os.Stat(base); err == nil && info.IsDir() {
				opts.Root = base
			}
			var out strings.Builder
			RunWith(strings.NewReader(string(script)), &out, opts)
			testutil.Golden(t, base+".txt", out.String())
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
	if !strings.Contains(out.String(), `Just (Line { text = "hello"`) {
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

func TestImportAsyncFromCheckoutRoot(t *testing.T) {
	var out strings.Builder
	RunWith(strings.NewReader("import Async\n:quit\n"), &out, Options{Root: filepath.Join("..", "..")})
	got := out.String()
	if !strings.Contains(got, "loaded Async\n") || strings.Contains(got, "RESERVED MODULE") {
		t.Fatalf("Async import failed from source root containing runtime/:\n%s", got)
	}
}

func TestExplicitStreamRunsInREPL(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader(`import Stream
import Iterator
produce = Stream.fromList [10, 20, 30]
Stream.forEach print produce
:quit
`), &out)
	got := out.String()
	if !strings.Contains(got, "10\n20\n30\n") || !strings.Contains(got, "() : ()") {
		t.Fatalf("owned generator did not run in the REPL:\n%s", got)
	}
}

func TestStreamResidualRowsInREPLAndStaging(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader(`import Stream
import Iterator
import Fail
import Meta
source : Stream.Stream Int (Fail.Fail String)
source = Stream.unfold 0 { state ->
    if state == 0 then Just (7, 1) else Fail.fail "finished" }
readAll() = Fail.attempt { _ -> Stream.toList source }
readAll()
answer : String
answer = $(Meta.lift (show (readAll())))
answer
readAll()
:quit
`), &out)
	got := out.String()
	if strings.Contains(got, "INTERNAL") || strings.Contains(got, "runtime error") || strings.Count(got, `Err "finished" :`) < 2 || !strings.Contains(got, `"Err \"finished\"" : String`) {
		t.Fatalf("latent row traversal did not survive REPL/staging boundaries:\n%s", got)
	}
}

func TestExplicitStreamStagesAndRollsBackInREPL(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader(`import Meta
early : Bool
early = $(Meta.lift True)
import Stream
import Iterator
produce = Stream.fromList [10, 20]
total : () -> Int
total() = Stream.fold { element acc -> element + acc } 0 produce
bad : String
bad = $(Meta.lift (total()))
:type bad
answer : Int
answer = $(Meta.lift (total()))
answer
$(Meta.lift (total()))
:quit
`), &out)
	got := out.String()
	if strings.Contains(got, "INTERNAL") || strings.Contains(got, "runtime error") {
		t.Fatalf("staged traversal failed:\n%s", got)
	}
	if !strings.Contains(got, "I don't know a value named `bad`.") || strings.Count(got, "30 : Int") < 2 {
		t.Fatalf("failed expansion leaked a declaration or prevented the next traversal:\n%s", got)
	}
}

func TestStreamCleanupAvailableInREPL(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader(`import Stream
import Iterator
import Runtime.Scope
withCleanup action cleanup = Runtime.Scope.finally action cleanup
run() = withCleanup { _ -> Stream.forEach print (Stream.fromList [1]) } { _ -> print "closed" }
:type run
run()
:quit
`), &out)
	got := out.String()
	if !strings.Contains(got, "run : () ->{Console} ()") || !strings.Contains(got, "1\n") {
		t.Fatalf("cleanup contract or following traversal failed:\n%s", got)
	}
	if strings.Contains(got, "INTERNAL") || strings.Contains(got, "CAPTURE CHECK ERROR") {
		t.Fatalf("cleanup escaped the source proof boundary:\n%s", got)
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
	for value, count := range map[string]int{`"fallback" : String`: 2, `"show" : String`: 1, `"eq" : String`: 2} {
		if strings.Count(got, value) != count {
			t.Fatalf("wrong count for %q:\n%s", value, got)
		}
	}
}

// A value declaration that performs effects runs once, when entered, and
// binds its result: a later use does not run the body again.
func TestEffectfulPromptDeclarationRunsOnce(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader("x = print 1\nx\n40 + 2\n:quit\n"), &out)
	got := out.String()
	for _, want := range []string{"> 1\nx : ()\n", "() : ()\n", "42 : Num a => a"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "> 1\n") != 1 {
		t.Fatalf("effectful declaration ran more than once:\n%s", got)
	}
}

// Importing Time.Clock moves evaluation into the native worker, where every
// input reinstalls the session's program: an effectful declaration's value
// must survive that, and the level it was computed under ending.
func TestEffectfulPromptDeclarationInWorker(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader("import Time.Clock\nuse Time.Clock.runSystem\nt = Time.Clock.now()\nt\nt\n:end\nt\n:quit\n"), &out)
	got := out.String()
	if !strings.Contains(got, "t : DateTime\n") {
		t.Fatalf("missing type echo:\n%s", got)
	}
	var values []string
	for _, line := range strings.Split(got, "\n") {
		if strings.HasSuffix(line, " : DateTime") && strings.Contains(line, "DateTime \"") {
			values = append(values, line[strings.Index(line, "DateTime \""):])
		}
	}
	if len(values) != 3 || values[0] != values[1] || values[1] != values[2] {
		t.Fatalf("expected one clock reading shown three times, got %q:\n%s", values, got)
	}
}

// A value computed in the worker is sealed there under its stamp, so later
// inputs and the end of its level keep it; a rebuilt worker never held it, so
// using the value there is an error rather than a second run of the effect.
func TestEffectfulPromptDeclarationAcrossWorkerRebuild(t *testing.T) {
	root := t.TempDir()
	write := func(name, contents string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Hash.fango", "module Hash exposing (twice)\n\ntwice : Int -> Int\ntwice = native\n")
	write("Hash.native.go", "package native\n\nfunc Twice(x int64) int64 { return x * 2 }\n")
	write("Hash2.fango", "module Hash2 exposing (thrice)\n\nthrice : Int -> Int\nthrice = native\n")
	write("Hash2.native.go", "package native\n\nfunc Thrice(x int64) int64 { return x * 3 }\n")
	var out strings.Builder
	RunWith(strings.NewReader("import Hash\nimport State\nuse State.run 0\nn = State.get()\nState.put 5\nn\nn\nm = State.get()\n:end\nn\nm\nimport Hash2\nn\nHash2.thrice 2\nn = 7\nn\n:quit\n"), &out, Options{Root: root})
	got := out.String()
	for _, want := range []string{"n : Int\n", "m : Int\n", "loaded Hash2\n", "6 : Int\n", "7 : Int\n", "holds the result of an effectful declaration"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "0 : Int\n") != 3 || strings.Count(got, "5 : Int\n") != 1 {
		t.Fatalf("value did not survive the worker's program reinstall:\n%s", got)
	}
}

// A value computed in process before the session needs a worker is not
// carried into it: memos never cross the wire, so the worker reports it.
func TestEffectfulPromptDeclarationBeforeWorker(t *testing.T) {
	root := t.TempDir()
	write := func(name, contents string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Hash.fango", "module Hash exposing (twice)\n\ntwice : Int -> Int\ntwice = native\n")
	write("Hash.native.go", "package native\n\nfunc Twice(x int64) int64 { return x * 2 }\n")
	var out strings.Builder
	RunWith(strings.NewReader("x = print 1\nimport Hash\nx\n:quit\n"), &out, Options{Root: root})
	got := out.String()
	if !strings.Contains(got, "holds the result of an effectful declaration") || strings.Count(got, "> 1\n") != 1 {
		t.Fatalf("expected the worker to report the stale value:\n%s", got)
	}
}

func TestEffectfulInstanceConstructionRollsBack(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader(`class C a
    c : a -> a

instance C Int
    c =
        print "must not run"
        { value -> value }

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
	Run(strings.NewReader("say : () ->{Console} ()\nsay() = print \"ok\"\n:type say\nsay()\nsay()\n:quit\n"), &out)
	if strings.Count(out.String(), "ok\n") != 2 {
		t.Fatalf("nullary function should run only on its two explicit calls:\n%s", out.String())
	}
}

// A user sidecar imported at the prompt runs in a worker built over the
// bundled sidecars and it, so `print` keeps working after the swap.
func TestImportNativeSidecar(t *testing.T) {
	root := t.TempDir()
	write := func(name, contents string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Hash.fango", "module Hash exposing (twice)\n\ntwice : Int -> Int\ntwice = native\n")
	write("Hash.native.go", "package native\n\nfunc Twice(x int64) int64 { return x * 2 }\n")
	var out strings.Builder
	RunWith(strings.NewReader("import Hash\nHash.twice 21\nprint \"still\"\n:quit\n"), &out, Options{Root: root})
	got := out.String()
	for _, want := range []string{"loaded Hash\n", "42 : Int\n", "still\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
}

// Native objects stay in the automatically generated worker, including across
// prompt inputs. A third-party module needs no in-process registry adapters.
func TestImportNativeSidecarOpaqueState(t *testing.T) {
	root := t.TempDir()
	write := func(name, contents string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Counter.fango", `module Counter exposing (Cell, newCell, add)
import Runtime.Native
type Cell = Cell Runtime.Native.Any
newCell : Int ->{IO} Cell
newCell = native
add : Cell -> Int ->{IO} Int
add = native
`)
	write("Counter.native.go", `package native
func NewCell(value int64) any { return &value }
func Add(cell any, increment int64) int64 {
    value := cell.(*int64)
    *value += increment
    return *value
}
`)
	var out strings.Builder
	RunWith(strings.NewReader("import Counter\ncell = Counter.newCell 40\nCounter.add cell 2\nCounter.add cell 3\n:quit\n"), &out, Options{Root: root})
	got := out.String()
	for _, want := range []string{"loaded Counter\n", "42 : Int\n", "45 : Int\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
}

func TestModuleFunctionImportRollback(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "Helpers.fango")
	bad := `module Helpers exposing (first)
first() = second()
second : () -> Int
second() = "bad"
`
	if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
		t.Fatal(err)
	}
	// Edit the module between inputs while preserving one live session.
	reader := &moduleRetryReader{path: path, inputs: []string{"import Helpers\n", "import Helpers\nHelpers.first()\n:quit\n"}}
	var out strings.Builder
	RunWith(reader, &out, Options{Root: root})
	got := out.String()
	if !strings.Contains(got, "TYPE MISMATCH") || strings.Count(got, "loaded Helpers") != 1 || !strings.Contains(got, "42 : Int") {
		t.Fatalf("import rollback: %s", got)
	}
}

type moduleRetryReader struct {
	path   string
	inputs []string
	offset int
}

func (r *moduleRetryReader) Read(p []byte) (int, error) {
	if len(r.inputs) == 0 {
		return 0, io.EOF
	}
	if r.offset == len(r.inputs[0]) {
		r.inputs = r.inputs[1:]
		r.offset = 0
		if len(r.inputs) == 0 {
			return 0, io.EOF
		}
		if err := os.WriteFile(r.path, []byte("module Helpers exposing (first)\nfirst() = second()\nsecond : () -> Int\nsecond() = 42\n"), 0600); err != nil {
			return 0, err
		}
	}
	n := copy(p, r.inputs[0][r.offset:])
	r.offset += n
	return n, nil
}

// A runtime failure at the prompt reports the Fango stack it unwound
// through, with the prompt's own line as the outermost frame.
func TestRuntimeErrorShowsTrace(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader(`import Basics
divide : Int -> Int -> Int
divide a b = Basics.quotientBy b a
fraction n = divide 100 n + 1
fraction 0
:quit
`), &out)
	got := out.String()
	if !strings.Contains(got, "runtime error: ") || !strings.Contains(got, "integer divide by zero") {
		t.Fatalf("division by zero was not reported as a runtime error:\n%s", got)
	}
	if strings.Contains(got, "runtime error: runtime error") || !strings.Contains(got, "Fango trace:\n  at divide (<repl>:2)\n  at fraction (<repl>:1)\n  at <expression> (<repl>:1)") {
		t.Fatalf("runtime error carries no Fango trace:\n%s", got)
	}
}

// A prompt declaration is bound under its bare name, which is also the form an
// imported module's parameters and pattern variables take; the module is
// checked against its own scope, so the names may coincide. The cache is off
// so the module is checked from source rather than served as a stored object.
func TestImportAfterPromptDeclaration(t *testing.T) {
	var out strings.Builder
	script := "a = 1\nb = 2\nx1 = 3\nimport Geometry.Point exposing (origin, manhattan)\nmanhattan origin (Geometry.Point.Point 3 4)\na + b\nx1\n:quit\n"
	RunWith(strings.NewReader(script), &out, Options{Root: filepath.Join("..", "..", "testdata", "repl", "import"), DisableCache: true})
	got := out.String()
	if strings.Contains(got, "SHADOWING") {
		t.Fatalf("prompt names leaked into the imported module's shadowing check:\n%s", got)
	}
	for _, want := range []string{"loaded Geometry.Point", "7 : Int", "3 : Int"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
}

func TestMainIsAnOrdinaryPromptName(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader("main = 42\nmain + 1\n:quit\n"), &out)
	if got := out.String(); strings.Contains(got, "BAD MAIN") || !strings.Contains(got, "43 : Int") {
		t.Fatalf("prompt main:\n%s", got)
	}
}

// The prompt runs an input under the default handlers of the effects it
// performs, as a program's entry runs `main`; a level takes precedence.
func TestPromptUsesDefaultHandlers(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader(`import Random
import Runtime.Entry
#[Runtime.Entry.DefaultHandler `+"`runGreeter`"+`]
effect Greeter
    greet : String -> String

runGreeter : (() ->{Greeter | e} a) ->{e} a
runGreeter action =
    handle action() on
        greet name -> resume ("Hello, " ++ name)

greet "prompt"
Random.int 3 3
saved = greet "value"
saved
use Random.runSeeded 1
Random.int 4 4
:quit
`), &out)
	got := out.String()
	for _, want := range []string{`"Hello, prompt" : String`, "3 : Int", `saved : String`, `"Hello, value" : String`, "4 : Int"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "UNHANDLED") || strings.Contains(got, "FAILURE") {
		t.Fatalf("default handlers were not applied:\n%s", got)
	}
}
