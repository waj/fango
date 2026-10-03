package format

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/source"
)

func TestRegexFormattingPreservesMeaning(t *testing.T) {
	input := "main =\n    a = x/y/z\n    b = x /y\n    f /a\\/b/ /\\\\/ // /--{-\"}/\n"
	output, errs := Source(source.NewFile("test.fango", []byte(input)))
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	want := strings.ReplaceAll(strings.ReplaceAll(input, "x/y/z", "x / y / z"), "x /y", "x / y")
	if string(output) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", output, want)
	}
	again, errs := Source(source.NewFile("test.fango", output))
	if len(errs) != 0 || string(again) != string(output) {
		t.Fatalf("formatter not idempotent: %s %v", again, errs)
	}
}
