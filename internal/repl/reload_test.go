package repl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/compileevent"
	"github.com/waj/fango/internal/types"
)

func reloadSession(t *testing.T, cached bool) (*Session, *strings.Builder, func(string, string)) {
	t.Helper()
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var out strings.Builder
	opts := Options{Root: root, DisableCache: !cached}
	if cached {
		opts.Cache = newObjectCache()
	}
	s := NewSessionWith(&out, opts)
	t.Cleanup(s.Close)
	return s, &out, write
}

func TestReloadPreservesFunctions(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(map[bool]string{false: "uncached", true: "cached"}[cached], func(t *testing.T) {
			s, out, write := reloadSession(t, cached)
			write("Maths.fango", "module Maths exposing (adjust)\nadjust : Int -> Int\nadjust x = x + 1\n")
			s.submit("import Maths")
			s.submit("f x = Maths.adjust x")
			s.submit("cached = Maths.adjust 10")
			s.submit("cached")
			write("Maths.fango", "module Maths exposing (adjust)\nadjust : Int -> Int\nadjust x = x + 2\n")
			out.Reset()
			s.command(":reload")
			s.submit("Maths.adjust 10")
			s.submit("f 10")
			s.submit("cached")
			got := out.String()
			if !strings.Contains(got, "reloaded Maths") || !strings.Contains(got, "retained earlier module bindings: cached, f") || !strings.Contains(got, "12 : Int") || strings.Count(got, "11 : Int") != 2 {
				t.Fatalf("reload lost generations:\n%s", got)
			}
			out.Reset()
			s.command(":reload")
			if out.String() != "nothing to reload\n" {
				t.Fatalf("unchanged: %s", out.String())
			}
			s.submit("g x = Maths.adjust x")
			write("Maths.fango", "module Maths exposing (adjust)\nadjust : Int -> Int\nadjust x = x + 3\n")
			out.Reset()
			s.command(":reload")
			s.submit("Maths.adjust 10")
			s.submit("g 10")
			s.submit("f 10")
			for _, want := range []string{"13 : Int", "12 : Int", "11 : Int"} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("second reload missing %s:\n%s", want, out.String())
				}
			}
		})
	}
}

func TestReloadFailureAndRetry(t *testing.T) {
	s, out, write := reloadSession(t, true)
	write("A.fango", "module A exposing (value)\nvalue : Int\nvalue = 1\n")
	s.submit("import A exposing (value)")
	write("A.fango", "module A exposing (value)\nvalue : Int\nvalue = \"bad\"\n")
	out.Reset()
	s.command(":reload")
	s.submit("value")
	if !strings.Contains(out.String(), "TYPE MISMATCH") || !strings.Contains(out.String(), "1 : Int") {
		t.Fatal(out.String())
	}
	write("A.fango", "module A exposing (value)\nvalue : Int\nvalue = 2\n")
	out.Reset()
	s.command(":reload")
	s.submit("value")
	if !strings.Contains(out.String(), "2 : Int") {
		t.Fatal(out.String())
	}
}

func TestReloadRemovesExport(t *testing.T) {
	s, out, write := reloadSession(t, true)
	write("A.fango", "module A exposing (value)\nvalue : Int\nvalue = 1\n")
	s.submit("import A as X exposing (value)")
	s.submit("old () = value")
	write("A.fango", "module A exposing (next)\nnext : Int\nnext = 2\n")
	out.Reset()
	s.command(":reload")
	s.submit("old()")
	s.submit("X.next")
	s.submit("value")
	for _, want := range []string{"removed imported names: A.value", "1 : Int", "2 : Int", "NAMING ERROR"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %s:\n%s", want, out.String())
		}
	}
}

