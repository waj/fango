package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// Reconstruct contracts as well: rejection must not depend solely on an
// old graph disagreeing with a changed node.

func TestExtractedCallbackCannotRetagResidualABI(t *testing.T) {
	var diagnostics bytes.Buffer
	p, ck, ok := compileFile(filepath.Join("..", "..", "testdata", "run", "row_kind_adt.fango"), &diagnostics)
	if !ok {
		t.Fatal(diagnostics.String())
	}
	found := false
	for i := range p.Defs {
		if p.Defs[i].Name != "main" {
			continue
		}
		core.Inspect(p.Defs[i].Body, func(e core.Expr) {
			ref, ok := e.(*core.VarRef)
			if !ok || found || !strings.HasPrefix(ref.Name, "_c") {
				return
			}
			if fn, ok := ref.Ty.(*types.TFun); ok {
				copy := *fn
				copy.OpenRow = !fn.OpenRow
				ref.Ty, found = &copy, true
			}
		})
	}
	if !found {
		t.Fatal("fixture has no extracted function field")
	}
	if got := fmt.Sprint(core.Lint(p, ck.B)); !strings.Contains(got, "changes its binding type") {
		t.Fatalf("extracted callback retagging accepted: %s", got)
	}
}
