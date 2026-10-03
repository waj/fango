package infer_test

import (
	"testing"

	"github.com/waj/fango/internal/types"
)

func TestRegexLiteralTypeAndDiagnostics(t *testing.T) {
	ck, _, errs := check(t, "main = /abc/\n")
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	scheme, _ := ck.Env.Lookup("main")
	if types.Show(scheme.Body) != "Regex" {
		t.Fatal(types.Show(scheme.Body))
	}
	for _, pattern := range []string{`[`, `(a)\1`, `a(?=b)`} {
		_, _, errs := check(t, "main = /"+pattern+"/\n")
		if len(errs) != 1 || errs[0].Error() != "INVALID REGEX" {
			t.Fatalf("%s: %v", pattern, errs)
		}
	}
}