func TestReloadNominalIdentity(t *testing.T) {
	s, out, write := reloadSession(t, true)
	module := func(body string) {
		write("A.fango", "module A exposing (Box(..), make, read)\ntype Box = Box Int\nmake : Int -> Box\nmake x = Box x\nread : Box -> Int\nread (Box x) = "+body+"\n")
	}
	module("x")
	s.submit("import A")
	s.submit("old = A.make 3")
	s.submit("readOld x = A.read x")
	out.Reset()
	module("x + 1")
	s.command(":reload")
	s.submit("A.read old")
	s.submit("readOld old")
	for _, want := range []string{"4 : Int", "3 : Int"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("body edit changed identity (%s):\n%s", want, out.String())
		}
	}
	write("A.fango", "module A exposing (Box(..), make, read)\ntype Box = Box String\nmake : String -> Box\nmake x = Box x\nread : Box -> String\nread (Box x) = x\n")
	out.Reset()
	s.command(":reload")
	s.submit("A.read old")
	s.submit("readOld old")
	s.submit("A.read (A.make \"new\")")
	for _, want := range []string{"TYPE MISMATCH", "3 : Int", "\"new\" : String"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("type edit (%s):\n%s", want, out.String())
		}
	}
}

func TestReloadTransitiveAndNewImports(t *testing.T) {
	s, out, write := reloadSession(t, true)
	write("A.fango", "module A exposing (value)\nvalue : Int\nvalue = 1\n")
	write("B.fango", "module B exposing (read)\nimport A\nread : () -> Int\nread () = A.value\n")
	s.submit("import B")
	s.submit("old () = B.read()")
	write("A.fango", "module A exposing (value)\nvalue : Int\nvalue = 2\n")
	out.Reset()
	s.command(":reload")
	s.submit("B.read()")
	s.submit("old()")
	for _, want := range []string{"2 : Int", "1 : Int"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(out.String())
		}
	}
	write("C.fango", "module C exposing (value)\nvalue : Int\nvalue = 3\n")
	write("B.fango", "module B exposing (read)\nimport C\nread : () -> Int\nread () = C.value\n")
	out.Reset()
	s.command(":reload")
	s.submit("B.read()")
	s.submit("old()")
	for _, want := range []string{"3 : Int", "1 : Int"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(out.String())
		}
	}
	s.submit("import A")
	s.submit("A.value")
	if !strings.Contains(out.String(), "2 : Int") {
		t.Fatal(out.String())
	}
}

func TestReloadHandlersAndEffectfulValues(t *testing.T) {
	s, out, write := reloadSession(t, true)
	write("A.fango", "module A exposing (value)\nvalue : Int\nvalue = 1\n")
	s.submit("import A")
	s.submit("import State")
	s.submit("use State.run 7")
	s.submit("saved = State.get()")
	write("A.fango", "module A exposing (value)\nvalue : Int\nvalue = 2\n")
	out.Reset()
	s.command(":reload")
	s.submit("State.get()")
	if !strings.Contains(out.String(), "RELOAD WITH ACTIVE HANDLERS") || !strings.Contains(out.String(), "7 : Int") {
		t.Fatal(out.String())
	}
	s.command(":end")
	out.Reset()
	s.command(":reload")
	s.submit("saved")
	s.submit("A.value")
	for _, want := range []string{"7 : Int", "2 : Int"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(out.String())
		}
	}
}

func TestReloadStaging(t *testing.T) {
	s, out, write := reloadSession(t, true)
	write("A.fango", "module A exposing (value)\nvalue : Int\nvalue = 1\n")
	s.submit("import A")
	s.submit("old () = A.value")
	write("A.fango", "module A exposing (value)\nvalue : Int\nvalue = 2\n")
	out.Reset()
	s.command(":reload")
	s.submit("$(if A.value == 2 then `20` else `0`)")
	s.submit("$(if old() == 1 then `10` else `0`)")
	for _, want := range []string{"20 :", "10 :"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(out.String())
		}
	}
}

