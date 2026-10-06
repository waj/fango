package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/check"
)

func TestInterpolationNavigation(t *testing.T) {
	root := t.TempDir()
	entry := filepath.Join(root, "Main.fango")
	data := "module Main exposing (main)\nmain =\n    name = \"Ada\"\n    \"Hello, #{name}\"\n"
	if err := os.WriteFile(entry, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	result, errs, internal := (&check.Session{DisableObjectCache: true}).Compile(entry)
	if internal != nil || len(errs) > 0 {
		t.Fatalf("%v %v", internal, errs)
	}
	idx := newIndex(root, result)
	var definition, use string
	for _, u := range idx.documents[entry].uses {
		text := string(u.span.File.Content[u.span.Start:u.span.End])
		if text == "name" {
			if u.span.StartPos().Line == 3 {
				definition = u.target
			}
			if u.span.StartPos().Line == 4 {
				use = u.target
			}
		}
	}
	if definition == "" || use != definition {
		t.Fatalf("%q / %q", definition, use)
	}
	if sym := idx.symbols[use]; !strings.Contains(sym.typeText, "String") {
		t.Fatalf("hover: %#v", sym)
	}
}
