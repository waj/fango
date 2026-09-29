package parser

import (
	"testing"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/source"
)

func TestJSONFieldAttributes(t *testing.T) {
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
	module, count := parse("type Settings = { {-# json key \"full_name\" #-} name : String, {-# json skip #-} {-# json default \"local\" #-} secret : String }\n")
	if count != 0 {
		t.Fatalf("valid attributes: %d parse errors", count)
	}
	fields := module.Decls[0].(*ast.TypeDecl).RecordFields
	if fields[0].JSONKey != "full_name" || !fields[0].JSONKeySet || !fields[1].JSONSkip || fields[1].JSONDefault == nil {
		t.Fatalf("attributes lost: %+v", fields)
	}
	for _, src := range []string{
		"type T = { {-# json skip #-} x : Int }\n",
		"type T = { {-# json key \"x\" #-} {-# json key \"y\" #-} x : Int }\n",
		"type T = { {-# json key \"y\" #-} x : Int, y : Int }\n",
		"type T = { {-# json default #-} x : Int }\n",
	} {
		if _, count := parse(src); count == 0 {
			t.Errorf("accepted invalid JSON attribute: %q", src)
		}
	}
}
