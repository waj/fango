package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/codegen"
)

// A generic captured argument and its dictionary cross every factory layer.
// The marker is executable code, not a count of wrappers or type declarations.
func TestEffectCallbackGrowthIsBounded(t *testing.T) {
	for depth := 0; depth <= 6; depth++ {
		t.Run(fmt.Sprint(depth), func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "nested.fango")
			body := `print text; print "effect-body-probe"`
			for range depth {
				body = "{ " + body + " }"
			}
			program := "make text = " + body + "\nmain() = make 42" + strings.Repeat(" ()", depth) + "\n"
			if err := os.WriteFile(source, []byte(program), 0600); err != nil {
				t.Fatal(err)
			}
			var diagnostics bytes.Buffer
			result, ok := checkGraph(source, &diagnostics, &compilationSession{noCache: true})
			if !ok {
				t.Fatal(diagnostics.String())
			}
			for _, strategy := range []string{"baseline", "factory", "selective"} {
				var units []codegen.Unit
				for _, unit := range result.Graph.Units {
					units = append(units, codegen.Unit{Name: unit.Name, Program: unit.Program, Imports: unit.Imports, Entry: unit.Entry, Source: unit.Source, BodyStrategy: strategy})
				}
				files, err := codegen.EmitProject(result.Program, result.Checker.B, units)
				if err != nil {
					t.Fatal(err)
				}
				count := strings.Count(string(entryFile(t, files)), `"effect-body-probe"`)
				t.Logf("%s: depth=%d bodies=%d entryBytes=%d", strategy, depth, count, len(entryFile(t, files)))
				if strategy == "baseline" {
					continue
				}
				limit := 2
				if strategy == "selective" && depth == 0 {
					limit = 1
				}
				if count < 1 || count > limit {
					t.Errorf("%s: %d bodies, limit %d", strategy, count, limit)
				}
			}
		})
	}
}
