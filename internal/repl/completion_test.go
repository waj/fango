package repl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCoroutineCompletionPreservesStagingNativePolicy(t *testing.T) {
	root := t.TempDir()
	for name, source := range map[string]string{
		"Secret.fango":     "module Secret exposing (read)\nread : Int -> Int\nread = native\n",
		"Secret.native.go": "package native\nfunc Read(value int64) int64 { return value + 1 }\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0644); err != nil {
			t.Fatal(err)
		}
	}
	var out strings.Builder
	RunWith(strings.NewReader(`import Coroutine
import Completion
import Meta
import Secret
unsafe : () -> Coroutine.Step Int Int
unsafe() = Coroutine.with (\_ input -> Secret.read input) (\work -> Coroutine.advance work 7)
denied : String
denied = $(Meta.lift (show (Completion.replay (Completion.capture (\_ -> unsafe())))))
:type denied
constant : () -> Int
constant() = Completion.replay (Completion.capture (\_ -> 42))
answer : Int
answer = $(Meta.lift (constant()))
answer
:quit
`), &out, Options{Root: root})
	got := out.String()
	if strings.Contains(got, "INTERNAL") || !strings.Contains(got, "Secret.read") || !strings.Contains(got, "I don't know a value named `denied`.") || !strings.Contains(got, "42 : Int") {
		t.Fatalf("completion bypassed staging policy or broke rollback:\n%s", got)
	}
}

func TestCoroutineCompletionREPLStagingAndRollback(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader(`import Coroutine
import Completion
import Meta
exchange() = Coroutine.with (\pause initial ->
    reply = pause (initial + 1)
    reply + 1) (\work ->
    first = Coroutine.advance work 10
    second = Coroutine.advance work 20
    third = Coroutine.advance work 30
    (first, second, third))
exchange()
staged() = Completion.replay (Completion.capture (\_ -> exchange()))
bad : Int
bad = $(Meta.lift (show (staged())))
:type bad
answer : String
answer = $(Meta.lift (show (staged())))
answer
staged()
:quit
`), &out)
	got := out.String()
	if strings.Contains(got, "INTERNAL") || strings.Contains(got, "runtime error") || !strings.Contains(got, "I don't know a value named `bad`.") || strings.Count(got, "Suspended 11, Finished 21, Closed") < 3 {
		t.Fatalf("coroutine completion staging/rollback failed:\n%s", got)
	}
}

func TestCoroutineCompletionCloseFailureInREPL(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader(`import Coroutine
import Completion
import Fail
import Scope
closed() = Fail.attempt (\_ -> Completion.replay (Completion.capture (\_ ->
    Coroutine.with (\pause () -> Scope.finally (\_ ->
        pause "ready"
        7) (\_ -> Fail.fail "close failed")) (\work ->
        first = Coroutine.advance work ()
        Coroutine.close work
        first))))
closed()
closed()
:quit
`), &out)
	got := out.String()
	if strings.Contains(got, "INTERNAL") || strings.Count(got, "Err close failed :") != 2 {
		t.Fatalf("REPL close completion failed:\n%s", got)
	}
}
