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
	RunWith(strings.NewReader(`import Runtime.Coroutine
import Runtime.Completion
import Meta
import Secret
unsafe : () -> Runtime.Coroutine.Step Int Int
unsafe() = Runtime.Coroutine.with (\_ input -> Secret.read input) (\work -> Runtime.Coroutine.advance work 7)
denied : String
denied = $(Meta.lift (show (Runtime.Completion.replay (Runtime.Completion.capture (\_ -> unsafe())))))
:type denied
constant : () -> Int
constant() = Runtime.Completion.replay (Runtime.Completion.capture (\_ -> 42))
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
	Run(strings.NewReader(`import Runtime.Coroutine
import Runtime.Completion
import Meta
exchange() = Runtime.Coroutine.with (\pause initial ->
    reply = pause (initial + 1)
    reply + 1) (\work ->
    first = Runtime.Coroutine.advance work 10
    second = Runtime.Coroutine.advance work 20
    third = Runtime.Coroutine.advance work 30
    (first, second, third))
exchange()
staged() = Runtime.Completion.replay (Runtime.Completion.capture (\_ -> exchange()))
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
	Run(strings.NewReader(`import Runtime.Coroutine
import Runtime.Completion
import Fail
import Runtime.Scope
closed() = Fail.attempt (\_ -> Runtime.Completion.replay (Runtime.Completion.capture (\_ ->
    Runtime.Coroutine.with (\pause () -> Runtime.Scope.finally (\_ ->
        pause "ready"
        7) (\_ -> Fail.fail "close failed")) (\work ->
        first = Runtime.Coroutine.advance work ()
        Runtime.Coroutine.close work
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
