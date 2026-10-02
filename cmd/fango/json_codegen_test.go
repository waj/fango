package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
)

func TestJSONRecordFieldArgumentLoop(t *testing.T) {
	var diagnostics bytes.Buffer
	result, ok := checkGraph(filepath.Join("..", "..", "testdata", "run", "json_field_arguments.fango"), &diagnostics, nil)
	if !ok {
		t.Fatal(diagnostics.String())
	}
	foundWideScan := false
	for i := range result.Program.Defs {
		def := &result.Program.Defs[i]
		if strings.HasSuffix(def.Name, "_json_fields") {
			t.Fatalf("generated duplicate streaming record parser: %s", def.Name)
		}
		scanning := strings.HasSuffix(def.Name, "_json_scan_fields")
		if !scanning {
			continue
		}
		if scanning && len(def.Params) == 8 && def.Params[0] == "_json_scan_cursor" && def.Params[1] == "_json_scan_first" {
			foundWideScan = true
			for j := 2; j < len(def.Params); j++ {
				if !strings.HasPrefix(def.Params[j], "_json_scan_slot_") {
					t.Fatalf("expected separate scanned field arguments, got %v", def.Params)
				}
			}
		}
		pureRecursion := false
		core.InspectPruned(def.Body, func(expr core.Expr) bool {
			if _, ok := expr.(*core.Lambda); ok {
				return false
			}
			if app, ok := expr.(*core.App); ok {
				if ref, ok := app.Callee.(*core.VarRef); ok && ref.Name == def.Name {
					pureRecursion = true
				}
			}
			return true
		})
		// A schema containing only skipped fields returns a suspension for
		// each key; runScan drives those steps without recursive execution.
		if _, ok := core.DetectTailLoop(def); !ok && pureRecursion {
			t.Fatalf("generated record key loop is not eligible for constant-stack recursion: %s", def.Name)
		}
	}
	if !foundWideScan {
		t.Fatal("missing generated six-field scan loop")
	}
	for _, name := range []string{"Json.Pull.runScan", "Json.Pull.scanListMore"} {
		found := false
		for i := range result.Program.Defs {
			def := &result.Program.Defs[i]
			if def.Name == name {
				found = true
				if _, ok := core.DetectTailLoop(def); !ok {
					t.Fatalf("resumable scan driver is not a tail loop: %s", name)
				}
			}
		}
		if !found {
			t.Fatalf("missing resumable scan driver: %s", name)
		}
	}
}
