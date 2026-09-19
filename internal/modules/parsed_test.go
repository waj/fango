package modules

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/source"
)

type memoryParsedCache map[string][]byte

func (c memoryParsedCache) LoadParsed(hash string) ([]byte, bool) {
	b, ok := c[hash]
	return append([]byte(nil), b...), ok
}

func (c memoryParsedCache) StoreParsed(hash string, data []byte) {
	c[hash] = append([]byte(nil), data...)
}

func TestParsedCacheSkipsParsingAndReturnsFreshTrees(t *testing.T) {
	d := t.TempDir()
	entry := write(t, d, "Main.fango", "main = 1 + 2 * 3\n")
	cache := memoryParsedCache{}
	var first, second int
	r1, errs := LoadWithOptions(entry, LoadOptions{Parsed: cache, Observe: func(stage, _ string) {
		if stage == "parse" {
			first++
		}
	}})
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	r2, errs := LoadWithOptions(entry, LoadOptions{Parsed: cache, Observe: func(stage, _ string) {
		if stage == "parse" {
			second++
		}
	}})
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if first == 0 || second != 0 {
		t.Fatalf("parse events first=%d second=%d", first, second)
	}
	if ast.Dump(r1.Module) != ast.Dump(r2.Module) {
		t.Fatalf("cold/cached AST differ:\n%s\n%s", ast.Dump(r1.Module), ast.Dump(r2.Module))
	}
	if &r1.Module.Decls[0] == &r2.Module.Decls[0] || r1.Module.Decls[0] == r2.Module.Decls[0] {
		t.Fatal("cached load reused a mutable AST declaration")
	}
}

