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
	foundWide := false
	foundWideScan := false
	for i := range result.Program.Defs {
		def := &result.Program.Defs[i]
		streaming := strings.HasSuffix(def.Name, "_json_fields")
		scanning := strings.HasSuffix(def.Name, "_json_scan_fields")
		if !streaming && !scanning {
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
		if _, ok := core.DetectTailLoop(def); !ok {
			t.Fatalf("generated record key loop is not eligible for constant-stack recursion: %s", def.Name)
		}
		if len(def.Params) == 7 && def.Params[0] == "_json_first" {
			foundWide = true
			for j := 1; j < len(def.Params); j++ {
				if !strings.HasPrefix(def.Params[j], "_json_slot_") {
					t.Fatalf("expected separate field arguments, got %v", def.Params)
				}
			}
		}
	}
	if !foundWide {
		t.Fatal("missing generated six-field key loop")
	}
	if !foundWideScan {
		t.Fatal("missing generated six-field scan loop")
	}
}
