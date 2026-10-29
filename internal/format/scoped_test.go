package format

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/source"
)

func TestScopedExtensionRoundTrip(t *testing.T) {
	input := "run : (Reader {#s} ->{Fail String,#s} a) ->{e} a\nrun action = action value\n"
	want := "run : (Reader {#s} ->{Fail String, #s} a) ->{e} a\nrun action = action value\n"
	for _, closed := range []bool{false, true} {
		if closed {
			input = strings.ReplaceAll(input, "->{e}", "->")
			want = strings.ReplaceAll(want, "->{e}", "->")
		}
		for range 2 {
			got, errs := Source(source.NewFile("scoped.fango", []byte(input)))
			if len(errs) != 0 || string(got) != want || strings.Contains(string(got), "{-#") {
				t.Fatalf("format: %s; errors %v", got, errs)
			}
			input = string(got)
		}
	}
}
