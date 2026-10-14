package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResourceValuesMayOutliveScopes(t *testing.T) {
	const prelude = `import Random
import Runtime.Scope
import State
import Fail exposing (Fail, fail, attempt)

{-# resource #-}
type Port = Port Int

openPort() = Port 1
closePort port = ()
withPort action = Runtime.Scope.bracket openPort closePort action
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
main() = print (0)`, ""},
		{"state evidence closure", `effect Counter
    readCounter : () -> Int
keep : (() ->{e} Int) -> (() ->{e} Int)
keep action = action
leak =
    handle keep ({ _ -> readCounter() }) with state = 0 on
        readCounter () -> resume state with state
main() = print (0)`, ""},
		{"mutual resource escape", `first port stop = if stop then ({ _ -> readPort port }) else second port True
second port stop = first port stop
leak = withPort ({ port -> first port False })
main() = print (0)`, ""},
		{"mutual safe resource", `first port stop = if stop then readPort port else second port True
second port stop = first port stop
main() = print (withPort ({ port -> first port False }))`, ""},
		{"scalar helper", `loop port n = if n == 0 then readPort port else loop port (n - 1)
main() = print (withPort ({ port -> loop port 3 }))`, ""},
		{"recursive owner store", `empty : Maybe Port
empty = Nothing
loop : Int ->{State.State (Maybe Port)} ()
loop n = withPort ({ port ->
    if n == 0 then State.put (Just port) else
        ignored = State.run empty ({ _ -> loop (n - 1) })
        () })
main() = ignore (State.run empty ({ _ -> loop 2 }))`, ""},
		{"safe closure", `saved = withPort ({ port -> { _ -> 42 } })
main() = print (saved())`, ""},
		{"safe higher order closure", `saved = withPort ({ port -> identity ({ _ -> 42 }) })
main() = print (saved())`, ""},
		{"safe nested handler closure", `effect Counter
    readCounter : () -> Int
keep : (() ->{e} Int) -> (() ->{e} Int)
keep action = action
safe =
    handle keep { _ ->
        handle readCounter() with inner = 7 on
            readCounter () -> resume inner with inner
    } with outer = 0 on
        readCounter () -> resume outer with outer
main() = print (safe())`, ""},
		{"direct", `probe() = withPort identity`, ""},
		{"closure", `saved = withPort ({ port -> { _ -> readPort port } })
main() = print (saved())`, ""},
		{"ADT", `probe() = withPort ({ port -> Just port })`, ""},
		{"indirect", `probe() = withPort ({ port -> apply identity port })`, ""},
		{"nested polymorphic callback", `probe() = withPort ({ port ->
    apply ({ _ -> apply identity port }) 0 })`, ""},
		{"outer state Unit result", `probe() = State.run Nothing ({ _ ->
    withPort ({ port -> State.put (Just port) })
    () })`, ""},
		{"outer user handler", `probe() =
    handle withPort ({ port -> save (Just port) }) with saved = Nothing on
        save next -> resume () with next
        return _ -> ()`, ""},
		{"indirect store", `retain port = save (Just port)
result =
    handle withPort ({ port -> apply retain port }) with saved = Nothing on
        save next -> resume () with next
        return _ -> ()
main() = print result`, ""},
		{"dictionary store", `class SaveValue a
    saveValue : a ->{Store a} ()
instance SaveValue Port
    saveValue port = save port
throughDictionary : SaveValue a => a ->{Store a} ()
throughDictionary port = saveValue port
result =
    handle withPort ({ port -> throughDictionary port }) with saved = Nothing on
        save next -> resume () with Just next
        return _ -> ()
main() = print result`, ""},
		{"distinct nested owners", `probe() = withPort ({ outer ->
    withPort ({ inner -> readPort outer + readPort inner }) })`, ""},
		{"inner abort consumed", `probe() = withPort ({ port ->
    ignored = attempt ({ _ -> fail port })
    42 })`, ""},
		{"outer synchronous borrow", `probe() =
    handle withPort ({ port -> save port }) on
        save port ->
            n = readPort port
            resume ()`, ""},
		{"inner state", `probe() = withPort ({ port -> State.run (Just port) ({ _ -> () }) })`, ""},
		{"inner state consumed", `probe() = withPort ({ port ->
    ignored = State.run (Just port) ({ _ -> () })
    42 })`, ""},
		{"abort payload", `probe() = attempt ({ _ -> withPort ({ port -> fail port }) })`, ""},
		{"release retention", `probe() =
    handle Runtime.Scope.bracket openPort ({ port -> save port }) ({ _ -> () }) with saved = Nothing on
        save port -> resume () with Just port
        return _ -> ()`, ""},
		{"callback ADT", `type Box a = Box a
probe() = withPort ({ port ->
    box = Box identity
    case box of
        Box callback -> callback port })`, ""},
		{"partial wrapper", `later = withPort
main() = ignore (later ({ port -> port }))`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "Main.fango")
			body := tt.body
			if strings.Contains(body, "probe() =") {
				// The probe is the program; the entry only runs it.
				body += "\nmain() = ignore (probe())"
			}
			if err := os.WriteFile(path, []byte(prelude+body+"\n"), 0600); err != nil {
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

func TestResourceLibraryAllowsIndirectRetention(t *testing.T) {
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
hide value = Just ({ _ -> value })
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
	if _, _, ok := compileFile(path, &diagnostics); !ok {
		t.Fatalf("indirect library retention rejected: %s", diagnostics.String())
	}
}