func TestDependencyEditReusesImporterParsedUnit(t *testing.T) {
	d := t.TempDir()
	entry := write(t, d, "Main.fango", "module Main exposing (main)\nimport Helper exposing (value)\nmain = value\n")
	helper := write(t, d, "Helper.fango", "module Helper exposing (value)\nvalue = 1\n")
	cache := memoryParsedCache{}
	if _, errs := LoadWithOptions(entry, LoadOptions{Parsed: cache}); len(errs) > 0 {
		t.Fatal(errs)
	}
	if err := os.WriteFile(helper, []byte("module Helper exposing (value)\nvalue = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var parsed []string
	if _, errs := LoadWithOptions(entry, LoadOptions{Parsed: cache, Observe: func(stage, owner string) {
		if stage == "parse" {
			parsed = append(parsed, owner)
		}
	}}); len(errs) > 0 {
		t.Fatal(errs)
	}
	if !reflect.DeepEqual(parsed, []string{"Helper"}) {
		t.Fatalf("parsed after dependency edit = %v", parsed)
	}
}

func TestSuccessfulParsePersistsBeforeGraphFailure(t *testing.T) {
	d := t.TempDir()
	entry := write(t, d, "Main.fango", "module Main exposing (main)\nimport Missing\nmain = 1\n")
	cache := memoryParsedCache{}
	if _, errs := LoadWithOptions(entry, LoadOptions{Parsed: cache}); len(errs) == 0 {
		t.Fatal("missing dependency accepted")
	}
	write(t, d, "Missing.fango", "module Missing exposing ()\n")
	var parsed []string
	if _, errs := LoadWithOptions(entry, LoadOptions{Parsed: cache, Observe: func(stage, owner string) {
		if stage == "parse" {
			parsed = append(parsed, owner)
		}
	}}); len(errs) > 0 {
		t.Fatal(errs)
	}
	if !reflect.DeepEqual(parsed, []string{"Missing"}) {
		t.Fatalf("parsed after graph repair = %v", parsed)
	}
}

func TestIdenticalHeaderlessSourceRebindsFilename(t *testing.T) {
	d := t.TempDir()
	body := []byte("main = 1\n")
	a, b := filepath.Join(d, "A.fango"), filepath.Join(d, "B.fango")
	if err := os.WriteFile(a, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, body, 0o644); err != nil {
		t.Fatal(err)
	}
	cache := memoryParsedCache{}
	if _, errs := LoadWithOptions(a, LoadOptions{Parsed: cache}); len(errs) > 0 {
		t.Fatal(errs)
	}
	parses := 0
	r, errs := LoadWithOptions(b, LoadOptions{Parsed: cache, Observe: func(stage, _ string) {
		if stage == "parse" {
			parses++
		}
	}})
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if parses != 0 {
		t.Fatalf("identical source reparsed %d times", parses)
	}
	decl := r.Module.Decls[len(r.Module.Decls)-1].(*ast.ValueDecl)
	if decl.Sp.File == nil || decl.Sp.File.Name != "B.fango" {
		t.Fatalf("cached span file = %#v", decl.Sp.File)
	}
}

func TestMalformedSourceIsNotCached(t *testing.T) {
	d := t.TempDir()
	entry := write(t, d, "Main.fango", "main =\n")
	cache := memoryParsedCache{}
	for range 2 {
		if _, errs := LoadWithOptions(entry, LoadOptions{Parsed: cache}); len(errs) == 0 {
			t.Fatal("malformed source accepted")
		}
	}
	if len(cache) != 0 {
		t.Fatalf("malformed parse cached %d artifacts", len(cache))
	}
}

func TestFixityChangeReusesImporterAndRewritesFreshTree(t *testing.T) {
	d := t.TempDir()
	entry := write(t, d, "Main.fango", "{-# no-prelude #-}\nmodule Main exposing (main)\nimport Ops exposing ((%%), (**))\nmain = 1 %% 2 ** 3\n")
	ops := write(t, d, "Ops.fango", "{-# no-prelude #-}\nmodule Ops exposing ((%%), (**))\ninfixl 5 (%%)\ninfixl 6 (**)\n(%%) x y = x\n(**) x y = y\n")
	cache := memoryParsedCache{}
	first, errs := LoadWithOptions(entry, LoadOptions{Parsed: cache})
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if err := os.WriteFile(ops, []byte("{-# no-prelude #-}\nmodule Ops exposing ((%%), (**))\ninfixl 5 (%%)\ninfixl 4 (**)\n(%%) x y = x\n(**) x y = y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var parsed []string
	second, errs := LoadWithOptions(entry, LoadOptions{Parsed: cache, Observe: func(stage, owner string) {
		if stage == "parse" {
			parsed = append(parsed, owner)
		}
	}})
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if !reflect.DeepEqual(parsed, []string{"Ops"}) {
		t.Fatalf("parsed after fixity edit = %v", parsed)
	}
	if first.FixityHash == second.FixityHash {
		t.Fatal("fixity edit preserved effective-table hash")
	}
	if ast.Dump(first.Module) == ast.Dump(second.Module) {
		t.Fatal("fixity edit did not regroup cached importer expression")
	}
}

func TestCachedDiscoveryPreservesImplicitDependenciesAndCycles(t *testing.T) {
	t.Run("implicit syntax root", func(t *testing.T) {
		d := t.TempDir()
		entry := write(t, d, "Main.fango", "{-# no-prelude #-}\nmain = [1, 2]\n")
		cache := memoryParsedCache{}
		if _, errs := LoadWithOptions(entry, LoadOptions{Parsed: cache}); len(errs) > 0 {
			t.Fatal(errs)
		}
		parses := 0
		r, errs := LoadWithOptions(entry, LoadOptions{Parsed: cache, Observe: func(stage, _ string) {
			if stage == "parse" {
				parses++
			}
		}})
		if len(errs) > 0 {
			t.Fatal(errs)
		}
		if parses != 0 {
			t.Fatalf("cached implicit graph parsed %d files", parses)
		}
		found := false
		for _, in := range r.Manifest {
			found = found || in.Path == "<stdlib>/List.fango"
		}
		if !found {
			t.Fatal("cached list syntax lost implicit List dependency")
		}
	})

	t.Run("cycle", func(t *testing.T) {
		d := t.TempDir()
		entry := write(t, d, "Main.fango", "{-# no-prelude #-}\nmodule Main exposing (main)\nimport A\nmain = 0\n")
		write(t, d, "A.fango", "{-# no-prelude #-}\nmodule A exposing (a)\nimport B\na = 1\n")
		write(t, d, "B.fango", "{-# no-prelude #-}\nmodule B exposing (b)\nimport A\nb = 2\n")
		cache := memoryParsedCache{}
		_, first := LoadWithOptions(entry, LoadOptions{Parsed: cache})
		parses := 0
		_, second := LoadWithOptions(entry, LoadOptions{Parsed: cache, Observe: func(stage, _ string) {
			if stage == "parse" {
				parses++
			}
		}})
		if parses != 0 {
			t.Fatalf("cached cycle parsed %d files", parses)
		}
		if len(first) == 0 || len(second) == 0 || first[0].Title != second[0].Title || first[0].Body != second[0].Body || !strings.Contains(second[0].Body, "A -> B -> A") {
			t.Fatalf("cycle diagnostics changed: %#v / %#v", first, second)
		}
	})
}

func TestParsedCodecCoversRegisteredASTStructs(t *testing.T) {
	f := source.NewFile("All.fango", []byte("0123456789"))
	for _, sample := range parsedTypes {
		typ := reflect.TypeOf(sample)
		t.Run(typ.Name(), func(t *testing.T) {
			encoded, err := encodeTree(reflect.New(typ).Elem())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeTree(encoded, typ, f); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestParsedCodecRoundTripsParserCorpus(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "testdata", "parse", "*.fango"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			cache := memoryParsedCache{}
			cold, _, coldErrs := parseUnit(source.NewFile(filepath.Base(path), content), cache)
			if len(coldErrs) > 0 {
				if len(cache) != 0 {
					t.Fatal("failed parse was cached")
				}
				return
			}
			cached, hit, cachedErrs := parseUnit(source.NewFile(filepath.Base(path), content), cache)
			if len(cachedErrs) > 0 || !hit {
				t.Fatalf("cached parse hit=%v errors=%v", hit, cachedErrs)
			}
			if ast.Dump(cold.mod) != ast.Dump(cached.mod) {
				t.Fatalf("parsed codec changed AST:\n%s\n%s", ast.Dump(cold.mod), ast.Dump(cached.mod))
			}
		})
	}
}

func TestParsedCodecPreservesFloatBitsAndRejectsDamage(t *testing.T) {
	f := source.NewFile("Float.fango", []byte("main = 0\n"))
	sp := source.Span{File: f, Start: 7, End: 8}
	m := &ast.Module{Decls: []ast.Decl{
		&ast.ValueDecl{Name: "nan", Body: &ast.FloatLit{Value: math.Float64frombits(0x7ff8000000000042), Sp: sp}, Sp: sp},
		&ast.ValueDecl{Name: "negativeZero", Body: &ast.FloatLit{Value: math.Copysign(0, -1), Sp: sp}, Sp: sp},
	}}
	u := newParsedUnit(m)
	hash := strings.Repeat("a", 64)
	data, err := encodeParsed(u, hash)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeParsed(data, hash, f)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range m.Decls {
		wantBits := math.Float64bits(want.(*ast.ValueDecl).Body.(*ast.FloatLit).Value)
		gotBits := math.Float64bits(got.mod.Decls[i].(*ast.ValueDecl).Body.(*ast.FloatLit).Value)
		if gotBits != wantBits {
			t.Fatalf("float %d bits = %x, want %x", i, gotBits, wantBits)
		}
	}
	data[len(data)/2] ^= 1
	if _, err := decodeParsed(data, hash, f); err == nil {
		t.Fatal("damaged parsed artifact accepted")
	}
}