func TestReloadNativeWorker(t *testing.T) {
	s, out, write := reloadSession(t, true)
	write("Hash.fango", "module Hash exposing (twice, adjust)\ntwice : Int -> Int\ntwice = native\nadjust : Int -> Int\nadjust x = twice x + 1\n")
	write("Hash.native.go", "package native\nfunc Twice(x int64) int64 { return x * 2 }\n")
	s.submit("import Hash")
	s.submit("old x = Hash.adjust x")
	s.submit("saved = print \"once\"")
	s.submit("old 10")
	worker := s.exec
	write("Hash.fango", "module Hash exposing (twice, adjust)\ntwice : Int -> Int\ntwice = native\nadjust : Int -> Int\nadjust x = twice x + 2\n")
	out.Reset()
	s.command(":reload")
	s.submit("Hash.adjust 10")
	s.submit("old 10")
	s.submit("saved")
	for _, want := range []string{"22 : Int", "21 : Int", "() : ()"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(out.String())
		}
	}
	if s.exec != worker || strings.Contains(out.String(), "once") {
		t.Fatalf("worker/value replaced:\n%s", out.String())
	}
	write("Hash.native.go", "package native\nfunc Twice(x int64) int64 { return x * 3 }\n")
	out.Reset()
	s.command(":reload")
	s.submit("Hash.adjust 10")
	if !strings.Contains(out.String(), "RELOAD REQUIRES RESTART") || !strings.Contains(out.String(), "22 : Int") || s.exec != worker {
		t.Fatal(out.String())
	}
}

func TestReloadRetainsOldTypeInstances(t *testing.T) {
	s, out, write := reloadSession(t, true)
	write("A.fango", "module A exposing (Box(..))\ntype Box = Box Int deriving (Show, Eq)\n")
	s.submit("import A")
	s.submit("old = A.Box 1")
	write("A.fango", "module A exposing (Box(..))\ntype Box = Box String deriving (Show, Eq)\n")
	out.Reset()
	s.command(":reload")
	s.submit("old")
	s.submit("old == old")
	s.submit("A.Box \"new\"")
	for _, want := range []string{"Box 1 :", "True : Bool", "Box \"new\" :"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(out.String())
		}
	}
	write("A.fango", "module A exposing (Box(..))\ntype Box = Box String deriving (Show, Eq)\n-- another reload\n")
	out.Reset()
	s.command(":reload")
	s.submit("old == old")
	if !strings.Contains(out.String(), "True : Bool") || strings.Contains(out.String(), "reload failed") {
		t.Fatal(out.String())
	}
}

func TestReloadClassGeneration(t *testing.T) {
	s, out, write := reloadSession(t, true)
	write("A.fango", "module A exposing (Read(..))\nclass Read a\n    read : a -> Int\ninstance Read Int\n    read x = x + 1\n")
	s.submit("import A")
	s.submit("old x = A.read x")
	s.submit("n : Int\nn = 10")
	s.submit("old n")
	if !strings.Contains(out.String(), "11 :") {
		t.Fatal(out.String())
	}
	write("A.fango", "module A exposing (Read(..))\nclass Read a\n    read : a -> String\ninstance Read Int\n    read _ = \"new\"\n")
	out.Reset()
	s.command(":reload")
	s.submit("A.read n")
	s.submit("old n")
	for _, want := range []string{"reloaded A", "\"new\" :", "11 :"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(out.String())
		}
	}
}

func TestReloadImportCollisionRollback(t *testing.T) {
	s, out, write := reloadSession(t, true)
	write("A.fango", "module A exposing (value)\nvalue : Int\nvalue = 1\n")
	s.submit("import A exposing (..)")
	s.submit("mine = 10")
	write("A.fango", "module A exposing (value, mine)\nvalue : Int\nvalue = 2\nmine : Int\nmine = 20\n")
	out.Reset()
	s.command(":reload")
	s.submit("value")
	s.submit("mine")
	for _, want := range []string{"UNQUALIFIED COLLISION", "1 : Int", "10 : Int"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(out.String())
		}
	}
}

func TestReloadKeepsUnchangedGenerations(t *testing.T) {
	s, out, write := reloadSession(t, true)
	write("A.fango", "module A exposing (value)\nvalue : Int\nvalue = 1\n")
	write("B.fango", "module B exposing (value)\nvalue : Int\nvalue = 9\n")
	s.submit("import A\nimport B")
	before := s.runtimeNames["B.value"]
	write("A.fango", "module A exposing (value)\nvalue : Int\nvalue = 2\n")
	out.Reset()
	s.command(":reload")
	if !strings.Contains(out.String(), "reloaded A") || before != s.runtimeNames["B.value"] {
		t.Fatal(out.String())
	}
	s.submit("import List")
	out.Reset()
	s.submit("List.map { x -> A.value + x } [1, 2]")
	if !strings.Contains(out.String(), "[3, 4]") {
		t.Fatal(out.String())
	}
}

