package format

import (
	"bytes"
	"testing"

	"github.com/waj/fango/internal/source"
)

func TestQuotationFormatting(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"code=build (`(1+2)`) (`3`)\n", "code = build `1 + 2` `3`\n"},
		{"code=`$(`1`)+$(c)`\n", "code = `$(`1`) + $(c)`\n"},
		{"quote x=x\n", "quote x = x\n"},
		{"code=`\n  1+2\n`\n", "code = `\n    1 + 2\n`\n"},
		{"code = `case True of\n    True -> 1\n    False -> 2\n`\n", "code = `case True of\n        True -> 1\n        False -> 2\n`\n"},
		{"code = `if True then\n    1\nelse\n    2\n`\n", "code = `if True then\n        1\n    else\n        2\n`\n"},
		{"code=`\"`\"`\n", "code = `\"`\"`\n"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			out, errs := Source(source.NewFile("<test>", []byte(tc.input)))
			if len(errs) != 0 {
				t.Fatal(errs)
			}
			if string(out) != tc.want {
				t.Fatalf("got:\n%s\nwant:\n%s", out, tc.want)
			}
			again, errs := Source(source.NewFile("<test>", out))
			if len(errs) != 0 || !bytes.Equal(out, again) {
				t.Fatalf("not idempotent: %v\n%s", errs, again)
			}
		})
	}
}

func TestQuotationCommentsPreserved(t *testing.T) {
	input := "code = `\n    -- backtick ` stays in the comment\n    1 + 2 {- ` -}\n`\n"
	out, errs := Source(source.NewFile("<test>", []byte(input)))
	if len(errs) != 0 || !bytes.Equal(out, []byte(input)) {
		t.Fatalf("comments changed: %v\n%s", errs, out)
	}
}
