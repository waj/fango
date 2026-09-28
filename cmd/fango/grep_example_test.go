package main

import (
	"path/filepath"
	"testing"
)

// The grep-lite example runs through the ordinary differential harness: its
// seed directory, arguments, and expected output live under examples/fixtures.
func TestGrepExample(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "examples", "grep.fango")
	runDifferentialCase(t, path, cliRunner(path))
}

// The exit statuses and the failure reporting are what make grep-lite a
// command-line tool rather than a demo, so each is pinned under both backends
// against the same seed directory.
func TestGrepExampleFailures(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "examples", "grep.fango")
	base := filepath.Join("..", "..", "examples", "grep")
	seed := readFixtureInputs(t, base)
	compiled := cliRunner(path)
	cases := []struct {
		name   string
		args   []string
		status int
		output string
	}{
		{"no match", []string{"omega", "poem.txt"}, 1, ""},
		{"single file omits the prefix", []string{"alpha", "poem.txt"}, 0, "1:alpha line\n"},
		{"missing file", []string{"alpha", "poem.txt", "missing.txt"}, 2,
			"poem.txt:1:alpha line\nmissing.txt: no such file or directory\n"},
		{"directory without -r", []string{"alpha", "notes", "poem.txt"}, 2,
			"notes: is a directory\npoem.txt:1:alpha line\n"},
		{"usage", []string{"alpha"}, 2, "Usage: grep [-r] PATTERN PATH...\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := fixtureInputs{args: tc.args, status: tc.status, files: seed.files}
			runDifferentialCaseWith(t, path, compiled, in, tc.output)
		})
	}
}