func TestReloadFixities(t *testing.T) {
	s, out, write := reloadSession(t, true)
	module := func(assoc string) {
		write("A.fango", "module A exposing ((<+>))\ninfix"+assoc+" 5 (<+>)\n(<+>) : Int -> Int -> Int\n(<+>) x y = x - y\n")
	}
	module("l")
	s.submit("import A exposing ((<+>))")
	s.submit("old() = 10 <+> 3 <+> 1")
	s.submit("(<*>) x y = x * y")
	s.submit("infixl 6 (<*>)")
	out.Reset()
	module("r")
	s.command(":reload")
	s.submit("10 <+> 3 <+> 1")
	s.submit("old()")
	s.submit("2 <*> 3 + 1")
	for _, want := range []string{"8 : Int", "6 : Int", "7 :"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(out.String())
		}
	}
}

func TestReloadCancellation(t *testing.T) {
	s, out, write := reloadSession(t, true)
	write("A.fango", "module A exposing (value)\nvalue : Int\nvalue = 1\n")
	s.submit("import A")
	write("A.fango", "module A exposing (value)\nvalue : Int\nvalue = 2\n")
	s.options.Observe = func(e compileevent.Event) {
		if e.Owner == "A" {
			s.cancelCurrent()
		}
	}
	out.Reset()
	s.command(":reload")
	s.submit("A.value")
	for _, want := range []string{"RELOAD CANCELLED", "1 : Int"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(out.String())
		}
	}
	s.options.Observe = nil
	out.Reset()
	s.command(":reload")
	s.submit("A.value")
	if !strings.Contains(out.String(), "2 : Int") {
		t.Fatal(out.String())
	}
}

func TestReloadStagingFailureRollback(t *testing.T) {
	s, out, write := reloadSession(t, true)
	write("A.fango", "module A exposing (value)\nvalue : Int\nvalue = 1\n")
	s.submit("import A")
	s.submit("import Meta")
	s.submit("answer = `A.value`")
	write("A.fango", "module A exposing (value)\nimport Meta\nvalue : Int\nvalue = $(Meta.fail \"reload stage failed\")\n")
	out.Reset()
	s.command(":reload")
	s.submit("$(answer)")
	for _, want := range []string{"reload stage failed", "1 : Int"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(out.String())
		}
	}
}

func TestReloadPreservesPromptDeclarations(t *testing.T) {
	s, out, write := reloadSession(t, true)
	write("A.fango", "module A exposing (value)\nvalue : Int\nvalue = 1\n")
	s.submit("import A")
	s.submit("type Local = Local Int deriving (Eq, Show)")
	s.submit("class Describe a\n    describe : a -> String")
	s.submit("instance Describe Local\n    describe _ = \"local\"")
	s.submit("value = Local 3")
	write("A.fango", "module A exposing (value)\nvalue : Int\nvalue = 2\n")
	out.Reset()
	s.command(":reload")
	s.submit("value")
	s.submit("describe value")
	s.submit("value == Local 3")
	for _, want := range []string{"Local 3 : Local", "\"local\" : String", "True : Bool"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(out.String())
		}
	}
	if strings.Contains(out.String(), "before reload") {
		t.Fatal(out.String())
	}
}

func TestReloadEffectGeneration(t *testing.T) {
	s, out, write := reloadSession(t, true)
	module := func(typ, value string) {
		write("A.fango", "module A exposing (Ping(..), run, read)\neffect Ping\n    ping : () -> "+typ+"\nrun : (() ->{Ping} "+typ+") -> "+typ+"\nrun action =\n    handle action() on\n        ping () -> resume "+value+"\nread : () ->{Ping} "+typ+"\nread () = ping()\n")
	}
	module("Int", "1")
	s.submit("import A")
	s.submit("oldRead() = A.read()")
	s.submit("oldRun action = A.run action")
	module("String", "\"new\"")
	out.Reset()
	s.command(":reload")
	s.submit("A.run { A.read() }")
	s.submit("oldRun oldRead")
	for _, want := range []string{"reloaded A", "\"new\" : String", "1 : Int"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(out.String())
		}
	}
}

