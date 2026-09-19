package modules

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/waj/fango/internal/ast"
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

func TestOnlyBundledYieldEffectGetsSuspensionIdentity(t *testing.T) {
	decl := func() *ast.EffectDecl { return &ast.EffectDecl{Name: "Yield"} }
	bundledDecl := decl()
	bundled := &node{name: "Stream", bundled: true, mod: &ast.Module{Decls: []ast.Decl{bundledDecl}}}
	if errs := validateModuleDecls(bundled); len(errs) != 0 {
		t.Fatalf("bundled validation: %+v", errs)
	}
	if !bundledDecl.CompilerSuspension {
		t.Fatal("bundled Yield effect did not receive compiler suspension identity")
	}
	localDecl := decl()
	local := &node{name: "Stream", mod: &ast.Module{Decls: []ast.Decl{localDecl}}}
	if errs := validateModuleDecls(local); len(errs) != 0 {
		t.Fatalf("local validation: %+v", errs)
	}
	if localDecl.CompilerSuspension {
		t.Fatal("local Yield spelling received compiler suspension identity")
	}
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
	if strings.Join(got, ",") != "Basics,Meta,Derive,List,Maybe,IO,IO,Prelude,B,A,Z,Main" {
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
	if strings.Join(units, ",") != "Basics:,Meta:Basics,Derive:Basics+Meta,List:Basics,Maybe:Basics,IO:Basics+List+Maybe,B:,A:B,Z:,Main:Z+A" {
		t.Fatalf("units %v", units)
	}
	if !r.Units[len(r.Units)-1].Entry {
		t.Fatal("last dependency-first unit is not the entry")
	}
	if len(r.Modules) == 0 || r.Modules[len(r.Modules)-1].Role != EntryRole || r.Modules[len(r.Modules)-1].Entry != "Main.main" {
		t.Fatalf("resolved module roles: %#v", r.Modules)
	}
	for i, module := range r.Modules[:len(r.Modules)-1] {
		if module.Role != DependencyRole || module.Entry != "" {
			t.Fatalf("dependency %d has entry role: %#v", i, module)
		}
		if len(module.Module.InstanceImports) != 1 {
			t.Fatalf("module %q visibility owners = %v", module.Name, module.Module.InstanceImports)
		}
	}
}

func TestPreludeFollowsBundledImports(t *testing.T) {
	p, errs := Prelude()
	if len(errs) > 0 {
		t.Fatalf("Prelude: %v", errs)
	}
	wantOwners := []string{"Basics", "Derive", "IO", "List", "Maybe", "Meta", "Prelude", "Tuple"}
	var gotOwners []string
	for owner := range p.Owners {
		gotOwners = append(gotOwners, owner)
	}
	slices.Sort(gotOwners)
	if !slices.Equal(gotOwners, wantOwners) {
		t.Fatalf("owners %v, want %v", gotOwners, wantOwners)
	}
	listDecls := 0
	for _, decl := range p.Module.Decls {
		if d, ok := decl.(*ast.TypeDecl); ok && d.Name == "List.List" {
			listDecls++
		}
	}
	if listDecls != 1 {
		t.Fatalf("resolved prelude contains %d List declarations, want 1", listDecls)
	}
}

func TestListSyntaxAddsDependency(t *testing.T) {
	f := source.NewFile("Lists.fango", []byte("values = [1, 2]\n"))
	m, errs := parse(f)
	if len(errs) > 0 {
		t.Fatalf("parse: %v", errs)
	}
	if !slices.Contains(syntaxDependencies(m, "Example"), ListModule) {
		t.Fatal("list syntax did not add the bundled List dependency")
	}
	if slices.Contains(syntaxDependencies(m, ListModule), ListModule) {
		t.Fatal("list syntax added a self-dependency to List")
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
	entry := write(t, d, "Main.fango", "module Main exposing (main)\nimport IO\nimport List\nimport Range\nmain = 0\n")
	r, errs := Load(entry)
	if len(errs) > 0 {
		t.Fatalf("Load: %v", errs)
	}
	var got []string
	for _, m := range r.Manifest {
		got = append(got, m.Module+":"+m.Path)
	}
	want := "Basics:<stdlib>/Basics.fango,Meta:<stdlib>/Meta.fango,Derive:<stdlib>/Derive.fango,List:<stdlib>/List.fango,Maybe:<stdlib>/Maybe.fango,IO:<stdlib>/IO.fango,IO:<stdlib>/IO.native.go,Prelude:<stdlib>/Prelude.fango,Range:<stdlib>/Range.fango,Main:Main.fango"
	if strings.Join(got, ",") != want {
		t.Fatalf("manifest = %v, want %s", got, want)
	}
	if f := r.Fixity.Lookup("+"); f.Prec != 6 || f.Assoc != ast.AssocLeft || len(r.Natives) != 1 {
		t.Fatalf("declared native metadata: fixity of (+)=%v %d natives=%v", f.Assoc, f.Prec, r.Natives)
	}
}

func TestBundledPureNativeSidecars(t *testing.T) {
	d := t.TempDir()
	entry := write(t, d, "Main.fango", "module Main exposing (main)\nimport Json\nimport Random\nimport String\nmain = 0\n")
	r, errs := Load(entry)
	if len(errs) > 0 {
		t.Fatalf("Load: %v", errs)
	}
	var got []string
	for _, native := range r.Natives {
		got = append(got, native.Module+":"+native.Path)
	}
	want := []string{
		"IO:<stdlib>/IO.native.go",
		"Random:<stdlib>/Random.native.go",
		"String:<stdlib>/String.native.go",
		"Json:<stdlib>/Json.native.go",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("bundled native sources = %v, want %v", got, want)
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

func TestValidateManifestRechecksDiscoveryFacts(t *testing.T) {
	d := t.TempDir()
	entryBody := "main = 1\n"
	entry := write(t, d, "Main.fango", entryBody)
	h := sha256.Sum256([]byte(entryBody))
	manifest := []ManifestEntry{
		{Module: "<entry>", Path: "Main.fango", SHA256: hex.EncodeToString(h[:])},
		{Module: "Basics", Path: "<stdlib>/Basics.fango", SHA256: strings.Repeat("0", 64)},
	}
	if !ValidateManifest(entry, manifest) {
		t.Fatal("valid manifest rejected")
	}

	write(t, d, "Basics.fango", "module Basics exposing (value)\nvalue = 1\n")
	if ValidateManifest(entry, manifest) {
		t.Fatal("new local conflict with bundled module was accepted")
	}
	if err := os.Remove(filepath.Join(d, "Basics.fango")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(entry, filepath.Join(d, "main.fango")); err != nil {
		t.Fatal(err)
	}
	if ValidateManifest(entry, manifest) {
		t.Fatal("entry path casing change was accepted")
	}
}

func TestValidateManifestTreatsUnexpectedSidecarErrorAsMiss(t *testing.T) {
	d := t.TempDir()
	body := "main = 1\n"
	entry := write(t, d, "Main.fango", body)
	h := sha256.Sum256([]byte(body))
	manifest := []ManifestEntry{{Module: "<entry>", Path: "Main.fango", SHA256: hex.EncodeToString(h[:])}}
	if err := os.Mkdir(filepath.Join(d, "Main.native.go"), 0o755); err != nil {
		t.Fatal(err)
	}
	if ValidateManifest(entry, manifest) {
		t.Fatal("sidecar read error was treated as absence")
	}
}

func TestNativeSidecarValidation(t *testing.T) {
	t.Run("effectful value", func(t *testing.T) {
		d := t.TempDir()
		entry := write(t, d, "Main.fango", "module Main exposing (main)\neffect Clock\n    tick : () -> Int\nvalue : () ->{Clock} Int\nvalue = native\nmain = 0\n")
		write(t, d, "Main.native.go", "package native\nfunc Value() int64 { return 42 }\n")
		if _, errs := Load(entry); len(errs) > 0 {
			t.Fatalf("effectful native: %v", errs)
		}
	})

	t.Run("valid scalar ABI", func(t *testing.T) {
		d := t.TempDir()
		entry := write(t, d, "Main.fango", "module Main exposing (main)\nimport Hash\nmain = Hash.twice 21\n")
		write(t, d, "Hash.fango", "module Hash exposing (twice)\ntwice : Int -> Int\ntwice = native\n")
		write(t, d, "Hash.native.go", "package native\nfunc Twice(x int64) int64 { return x * 2 }\n")
		r, errs := Load(entry)
		if len(errs) > 0 {
			t.Fatalf("Load: %v", errs)
		}
		found := false
		for _, native := range r.Natives {
			found = found || native.Module == "Hash"
		}
		if !found {
			t.Fatalf("native sources: %#v", r.Natives)
		}
	})

	tests := []struct{ name, source, sidecar, title string }{
		{"missing sidecar", "value : Int -> Int\nvalue = native\n", "", "MISSING NATIVE SIDECAR"},
		{"package", "value : Int -> Int\nvalue = native\n", "package wrong\nfunc Value(x int64) int64 { return x }\n", "NATIVE PACKAGE NAME"},
		{"external import", "value : Int -> Int\nvalue = native\n", "package native\nimport _ \"example.com/nope\"\nfunc Value(x int64) int64 { return x }\n", "NATIVE IMPORT NOT ALLOWED"},
		{"shape", "value : Int -> Int\nvalue = native\n", "package native\nfunc Value(x string) int64 { return 0 }\n", "NATIVE ABI"},
		{"template", "value : Int -> Int\nvalue = native \"$1\"\n", "", "NATIVE TEMPLATE NOT ALLOWED"},
		{"fixity without definition", "value = 1\ninfixl 6 (<+>)\n", "", "FIXITY WITHOUT DEFINITION"},
		{"duplicate fixity", "(<+>) : Int -> Int -> Int\n(<+>) a b = a\ninfixl 6 (<+>)\ninfixl 6 (<+>)\n", "", "DUPLICATE FIXITY"},
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

func TestNativeEffectAndReservedHostValidation(t *testing.T) {
	d := t.TempDir()
	entry := write(t, d, "Main.fango", "module Main exposing (main)\neffect Clock\n    tick : () -> Int = native\nmain = tick()\n")
	write(t, d, "Main.native.go", "package native\nfunc Tick() int64 { return 42 }\n")
	if _, errs := Load(entry); len(errs) > 0 {
		t.Fatalf("native effect: %v", errs)
	}
	write(t, d, "Main.native.go", "package native\nvar FangoHost any\nfunc Tick() int64 { return 42 }\n")
	_, errs := Load(entry)
	if len(errs) == 0 || errs[0].Title != "RESERVED NATIVE IDENTIFIER" {
		t.Fatalf("reserved host errors: %#v", errs)
	}
}

// Fixity is graph-wide, so it is the one operator property two modules can
// disagree about. Declaring the same operator in both is fine; only a
// conflicting fixity, or importing both spellings unqualified, is an error.
func TestCrossModuleOperators(t *testing.T) {
	const opA = "module A exposing ((<+>))\n(<+>) : Int -> Int -> Int\n(<+>) a b = a\n"
	for _, tt := range []struct{ name, a, b, main, title string }{
		{"conflicting fixity",
			opA + "infixl 6 (<+>)\n",
			"module B exposing ((<+>))\n(<+>) : Int -> Int -> Int\n(<+>) a b = b\ninfixr 3 (<+>)\n",
			"import A\nimport B\n", "CONFLICTING FIXITY"},
		{"same fixity in both",
			opA + "infixl 6 (<+>)\n",
			"module B exposing ((<+>))\n(<+>) : Int -> Int -> Int\n(<+>) a b = b\ninfixl 6 (<+>)\n",
			"import A\nimport B\n", ""},
		{"both exposed unqualified",
			opA + "infixl 6 (<+>)\n",
			"module B exposing ((<+>))\n(<+>) : Int -> Int -> Int\n(<+>) a b = b\ninfixl 6 (<+>)\n",
			"import A exposing ((<+>))\nimport B exposing ((<+>))\n", "UNQUALIFIED COLLISION"},
		{"imported operator used infix",
			opA + "infixl 6 (<+>)\n", "module B exposing ()\nunused = 1\n",
			"import A exposing ((<+>))\nvalue = 1 <+> 2\n", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := t.TempDir()
			entry := write(t, d, "Main.fango", "module Main exposing (main)\n"+tt.main+"main = 0\n")
			write(t, d, "A.fango", tt.a)
			write(t, d, "B.fango", tt.b)
			_, errs := Load(entry)
			if tt.title == "" {
				if len(errs) > 0 {
					t.Fatalf("unexpected errors: %#v", errs)
				}
				return
			}
			if len(errs) == 0 || errs[0].Title != tt.title {
				t.Fatalf("errors: %#v", errs)
			}
		})
	}
}

func TestResourceExportsAreOpaque(t *testing.T) {
	for _, tc := range []struct {
		exports, decl string
		bad           bool
	}{
		{"Handle", "type Handle = Handle Int", false},
		{"Handle(..)", "type Handle = Handle Int", true},
		{"..", "type Handle = Handle Int", true},
		{"Handle", "type Handle = { id : Int }", false},
		{"Handle(..)", "type Handle = { id : Int }", true},
	} {
		t.Run(tc.exports+tc.decl, func(t *testing.T) {
			dir := t.TempDir()
			entry := write(t, dir, "Main.fango", "module Main exposing (main)\nimport Resource\nmain = 0\n")
			write(t, dir, "Resource.fango", "module Resource exposing ("+tc.exports+")\n{-# resource #-}\n"+tc.decl+"\n")
			_, errs := Load(entry)
			found := false
			for _, e := range errs {
				found = found || e.Title == "RESOURCE REPRESENTATION EXPOSED"
			}
			if found != tc.bad || (!tc.bad && len(errs) > 0) {
				t.Fatalf("errors: %v", errs)
			}
		})
	}
}
