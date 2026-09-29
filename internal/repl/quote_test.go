package repl

import (
	"strings"
	"testing"
)

func TestMultilineBacktickContinuation(t *testing.T) {
	script := "import Meta\ncode = `\ncase True of\n    True -> 42\n    False -> 0\n`\n$(code)\nquote x = x\nquote 7\n:quit\n"
	var out strings.Builder
	Run(strings.NewReader(script), &out)
	got := out.String()
	if !strings.Contains(got, "42 :") || !strings.Contains(got, "7 :") || strings.Contains(got, "SYNTAX PROBLEM") || strings.Contains(got, "UNFINISHED PROGRAM") {
		t.Fatalf("quotation continuation failed:\n%s", got)
	}
}
