package infer_test

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/types"
)

func TestInterpolationDisplayEvidence(t *testing.T) {
	ck, _, errs := check(t, "render x = \"value=#{x}\"\nmain() = print (render 42)\n")
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	scheme, _ := ck.Env.Lookup("render")
	if got := types.Show(scheme.Body); got != "a -> String" || len(scheme.Preds) != 1 || !strings.HasSuffix(scheme.Preds[0].Class, "Display") {
		t.Fatalf("%+v", scheme)
	}
	for _, input := range []string{
		"type Secret = Secret\nmain() = print (\"#{Secret}\")\n",
		"render : Show a => a -> String\nrender x = \"#{x}\"\nmain() = print (render 1)\n",
	} {
		_, _, errs := check(t, input)
		if len(errs) == 0 {
			t.Fatalf("accepted %s", input)
		}
	}
}
