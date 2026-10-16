package repl

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func completionSession(t *testing.T, debug bool) *Session {
	t.Helper()
	root := t.TempDir()
	for name, body := range map[string]string{
		"Shapes/Circle.fango": "module Shapes.Circle exposing (area)\narea : Int -> Int\narea r = r * r * 3\n",
		"lower/Hidden.fango":  "module Hidden exposing (x)\nx = 1\n",
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var out strings.Builder
	s := NewSessionWith(&out, Options{Root: root, DisableCache: true, Debug: debug})
	t.Cleanup(s.Close)
	return s
}

// completeAt completes text with the cursor at the `|` marker, or at the end.
func completeAt(s *Session, text string) (string, []string, string) {
	pos := strings.IndexByte(text, '|')
	if pos < 0 {
		pos = len(text)
	} else {
		text = text[:pos] + text[pos+1:]
	}
	return s.complete(text, pos)
}

func TestCompleteNames(t *testing.T) {
	s := completionSession(t, false)
	s.submit("import Http.Client as C")
	s.submit("fortyTwo = 42")
	for _, tc := range []struct {
		text       string
		head, tail string
		want       []string
		absent     []string
	}{
		{text: "Li", want: []string{"List."}, absent: []string{"List.map"}},
		{text: "List.ma", want: []string{"List.map"}},
		{text: "1 + forty", head: "1 + ", want: []string{"fortyTwo"}},
		{text: "C.", want: []string{"C.getText"}},
		{text: "impo", want: []string{"import"}},
		{text: "show (List.ma|) xs", head: "show (", tail: ") xs", want: []string{"List.map"}},
		{text: "    ", head: "    "},
	} {
		head, items, tail := completeAt(s, tc.text)
		if head != tc.head || tail != tc.tail {
			t.Errorf("%q: head, tail = %q, %q, want %q, %q", tc.text, head, tail, tc.head, tc.tail)
		}
		if tc.want == nil && items != nil {
			t.Errorf("%q: completions = %q, want none", tc.text, items)
		}
		for _, w := range tc.want {
			if !slices.Contains(items, w) {
				t.Errorf("%q: completions %q lack %q", tc.text, items, w)
			}
		}
		for _, a := range tc.absent {
			if slices.Contains(items, a) {
				t.Errorf("%q: completions %q include %q", tc.text, items, a)
			}
		}
	}
}

func TestCompleteCommandsAndModules(t *testing.T) {
	s := completionSession(t, false)
	if head, items, _ := completeAt(s, ":re"); head != "" || !slices.Equal(items, []string{":reload"}) {
		t.Errorf(":re completes to %q after %q", items, head)
	}
	if _, items, _ := completeAt(s, ":"); slices.Contains(items, ":break") {
		t.Errorf("debugger commands offered without --debug: %q", items)
	}
	if _, items, _ := completeAt(s, "import Sha"); !slices.Equal(items, []string{"Shapes."}) {
		t.Errorf("import Sha completes to %q", items)
	}
	if _, items, _ := completeAt(s, "import Shapes.C"); !slices.Equal(items, []string{"Shapes.Circle"}) {
		t.Errorf("import Shapes.C completes to %q", items)
	}
	if _, items, _ := completeAt(s, "import Li"); !slices.Contains(items, "List") {
		t.Errorf("import Li lacks the bundled List: %q", items)
	}
	if _, items, _ := completeAt(s, "import Hid"); len(items) != 0 {
		t.Errorf("a module under a lowercase directory completes: %q", items)
	}

	debug := completionSession(t, true)
	if _, items, _ := completeAt(debug, ":br"); !slices.Equal(items, []string{":break", ":breaks"}) {
		t.Errorf(":br in a debug session completes to %q", items)
	}
}
