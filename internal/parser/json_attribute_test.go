package parser

import (
	"testing"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/source"
)

func TestFieldAttributes(t *testing.T) {
	parse := func(src string) (*ast.Module, int) {
		t.Helper()
		file := source.NewFile("json.fango", []byte(src))
		tokens, lexErrs := lexer.Lex(file)
		if len(lexErrs) != 0 {
			t.Fatalf("lex %q: %v", src, lexErrs)
		}
		module, errs := Parse(tokens, file)
		return module, len(errs)
	}
	module, count := parse("type Settings = { #[Json.Key \"full_name\"] name : String, #[Json.Skip, Json.Default (quote \"local\")] secret : String }\n")
	if count != 0 {
		t.Fatalf("valid attributes: %d parse errors", count)
	}
	fields := module.Decls[0].(*ast.TypeDecl).RecordFields
	if len(fields[0].Attributes) != 1 || len(fields[0].Attributes[0].Exprs) != 1 || len(fields[1].Attributes[0].Exprs) != 2 {
		t.Fatalf("attributes lost: %+v", fields)
	}
	for _, src := range []string{
		"type T = { #[] x : Int }\n",
		"type T = { x : Int #[] }\n",
		"type T = { x : Int #[Foo,, Bar] }\n",
		"type T = { x : Int #[Foo }\n",
		"type T = { x : #[Foo] Int }\n",
		"type T = { #[Foo,, Bar] x : Int }\n",
		"type T = { #[Foo x : Int }\n",
		"#[Foo]\nx = 1\n",
		"type T = { {-# json skip #-} x : Int }\n",
	} {
		if _, count := parse(src); count == 0 {
			t.Errorf("accepted invalid attributes: %q", src)
		}
	}
}

func TestTrailingFieldAttributeBoundaries(t *testing.T) {
	parse := func(src string) *ast.Module {
		t.Helper()
		file := source.NewFile("attributes.fango", []byte(src))
		tokens, lexErrs := lexer.Lex(file)
		if len(lexErrs) != 0 {
			t.Fatal(lexErrs)
		}
		module, errs := Parse(tokens, file)
		if len(errs) != 0 {
			t.Fatal(errs)
		}
		return module
	}
	prefix := parse("type T = { #[A] #[B (1, 2), C [3, 4],] x : List (Int, String), #[D] y : Int ->{IO} String }\n")
	for _, text := range []string{
		"type T = { x : List (Int, String) #[A] #[B (1, 2), C [3, 4],], y : Int ->{IO} String #[D] }\n",
		"type T = { #[A] x : List (Int, String) #[B (1, 2), C [3, 4],], y : Int ->{IO} String\n    #[D]\n}\n",
	} {
		got := parse(text)
		if ast.Dump(got) != ast.Dump(prefix) {
			t.Fatalf("attributes or field types changed:\n%s\n%s", ast.Dump(prefix), ast.Dump(got))
		}
	}
	got := parse("type T = { x : Int #[A], #[B] y : String, z : Bool #[C] }\n")
	for i, f := range got.Decls[0].(*ast.TypeDecl).RecordFields {
		if len(f.Attributes) != 1 || f.Attributes[0].Exprs[0].(*ast.Ctor).Name != []string{"A", "B", "C"}[i] {
			t.Fatalf("attribute attached to the wrong field: %+v", f)
		}
	}
}