func TestReloadReflectedTypes(t *testing.T) {
	s, out, write := reloadSession(t, true)
	module := func(ctor string) {
		write("A.fango", "module A exposing (Box(..), reflected)\nimport Meta\ntype Box = "+ctor+"\nreflected = typeOf Box\n")
	}
	module("Old")
	s.submit("import A")
	s.submit("import Meta")
	s.submit("name repr = case Meta.info repr of\n    Meta.Visible info -> Meta.lift info.name\n    Meta.Opaque -> Meta.lift \"opaque\"")
	module("New")
	out.Reset()
	s.command(":reload")
	s.submit("$(name A.reflected)")
	if !strings.Contains(out.String(), "\"Box\" : String") {
		t.Fatal(out.String())
	}
}

func TestReloadRetainedAttributeCode(t *testing.T) {
	s, out, write := reloadSession(t, true)
	module := func(value string) {
		write("A.fango", "module A exposing (Box(..), Default(..), reflected, readDefault)\nimport Meta\ntype Default = Default Meta.Code\n#[Default `"+value+"`] type Box = Box\nreflected = typeOf Box\nreadDefault repr =\n    case Meta.info repr of\n        Meta.Visible info ->\n            case Meta.attributes @Default info.attributes of\n                Meta.Item attached _ ->\n                    case attached.value of\n                        Default code -> code\n                Meta.NoItems -> Meta.fail \"missing\"\n        Meta.Opaque -> Meta.fail \"opaque\"\n")
	}
	module("11")
	s.submit("import A")
	s.submit("old = A.reflected")
	s.submit("readOld repr = A.readDefault repr")
	module("22")
	out.Reset()
	s.command(":reload")
	s.submit("$(A.readDefault A.reflected)")
	s.submit("$(readOld old)")
	for _, want := range []string{"22 :", "11 :"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(out.String())
		}
	}
	identity := s.ck.TypeNames["A.Box"].(*types.TCon).Unique
	write("B.fango", "module B exposing (value)\nvalue : Int\nvalue = 1\n")
	s.submit("import B")
	write("B.fango", "module B exposing (value)\nvalue : Int\nvalue = 2\n")
	out.Reset()
	s.command(":reload")
	s.submit("$(A.readDefault A.reflected)")
	if !strings.Contains(out.String(), "22 :") || identity != s.ck.TypeNames["A.Box"].(*types.TCon).Unique {
		t.Fatalf("unchanged staged contract acquired a new identity:\n%s", out.String())
	}
}

func TestReloadCachedRecursiveType(t *testing.T) {
	s, out, write := reloadSession(t, true)
	write("A.fango", "module A exposing (Tree(..), size)\ntype Tree = Leaf Int | Fork Tree Tree\nsize : Tree -> Int\nsize tree = case tree of\n    Leaf _ -> 1\n    Fork a b -> size a + size b\n")
	write("B.fango", "module B exposing (value)\nimport A\nvalue : Int\nvalue = 1\n")
	s.submit("import A\nimport B")
	s.submit("old = A.Fork (A.Leaf 1) (A.Leaf 2)")
	before := s.ck.TypeNames["A.Tree"].(*types.TCon).Unique
	write("B.fango", "module B exposing (value)\nimport A\nvalue : Int\nvalue = 2\n")
	out.Reset()
	s.command(":reload")
	s.submit("A.size old")
	if !strings.Contains(out.String(), "2 : Int") || before != s.ck.TypeNames["A.Tree"].(*types.TCon).Unique {
		t.Fatal(out.String())
	}
}

