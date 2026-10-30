package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/testutil"
)

// TestDiagnostics pins the complete text of the compiler's reports. Each case
// under testdata/diagnostics is one realistic mistake: broken/ must fail with
// exactly expected.txt, so a moved caret or a new cascade shows up as a diff,
// and fixed/ applies the fix meta.txt describes and must compile, so the mistake
// is the only thing wrong with the program.
func TestDiagnostics(t *testing.T) {
	cases, err := filepath.Glob(filepath.Join("..", "..", "testdata", "diagnostics", "*", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no diagnostic cases")
	}
	for _, dir := range cases {
		name := strings.TrimPrefix(filepath.ToSlash(dir), "../../testdata/diagnostics/")
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var diagnostics bytes.Buffer
			if _, _, ok := compileFile(filepath.Join(dir, "broken", "main.fango"), &diagnostics); ok {
				t.Fatal("broken program compiled")
			}
			testutil.Golden(t, filepath.Join(dir, "expected.txt"), diagnostics.String())
			var fixed bytes.Buffer
			if _, _, ok := compileFile(filepath.Join(dir, "fixed", "main.fango"), &fixed); !ok {
				t.Errorf("fixed program does not compile:\n%s", fixed.String())
			}
		})
	}
}
