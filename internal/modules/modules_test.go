package modules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, body string) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDependencyOrderAndManifest(t *testing.T) {
	d := t.TempDir()
	entry := write(t, d, "Main.fango", "module Main exposing (main)\nimport Z\nimport A\nmain = A.a + Z.z\n")
	write(t, d, "A.fango", "module A exposing (a)\nimport B\na = B.b\n")
	write(t, d, "B.fango", "module B exposing (b)\nb = 20\n")
	write(t, d, "Z.fango", "module Z exposing (z)\nz = 22\n")
	r, errs := Load(entry)
	if len(errs) > 0 {
		t.Fatalf("Load: %v", errs)
	}
	var got []string
	for _, m := range r.Manifest {
		got = append(got, m.Module)
	}
	if strings.Join(got, ",") != "B,A,Z,Main" {
		t.Fatalf("order %v", got)
	}
	if r.Entry != "Main.main" {
		t.Fatalf("entry %q", r.Entry)
	}
	if len(r.Module.Decls) != 4 {
		t.Fatalf("merged %d declarations", len(r.Module.Decls))
	}
	var units []string
	for _, unit := range r.Units {
		units = append(units, unit.Name+":"+strings.Join(unit.Imports, "+"))
	}
	if strings.Join(units, ",") != "B:,A:B,Z:,Main:Z+A" {
		t.Fatalf("units %v", units)
	}
	if !r.Units[len(r.Units)-1].Entry {
		t.Fatal("last dependency-first unit is not the entry")
	}
}

func TestGraphDiagnostics(t *testing.T) {
	t.Run("cycle", func(t *testing.T) {
		d := t.TempDir()
		entry := write(t, d, "Main.fango", "module Main exposing (main)\nimport A\nmain = 0\n")
		write(t, d, "A.fango", "module A exposing (a)\nimport B\na = 1\n")
		write(t, d, "B.fango", "module B exposing (b)\nimport A\nb = 2\n")
		_, errs := Load(entry)
		if len(errs) == 0 || !strings.Contains(errs[0].Body, "A -> B -> A") {
			t.Fatalf("cycle errors: %#v", errs)
		}
	})
	t.Run("missing", func(t *testing.T) {
		d := t.TempDir()
		entry := write(t, d, "Main.fango", "module Main exposing (main)\nimport Missing.Module\nmain = 0\n")
		_, errs := Load(entry)
		if len(errs) == 0 || errs[0].Title != "MISSING MODULE" {
			t.Fatalf("errors: %#v", errs)
		}
	})
	t.Run("private", func(t *testing.T) {
		d := t.TempDir()
		entry := write(t, d, "Main.fango", "module Main exposing (main)\nimport Secret\nmain = Secret.hidden\n")
		write(t, d, "Secret.fango", "module Secret exposing (visible)\nvisible = 1\nhidden = 2\n")
		_, errs := Load(entry)
		if len(errs) == 0 || errs[0].Title != "PRIVATE OR UNKNOWN NAME" {
			t.Fatalf("errors: %#v", errs)
		}
	})
}

func TestBundledModules(t *testing.T) {
	d := t.TempDir()
	entry := write(t, d, "Main.fango", "module Main exposing (main)\nimport IO\nimport List\nmain = 0\n")
	r, errs := Load(entry)
	if len(errs) > 0 {
		t.Fatalf("Load: %v", errs)
	}
	var got []string
	for _, m := range r.Manifest {
		got = append(got, m.Module+":"+m.Path)
	}
	want := "IO:<stdlib>/IO.fango,List:<stdlib>/List.fango,Main:Main.fango"
	if strings.Join(got, ",") != want {
		t.Fatalf("manifest = %v, want %s", got, want)
	}
	if strings.Join(r.NativeOperations, ",") != "IO.write" {
		t.Fatalf("native operations = %v", r.NativeOperations)
	}
}

func TestBundledModuleNamesAreReserved(t *testing.T) {
	t.Run("import", func(t *testing.T) {
		d := t.TempDir()
		entry := write(t, d, "Main.fango", "module Main exposing (main)\nimport List\nmain = 0\n")
		write(t, d, "List.fango", "module List exposing (answer)\nanswer = 42\n")
		_, errs := Load(entry)
		if len(errs) == 0 || errs[0].Title != "RESERVED MODULE" {
			t.Fatalf("errors: %#v", errs)
		}
	})
	t.Run("entry", func(t *testing.T) {
		d := t.TempDir()
		entry := write(t, d, "List.fango", "module List exposing (main)\nmain = 0\n")
		_, errs := Load(entry)
		if len(errs) == 0 || errs[0].Title != "RESERVED MODULE" {
			t.Fatalf("errors: %#v", errs)
		}
	})
}
