package repl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/compileevent"
)

func progressRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, src := range map[string]string{
		"Base":           "base = 1\n",
		"LongDependency": "long = 2\n",
		"Foo":            "import Base\nfoo = Base.base\n",
		"Bar":            "import Base\nimport LongDependency\nbar = Base.base\n",
		"Bad":            "bad = unknownName\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name+".fango"), []byte("module "+name+" exposing (..)\n"+src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestInteractiveImportSummaries(t *testing.T) {
	root := progressRoot(t)
	for _, tc := range []struct{ name, input, summary string }{
		{"no dependencies", "import Base", "loaded Base\n"},
		{"one dependency", "import Foo as F", "loaded Foo (and 1 other module)\n"},
		{"several dependencies", "import Bar", "loaded Bar (and 2 other modules)\n"},
		{"shared dependency", "import Foo\nimport Bar", "loaded Foo, Bar (and 2 other modules)\n"},
		{"explicit dependency", "import Foo\nimport Base", "loaded Foo, Base\n"},
		{"duplicate", "import Foo\nimport Foo", "loaded Foo (and 1 other module)\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			s := NewSessionWith(&out, Options{Root: root, Interactive: true, DisableCache: true})
			defer s.Close()
			if out.Len() != 0 {
				t.Fatalf("bootstrap produced output: %q", out.String())
			}
			s.submit(tc.input)
			got := out.String()
			if !strings.HasSuffix(got, "\r\x1b[2K"+tc.summary) || strings.Count(got, "\n") != 1 {
				t.Fatalf("expected a single final summary %q, got %q", tc.summary, got)
			}
			out.Reset()
			s.submit(tc.input)
			if out.Len() != 0 {
				t.Fatalf("repeated imports produced output: %q", out.String())
			}
		})
	}
}

func TestImportProgressClearsShorterNames(t *testing.T) {
	var out strings.Builder
	p := &importProgress{out: &out}
	p.show("LongDependency")
	p.observe(compileevent.Event{Stage: "check", Owner: "Foo", Begin: true})
	p.observe(compileevent.Event{Stage: "elaborate", Owner: "Foo", Begin: true})
	p.clear()
	if got, want := out.String(), "\r\x1b[2Kloading LongDependency\r\x1b[2Kloading Foo\r\x1b[2K"; got != want {
		t.Fatalf("progress = %q, want %q", got, want)
	}
}

func TestInteractiveImportExcludesPreviouslyLoadedModules(t *testing.T) {
	var out strings.Builder
	s := NewSessionWith(&out, Options{Root: progressRoot(t), Interactive: true, DisableCache: true})
	defer s.Close()
	s.submit("import Foo")
	out.Reset()
	s.submit("import Foo\nimport Bar")
	if got := out.String(); !strings.HasSuffix(got, "loaded Bar (and 1 other module)\n") || strings.Contains(got, "loading Foo") || strings.Contains(got, "loading Base") {
		t.Fatalf("previously loaded modules appeared in progress or summary: %q", got)
	}
}

func TestInteractiveImportFailureAndRetry(t *testing.T) {
	root := progressRoot(t)
	for _, bad := range []string{"Missing", "Bad"} {
		t.Run(bad, func(t *testing.T) {
			var out strings.Builder
			s := NewSessionWith(&out, Options{Root: root, Interactive: true, DisableCache: true})
			defer s.Close()
			s.submit("import Foo\nimport " + bad)
			got := out.String()
			last := strings.LastIndex(got, "\r\x1b[2K")
			if last < 0 || strings.HasPrefix(got[last+len("\r\x1b[2K"):], "loading ") || strings.Contains(got, "loaded ") || s.graph.Loaded("Foo") {
				t.Fatalf("failed import did not clear progress and roll back: %q", got)
			}
			out.Reset()
			s.submit("import Foo")
			if !strings.HasSuffix(out.String(), "loaded Foo (and 1 other module)\n") {
				t.Fatalf("retry failed: %q", out.String())
			}
		})
	}
}

func TestInteractiveCachedImportObserver(t *testing.T) {
	root, cache := progressRoot(t), newObjectCache()
	var transcripts []string
	for range 2 {
		var out strings.Builder
		seen := newEvents()
		s := NewSessionWith(&out, Options{Root: root, Interactive: true, Cache: cache, Observe: seen.record})
		if seen.total("check", "checked-cache-hit") == 0 {
			t.Fatal("bootstrap events did not reach caller")
		}
		s.submit("import Foo")
		s.Close()
		transcripts = append(transcripts, out.String())
		if len(transcripts) == 2 && (seen.counts["checked-cache-hit"]["Foo"] != 1 || seen.counts["checked-cache-hit"]["Base"] != 1) {
			t.Fatalf("cached import events did not reach caller: %#v", seen.counts)
		}
	}
	if transcripts[0] != transcripts[1] {
		t.Fatalf("cold and cached progress differ: %q / %q", transcripts[0], transcripts[1])
	}
}

func TestEditorImportProgressLeavesCleanTail(t *testing.T) {
	ed, out := runScripted(t, typed("import Dict"), typed("ask() ="),
		typed(`    IO.write "Name? "`), typed("    readLine ()"), typed(""), typed("ask()"), typed("Ada"))
	if !strings.Contains(out, "\r\x1b[2Kloaded Dict") || len(ed.tails) != 1 || ed.tails[0] != "Name? " {
		t.Fatalf("import progress disturbed program read: output %q, tails %q", out, ed.tails)
	}
}
