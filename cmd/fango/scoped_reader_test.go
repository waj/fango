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
	writeModuleFile(t, dir, "Cursor.fango", `module Cursor exposing (withText, withNumber)
import Bytes
import Reader

{-# scoped s #-}
withText : String -> (Reader.Reader s ->{s} a) ->{e} a
withText text use = Reader.withBytes (Bytes.fromString text) use

{-# scoped s #-}
withNumber : (Int ->{s} a) ->{e} a
withNumber use = use 1
`)
	path := writeModuleFile(t, dir, "Main.fango", `import Cursor
import Reader
main = Cursor.withText "abc" ({ reader -> Reader.readUpTo reader 1 })
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
main = $(Meta.lift (Cursor.withText "abc" ({ reader -> Bytes.length (Reader.readUpTo reader 1) })))
`)
	var stageOut, stageErr bytes.Buffer
	if code := run([]string{"check", "-v", path}, &stageOut, &stageErr); code != 0 || !strings.Contains(stageErr.String(), "Checking  Cursor (from cache)") {
		t.Fatalf("cached scoped stage evaluation failed: %s", stageErr.String())
	}
	writeModuleFile(t, dir, "Main.fango", `import Cursor
bad = Cursor.withText "abc" ({ reader -> reader })
main() = ()
`)
	var out, errs bytes.Buffer
	code := run([]string{"check", "-v", path}, &out, &errs)
	if code == 0 || !strings.Contains(errs.String(), "SCOPE ESCAPE") || !strings.Contains(errs.String(), "Checking  Cursor (from cache)") {
		t.Fatalf("cached contract lost: %s", errs.String())
	}
}
