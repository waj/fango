package format

import (
	"bytes"
	"testing"

	"github.com/waj/fango/internal/source"
)

func TestInterpolationFormatting(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"main=\"a #{1+2} b #{\"nested #{3+4}\"}\"\n", "main = \"a #{1 + 2} b #{\"nested #{3 + 4}\"}\"\n"},
		{"main=\"#{({x=2;x*3})()}\"\n", "main = \"#{{ x = 2; x * 3 }()}\"\n"},
		{"main = \"#{1 {- retained -} + 2}\"\n", "main = \"#{1 {- retained -} + 2}\"\n"},
	} {
		out, errs := Source(source.NewFile("test.fango", []byte(tc.input)))
		if len(errs) != 0 || string(out) != tc.want {
			t.Fatalf("%q: %q %v", tc.input, out, errs)
		}
		again, errs := Source(source.NewFile("test.fango", out))
		if len(errs) != 0 || !bytes.Equal(out, again) {
			t.Fatalf("not idempotent: %q %v", again, errs)
		}
	}
}
