package main

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	machineir "github.com/waj/fango/internal/machine"
)

func TestMachineClosureRejectsMissingEvidence(t *testing.T) {
	var diagnostics bytes.Buffer
	p, ck, ok := compileFile(filepath.Join("..", "..", "testdata", "run", "stream_operations.fango"), &diagnostics)
	if !ok {
		t.Fatal(diagnostics.String())
	}
	mp, errs := machineir.Lower(p, ck.B)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	found := false
	for i := range mp.Closures {
		if len(mp.Closures[i].CapturedEvidence) != 0 {
			mp.Closures[i].CapturedEvidence = nil
			found = true
			break
		}
	}
	if !found {
		t.Fatal("fixture has no captured evidence")
	}
	if got := fmt.Sprint(machineir.Lint(mp)); !strings.Contains(got, "missing captured evidence") {
		t.Fatalf("errors: %s", got)
	}
}
