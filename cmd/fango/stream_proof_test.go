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

func TestResidualInvocationRejectsMissingProducerEvidence(t *testing.T) {
	var diagnostics bytes.Buffer
	p, ck, ok := compileFile(filepath.Join("..", "..", "testdata", "run", "stream_recovery.fango"), &diagnostics)
	if !ok {
		t.Fatal(diagnostics.String())
	}
	found := false
	for i := range p.Defs {
		core.Inspect(p.Defs[i].Body, func(e core.Expr) {
			if row := core.ExpressionRow(e); row != nil && len(row.Effects) != 0 {
				row.Effects = nil
				found = true
			}
		})
	}
	if !found {
		t.Fatal("fixture has no residual evidence overlay")
	}
	// Reconstruct contracts as well: rejection must not depend solely on an
	// old graph disagreeing with a changed node.
	if got := fmt.Sprint(core.InferCaptures(p, ck.B)); !strings.Contains(got, "missing deferred evidence") {
		t.Fatalf("missing invocation evidence accepted: %s", got)
	}
}

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