func TestImportRejectsWorkerReplacementInLevel(t *testing.T) {
	s, out, write := reloadSession(t, true)
	write("Hash.fango", "module Hash exposing (twice)\ntype Hidden = Hidden Int deriving (Show)\ntwice : Int -> Int\ntwice = native\n")
	write("Hash.native.go", "package native\nfunc Twice(x int64) int64 { return x * 2 }\n")
	s.submit("import State")
	s.submit("use State.run 9")
	out.Reset()
	s.submit("import Hash")
	s.submit("State.get()")
	if !strings.Contains(out.String(), "NATIVE WORKER IN USE") || !strings.Contains(out.String(), "9 : Int") {
		t.Fatal(out.String())
	}
	s.command(":end")
	out.Reset()
	s.submit("import Hash")
	s.submit("Hash.twice 3")
	if !strings.Contains(out.String(), "6 : Int") {
		t.Fatal(out.String())
	}
}

func TestReloadWithScopedLibrary(t *testing.T) {
	s, out, write := reloadSession(t, true)
	write("A.fango", "module A exposing (value)\nvalue : Int\nvalue = 1\n")
	s.submit("import A\nimport Reader\nimport Bytes")
	write("A.fango", "module A exposing (value)\nvalue : Int\nvalue = 2\n")
	out.Reset()
	s.command(":reload")
	s.submit("use reader <- Reader.withBytes (Bytes.fromString \"hello\")")
	s.submit("Bytes.toStringLossy (Reader.readUpTo reader 5)")
	s.command(":end")
	if !strings.Contains(out.String(), "\"hello\" : String") || strings.Contains(out.String(), "reload failed") {
		t.Fatal(out.String())
	}
}

func TestReloadReportsPromptInstanceDependencies(t *testing.T) {
	s, out, write := reloadSession(t, true)
	write("A.fango", "module A exposing (value)\nvalue : Int\nvalue = 1\n")
	s.submit("import A")
	s.submit("class C a\n    c : a -> Int")
	s.submit("instance C Int\n    c _ = A.value")
	s.submit("old : Int -> Int\nold x = c x")
	write("A.fango", "module A exposing (value)\nvalue : Int\nvalue = 2\n")
	out.Reset()
	s.command(":reload")
	s.submit("old 10")
	if !strings.Contains(out.String(), "retained earlier module bindings: old") || !strings.Contains(out.String(), "1 : Int") {
		t.Fatal(out.String())
	}
}

// nominalIdentities names every declared type and effect of a session.
func nominalIdentities(s *Session) map[string]int {
	ids := map[string]int{}
	for name, ty := range s.ck.TypeNames {
		if con, ok := ty.(*types.TCon); ok {
			ids["type "+name] = con.Unique
		}
	}
	for name, effect := range s.ck.Effects {
		ids["effect "+name] = effect.Unique
	}
	return ids
}

func expectIdentitiesRetained(t *testing.T, s *Session, before map[string]int) {
	t.Helper()
	for name, id := range before {
		if now := nominalIdentities(s)[name]; now != id {
			t.Errorf("%s changed identity across reload: %d -> %d", name, id, now)
		}
	}
}

// sessionsOver starts sessions over one root and one object cache, each with
// the given imports already entered.
func sessionsOver(t *testing.T, start map[string]string, importing string) (func() (*Session, *strings.Builder), func(string, string)) {
	t.Helper()
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range start {
		write(name, body)
	}
	opts := Options{Root: root, Cache: newObjectCache()}
	return func() (*Session, *strings.Builder) {
		var out strings.Builder
		s := NewSessionWith(&out, opts)
		t.Cleanup(s.Close)
		s.submit(importing)
		return s, &out
	}, write
}

// sessionsFromCache is sessionsOver after a first session filled the cache, so
// every declaration a later session installs is decoded from an object and
// numbered in decoding order.
func sessionsFromCache(t *testing.T, start map[string]string, importing string) (func() (*Session, *strings.Builder), func(string, string)) {
	t.Helper()
	session, write := sessionsOver(t, start, importing)
	session()
	return session, write
}

