package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResourceCaptureContracts(t *testing.T) {
	const prelude = `import Random
import Scope
import State
import Fail exposing (Fail, fail, attempt)

{-# resource #-}
type Port = Port Int

openPort() = Port 1
closePort port = ()
withPort use = Scope.bracket openPort closePort use
readPort (Port n) = n
identity value = value
apply action arg = action arg

effect Store a
    save : a -> ()

`
	tests := []struct{ name, body, want string }{
		{"seeded closure", `leakingAction() =
    ignored = Random.int 1 2
    Random.int 1
leak = Random.runSeeded 0 leakingAction
main = 0`, ""},
		{"state evidence closure", `effect Counter
    readCounter : () -> Int
keep : (() ->{e} Int) -> (() ->{e} Int)
keep action = action
leak =
    handle keep (\_ -> readCounter()) with state = 0 of
        readCounter () -> resume state with state
main = 0`, "STATE RESULT ESCAPES"},
		{"mutual resource escape", `first port stop = if stop then (\_ -> readPort port) else second port True
second port stop = first port stop
leak = withPort (\port -> first port False)
main = 0`, "RESOURCE ESCAPES"},
		{"mutual safe resource", `first port stop = if stop then readPort port else second port True
second port stop = first port stop
main = withPort (\port -> first port False)`, ""},
		{"scalar helper", `loop port n = if n == 0 then readPort port else loop port (n - 1)
main = withPort (\port -> loop port 3)`, ""},
		{"recursive owner store", `empty : Maybe Port
empty = Nothing
loop : Int ->{State.State (Maybe Port)} ()
loop n = withPort (\port ->
    if n == 0 then State.put (Just port) else
        ignored = State.run empty (\_ -> loop (n - 1))
        ())
main = State.run empty (\_ -> loop 2)`, "RESOURCE ESCAPES"},
		{"safe closure", `saved = withPort (\port -> \_ -> 42)
main = saved()`, ""},
		{"safe higher order closure", `saved = withPort (\port -> identity (\_ -> 42))
main = saved()`, ""},
		{"safe nested handler closure", `effect Counter
    readCounter : () -> Int
keep : (() ->{e} Int) -> (() ->{e} Int)
keep action = action
safe =
    handle keep (\_ ->
        handle readCounter() with inner = 7 of
            readCounter () -> resume inner with inner) with outer = 0 of
        readCounter () -> resume outer with outer
main = safe()`, ""},
		{"direct", `main = withPort identity`, "RESOURCE ESCAPES"},
		{"closure", `saved = withPort (\port -> \_ -> readPort port)
main = saved()`, "RESOURCE ESCAPES"},
		{"ADT", `main = withPort (\port -> Just port)`, "RESOURCE ESCAPES"},
		{"indirect", `main = withPort (\port -> apply identity port)`, "RESOURCE ESCAPES"},
		{"nested polymorphic callback", `main = withPort (\port ->
    apply (\_ -> apply identity port) 0)`, "RESOURCE ESCAPES"},
		{"outer state Unit result", `main = State.run Nothing (\_ ->
    withPort (\port -> State.put (Just port))
    ())`, "RESOURCE ESCAPES"},
		{"outer user handler", `main =
    handle withPort (\port -> save (Just port)) with saved = Nothing of
        save next -> resume () with next
        return _ -> ()`, "RESOURCE ESCAPES"},
		{"indirect store", `retain port = save (Just port)
main =
    handle withPort (\port -> apply retain port) with saved = Nothing of
        save next -> resume () with next
        return _ -> ()`, "RESOURCE ESCAPES"},
		{"dictionary store", `class SaveValue a
    saveValue : a ->{Store a} ()
instance SaveValue Port
    saveValue port = save port
throughDictionary : SaveValue a => a ->{Store a} ()
throughDictionary port = saveValue port
main =
    handle withPort (\port -> throughDictionary port) with saved = Nothing of
        save next -> resume () with Just next
        return _ -> ()`, "RESOURCE ESCAPES"},
		{"distinct nested owners", `main = withPort (\outer ->
    withPort (\inner -> readPort outer + readPort inner))`, ""},
		{"inner abort consumed", `main = withPort (\port ->
    ignored = attempt (\_ -> fail port)
    42)`, ""},
		{"outer synchronous borrow", `main =
    handle withPort (\port -> save port) of
        save port ->
            n = readPort port
            resume ()`, ""},
		{"inner state", `main = withPort (\port -> State.run (Just port) (\_ -> ()))`, "RESOURCE ESCAPES"},
		{"inner state consumed", `main = withPort (\port ->
    ignored = State.run (Just port) (\_ -> ())
    42)`, ""},
		{"abort payload", `main = attempt (\_ -> withPort (\port -> fail port))`, "RESOURCE ESCAPES"},
		{"release retention", `main =
    handle Scope.bracket openPort (\port -> save port) (\_ -> ()) with saved = Nothing of
        save port -> resume () with Just port
        return _ -> ()`, "RESOURCE ESCAPES"},
		{"callback ADT", `type Box a = Box a
main = withPort (\port ->
    box = Box identity
    case box of
        Box callback -> callback port)`, "RESOURCE ESCAPES"},
		{"partial wrapper", `later = withPort
main = later (\port -> port)`, "RESOURCE ESCAPES"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "Main.fango")
			if err := os.WriteFile(path, []byte(prelude+tt.body+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			var diagnostics bytes.Buffer
			_, _, ok := compileFile(path, &diagnostics)
			if tt.want == "" {
				if !ok {
					t.Fatalf("compile: %s", diagnostics.String())
				}
			} else if ok || !strings.Contains(diagnostics.String(), tt.want) {
				t.Fatalf("want %s; compiled=%v: %s", tt.want, ok, diagnostics.String())
			}
		})
	}
}

func TestIndependentResourceLibrary(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "modules", "resources", "Main.fango")
	runDifferentialCase(t, path, cliRunner(path))
}

func TestResourceLibraryRejectsIndirectEscape(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"Connection.fango", "Connection.native.go", "Consumer.fango"} {
		content, err := os.ReadFile(filepath.Join("..", "..", "testdata", "modules", "resources", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	helper := `module Retention exposing (hide)
hide value = Just (\_ -> value)
`
	if err := os.WriteFile(filepath.Join(dir, "Retention.fango"), []byte(helper), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "Main.fango")
	body := `import Connection
import Retention
main() =
    leaked = Connection.withConnection 1 Retention.hide
    print "unreachable"
`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	if _, _, ok := compileFile(path, &diagnostics); ok || !strings.Contains(diagnostics.String(), "RESOURCE ESCAPES") {
		t.Fatalf("indirect library escape accepted: %s", diagnostics.String())
	}
}
