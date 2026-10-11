package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestScopedReaderEscapes(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"reader", `bad = Reader.withBytes Bytes.empty ({ r -> r })`},
		{"callback", `bad = Reader.withBytes Bytes.empty ({ r -> { _ -> Reader.readUpTo r 1 } })`},
		{"wrapper", `type Box a = Box a
bad = Reader.withBytes Bytes.empty ({ r -> Box ({ _ -> Reader.readUpTo r 1 }) })`},
		{"returned response body", `type Reply e = { render : () ->{e} Bytes.Bytes }
bad = Reader.withBytes Bytes.empty ({ r -> Reply { render = { Reader.readUpTo r 1 } } })`},
		{"outer scoped storage", `bad = Runtime.Local.run Nothing ({ slot ->
    Reader.withBytes Bytes.empty ({ r -> slot.write (Just r) }) })`},
		{"latent storage callback", `bad = Runtime.Local.run Nothing ({ slot ->
    save value = slot.write (Just value)
    Reader.withBytes Bytes.empty ({ r ->
        saved = save ({ _ -> Reader.readUpTo r 1 })
        identity x = x
        identity saved }) })`},
		{"nested local generalization", `bad = Runtime.Local.run Nothing ({ slot ->
    Reader.withBytes Bytes.empty ({ r ->
        remember value = slot.write (Just value)
        remember r
        identity x = x
        identity () }) })`},
		{"partial runner", `bad = Reader.withBytes Bytes.empty`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeModuleFile(t, t.TempDir(), "Main.fango",
				`import Bytes
import Reader
import Runtime.Local
`+tc.body+`
main() = ()
`)
			want := "SCOPE ESCAPE"
			if tc.name == "partial runner" {
				want = "SCOPED CALLBACK"
			}
			runErrorCase(t, path, want)
		})
	}
}

func TestScopedExportSurvivesCache(t *testing.T) {
	dir := t.TempDir()
	writeModuleFile(t, dir, "Cursor.fango", `module Cursor exposing (withText, withNumber, withTextFirst, withTextAndAction)
import Bytes
import Reader

{-# scoped s #-}
withText : String -> (Reader.Reader s ->{s} a) ->{e} a
withText text action = Reader.withBytes (Bytes.fromString text) action

{-# scoped s #-}
withNumber : (Int ->{s} a) ->{e} a
withNumber action = action 1

{-# scoped s #-}
withTextFirst : (Reader.Reader s ->{s} a) -> String ->{e} a
withTextFirst action text = Reader.withBytes (Bytes.fromString text) action

{-# scoped s #-}
withTextAndAction : String -> (Reader.Reader s ->{s} Int) -> (() ->{e} a) ->{e} (Int, a)
withTextAndAction text callback action =
    value = Reader.withBytes (Bytes.fromString text) callback
    (value, action())
`)
	path := writeModuleFile(t, dir, "Main.fango", `import Cursor
import Reader
import Bytes
main =
    first = Cursor.withTextFirst { reader -> Reader.readUpTo reader 1 } "abc"
    middle = Cursor.withTextAndAction "abc" { reader -> Bytes.length (Reader.readUpTo reader 2) } { 7 }
    last = Cursor.withText "abc" { reader -> Reader.readUpTo reader 1 }
    (first, middle, last)
`)
	buildVerbose(t, path, "-v")
	_, warm := buildVerbose(t, path, "-v")
	if !strings.Contains(warm, "Checking  Cursor (from cache)") {
		t.Fatalf("scoped wrapper not reused: %s", warm)
	}
	// A changed entry forces the deferred stage sections of cached scoped
	// wrappers to load and execute, preserving the same permission identities.
	writeModuleFile(t, dir, "Main.fango", `import Cursor
import Reader
import Bytes
import Meta
main = $(Meta.lift (Cursor.withNumber ({ number -> number + 2 })))
`)
	_, staged := buildVerbose(t, path, "-v")
	if !strings.Contains(staged, "Checking  Cursor (from cache)") {
		t.Fatalf("stage wrapper not reused: %s", staged)
	}
	// The cached scoped wrapper can now execute its Fango state handler during
	// stage evaluation.
	writeModuleFile(t, dir, "Main.fango", `import Cursor
import Reader
import Bytes
import Meta
main = $(Meta.lift (Cursor.withTextFirst { reader -> Bytes.length (Reader.readUpTo reader 1) } "abc"))
`)
	var stageOut, stageErr bytes.Buffer
	if code := run([]string{"check", "-v", path}, &stageOut, &stageErr); code != 0 || !strings.Contains(stageErr.String(), "Checking  Cursor (from cache)") {
		t.Fatalf("cached scoped stage evaluation failed: %s", stageErr.String())
	}
	writeModuleFile(t, dir, "Main.fango", `import Cursor
bad = Cursor.withTextFirst { reader -> reader } "abc"
main() = ()
`)
	var out, errs bytes.Buffer
	code := run([]string{"check", "-v", path}, &out, &errs)
	if code == 0 || !strings.Contains(errs.String(), "SCOPE ESCAPE") || !strings.Contains(errs.String(), "Checking  Cursor (from cache)") {
		t.Fatalf("cached contract lost: %s", errs.String())
	}
}

func TestNonFinalScopedReaderEscapes(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"returned reader", `bad = withTextFirst { reader -> reader } "abc"`},
		{"returned callback", `bad = withTextFirst { reader -> { Reader.readUpTo reader 1 } } "abc"`},
		{"later action storage", `bad = Runtime.Local.run Nothing { slot ->
    withTextAndAction { reader -> slot.write (Just reader) } { slot.read() }
}`},
		{"mock handler storage", `bad = Runtime.Local.run Nothing { slot ->
    Client.mock { request ->
        slot.write (Just request.body)
        Server.Response { status = 200, headers = [], body = Http.Empty }
    } { slot.read() }
}`},
		{"partial runner", `bad = withTextFirst { _ -> 1 }`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeModuleFile(t, t.TempDir(), "Main.fango", `import Bytes
import Reader
import Runtime.Local
import Http
import Http.Client as Client
import Http.Server as Server

{-# scoped s #-}
withTextFirst : (Reader.Reader s ->{s} a) -> String ->{e} a
withTextFirst callback text = Reader.withBytes (Bytes.fromString text) callback

{-# scoped s #-}
withTextAndAction : (Reader.Reader s ->{s} ()) -> (() ->{e} a) ->{e} a
withTextAndAction callback action =
    Reader.withBytes (Bytes.fromString "abc") callback
    action()

`+tc.body+"\nmain() = ()\n")
			want := "SCOPE ESCAPE"
			if tc.name == "partial runner" {
				want = "SCOPED CALLBACK"
			}
			runErrorCase(t, path, want)
		})
	}
}