func TestReloadFromCachedObjects(t *testing.T) {
	for name, module := range map[string]string{
		"values":  "module Lib exposing (x)\n\nx = %s\n",
		"effects": "module Lib exposing (x, Log(..))\n\neffect Log\n    info : String -> ()\n\nx = %s\n",
	} {
		t.Run(name, func(t *testing.T) {
			lib := func(value string) string { return strings.Replace(module, "%s", value, 1) }
			session, write := sessionsFromCache(t, map[string]string{"Lib.fango": lib("1")}, "import Lib")
			// Every later session has to reload as well; the first one to fail
			// used to leave nothing different behind for the next.
			for _, value := range []string{"2", "3"} {
				s, out := session()
				before := nominalIdentities(s)
				write("Lib.fango", lib(value))
				out.Reset()
				s.command(":reload")
				s.submit("Lib.x")
				if want := "reloaded Lib\n" + value + " : Num a => a\n"; out.String() != want {
					t.Fatalf("got:\n%s\nwant:\n%s", out.String(), want)
				}
				expectIdentitiesRetained(t, s, before)
			}
		})
	}
}

// Library types and effects survive an unrelated reload, whether the session
// checked them from source or decoded them. The reload itself decodes them in
// both, so a contract must not depend on how its declaration was built: a
// retired Console would strand any retained binding that performs it.
func TestReloadKeepsLibraryIdentities(t *testing.T) {
	const imports = "import Lib\nimport Console\nimport File\nimport Bytes"
	lib := func(value string) string { return "module Lib exposing (x)\n\nx = " + value + "\n" }
	for name, sessions := range map[string]func(*testing.T, map[string]string, string) (func() (*Session, *strings.Builder), func(string, string)){
		"from source":  sessionsOver,
		"from objects": sessionsFromCache,
	} {
		t.Run(name, func(t *testing.T) {
			session, write := sessions(t, map[string]string{"Lib.fango": lib("0")}, imports)
			s, out := session()
			s.submit("greet () = Console.write \"hi\"")
			before := nominalIdentities(s)
			write("Lib.fango", lib("1"))
			out.Reset()
			s.command(":reload")
			s.command(":type greet")
			s.submit("Console.runTest Bytes.empty greet")
			for _, want := range []string{"reloaded Lib", "() ->{Console} ()\n", "Transcript ()"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("missing %q in:\n%s", want, out.String())
				}
			}
			if strings.Contains(out.String(), "before reload") {
				t.Errorf("a library effect was retired:\n%s", out.String())
			}
			expectIdentitiesRetained(t, s, before)
		})
	}
}

// A native's contract is its nominal identities as well as its printed
// signature. Each edit leaves the sidecar's bytes alone, so only the contract
// can refuse it.
func TestReloadFromCachedObjectsChecksNatives(t *testing.T) {
	const sidecar = "package native\nfunc Twice(x int64) int64 { return x * 2 }\nfunc Mint(x int64) int64 { return x }\n"
	module := func(constructor, twice, value string) string {
		return "module Hash exposing (Token, mint, twice, value)\ntype Token = " + constructor + " Int\nmint : Int -> Token\nmint = native\ntwice : " + twice + "\ntwice = native\nvalue = " + value + "\n"
	}
	for _, test := range []struct {
		name, module string
		restart      bool
	}{
		{"value edit", module("Token", "Int -> Int", "2"), false},
		{"parameter type", module("Token", "Token -> Int", "1"), true},
		{"effect row", module("Token", "Int ->{IO} Int", "1"), true},
		{"same printed signature", module("Wrapped", "Int -> Int", "1"), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := map[string]string{"Hash.fango": module("Token", "Int -> Int", "1"), "Hash.native.go": sidecar}
			session, write := sessionsFromCache(t, files, "import Hash")
			s, out := session()
			worker := s.exec
			write("Hash.fango", test.module)
			out.Reset()
			s.command(":reload")
			if refused := strings.Contains(out.String(), "A native interface changed"); refused != test.restart {
				t.Fatalf("restart required = %v, want %v:\n%s", refused, test.restart, out.String())
			}
			if !test.restart && !strings.Contains(out.String(), "reloaded Hash") {
				t.Fatal(out.String())
			}
			s.submit("Hash.twice 10")
			if !strings.Contains(out.String(), "20 : Int") || s.exec != worker {
				t.Fatalf("worker or session replaced:\n%s", out.String())
			}
		})
	}
}
