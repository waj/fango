package modules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/source"
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

func TestNativeTemplateValidation(t *testing.T) {
	for _, tt := range []struct {
		name, template, title string
		arity                 int
	}{
		{"valid", "$1 + $2", "", 2},
		{"missing", "$1 + 1", "NATIVE TEMPLATE PLACEHOLDER", 2},
		{"duplicate", "$1 + $1", "NATIVE TEMPLATE PLACEHOLDER", 1},
		{"syntax", "$1 +", "INVALID NATIVE TEMPLATE", 1},
		{"identifier", "helper($1)", "NATIVE TEMPLATE IDENTIFIER", 1},
		{"intrinsic", "$eq($1)", "NATIVE TEMPLATE INTRINSIC", 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateTemplate(tt.template, tt.arity, source.Span{})
			if tt.title == "" {
				if len(errs) > 0 {
					t.Fatalf("errors: %#v", errs)
				}
				return
			}
			found := false
			for _, err := range errs {
				found = found || err.Title == tt.title
			}
			if !found {
				t.Fatalf("errors %#v do not include %s", errs, tt.title)
			}
		})
	}
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
	if strings.Join(got, ",") != "Basics,Basics,IO,IO,B,A,Z,Main" {
		t.Fatalf("order %v", got)
	}
	if r.Entry != "Main.main" {
		t.Fatalf("entry %q", r.Entry)
	}
	if len(r.Module.Decls) <= 4 {
		t.Fatalf("merged program did not include the declared prelude: %d declarations", len(r.Module.Decls))
	}
	var units []string
	for _, unit := range r.Units {
		units = append(units, unit.Name+":"+strings.Join(unit.Imports, "+"))
	}
	if strings.Join(units, ",") != "Basics:,IO:,B:,A:B,Z:,Main:Z+A" {
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
	want := "Basics:<stdlib>/Basics.fango,Basics:<stdlib>/Basics.native.go,IO:<stdlib>/IO.fango,IO:<stdlib>/IO.native.go,List:<stdlib>/List.fango,Main:Main.fango"
	if strings.Join(got, ",") != want {
		t.Fatalf("manifest = %v, want %s", got, want)
	}
	if r.Operators["+"] != "Basics.add" || len(r.Natives) != 2 {
		t.Fatalf("declared native metadata: operators=%v natives=%v", r.Operators, r.Natives)
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

func TestNativeSidecarValidation(t *testing.T) {
	t.Run("valid scalar ABI", func(t *testing.T) {
		d := t.TempDir()
		entry := write(t, d, "Main.fango", "module Main exposing (main)\nimport Hash\nmain = Hash.twice 21\n")
		write(t, d, "Hash.fango", "module Hash exposing (twice)\ntwice : Int -> Int\ntwice = native\n")
		write(t, d, "Hash.native.go", "package native\nfunc Twice(x int64) int64 { return x * 2 }\n")
		r, errs := Load(entry)
		if len(errs) > 0 {
			t.Fatalf("Load: %v", errs)
		}
		if len(r.Natives) != 3 || r.Natives[2].Module != "Hash" {
			t.Fatalf("native sources: %#v", r.Natives)
		}
	})

	tests := []struct{ name, source, sidecar, title string }{
		{"missing sidecar", "value : Int -> Int\nvalue = native\n", "", "MISSING NATIVE SIDECAR"},
		{"package", "value : Int -> Int\nvalue = native\n", "package wrong\nfunc Value(x int64) int64 { return x }\n", "NATIVE PACKAGE NAME"},
		{"external import", "value : Int -> Int\nvalue = native\n", "package native\nimport _ \"example.com/nope\"\nfunc Value(x int64) int64 { return x }\n", "NATIVE IMPORT NOT ALLOWED"},
		{"shape", "value : Int -> Int\nvalue = native\n", "package native\nfunc Value(x string) int64 { return 0 }\n", "NATIVE ABI"},
		{"template", "value : Int -> Int\nvalue = native \"$1\"\n", "", "NATIVE TEMPLATE NOT ALLOWED"},
		{"infix", "value = 1\ninfix (+) = value\n", "", "INFIX NOT ALLOWED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := t.TempDir()
			entry := write(t, d, "Main.fango", "module Main exposing (main)\n"+tt.source+"main = 0\n")
			if tt.sidecar != "" {
				write(t, d, "Main.native.go", tt.sidecar)
			}
			_, errs := Load(entry)
			if len(errs) == 0 || errs[0].Title != tt.title {
				t.Fatalf("errors: %#v", errs)
			}
		})
	}
}
