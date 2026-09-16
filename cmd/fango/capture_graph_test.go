package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/waj/fango/internal/testutil"
)

func TestCaptureGraphDifferential(t *testing.T) {
	for _, effectful := range []bool{false, true} {
		t.Run(fmt.Sprint(effectful), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "graph.fango")
			if err := os.WriteFile(path, []byte(testutil.CaptureGraph(8, 3, effectful)), 0600); err != nil {
				t.Fatal(err)
			}
			expected := "3\n"
			if effectful {
				expected = "Ok 3\n"
			}
			runDifferentialCaseWith(t, path, cliRunner(path), fixtureInputs{}, expected)
		})
	}
}
