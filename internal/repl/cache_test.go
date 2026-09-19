package repl

import (
	"github.com/waj/fango/internal/compileevent"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type objectCache struct {
	objects map[string][]byte
}

func newObjectCache() *objectCache {
	return &objectCache{objects: map[string][]byte{}}
}

func (c *objectCache) LoadObject(slot string) ([]byte, bool) {
	data, ok := c.objects[slot]
	return data, ok
}
func (c *objectCache) StoreObject(slot string, data []byte) {
	c.objects[slot] = append([]byte(nil), data...)
}

type events struct {
	counts map[string]map[string]int
}

func newEvents() *events { return &events{counts: map[string]map[string]int{}} }

func (e *events) record(event compileevent.Event) {
	if event.Begin {
		return
	}
	if e.counts[event.Stage] == nil {
		e.counts[event.Stage] = map[string]int{}
	}
	e.counts[event.Stage][event.Owner]++
}

func (e *events) total(stages ...string) int {
	n := 0
	for _, stage := range stages {
		for _, count := range e.counts[stage] {
			n += count
		}
	}
	return n
}

func run(script string, opts Options) string {
	var out strings.Builder
	RunWith(strings.NewReader(script), &out, opts)
	return out.String()
}

// A session served from artifacts must be indistinguishable from a cold one,
// transcript for transcript: staging, deriving, resources, sidecars, imported
// entry points, and redefinition generations included.
func TestCachedSessionsMatchColdTranscripts(t *testing.T) {
	t.Parallel()
	scripts, err := filepath.Glob(filepath.Join("..", "..", "testdata", "repl", "*.in"))
	if err != nil {
		t.Fatal(err)
	}
	if len(scripts) == 0 {
		t.Fatal("no transcripts")
	}
	for _, path := range scripts {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			script, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			base := strings.TrimSuffix(path, ".in")
			opts := Options{Cache: newObjectCache()}
			if info, statErr := os.Stat(base); statErr == nil && info.IsDir() {
				opts.Root = base
			}
			cold := run(string(script), opts)
			warm := run(string(script), opts)
			if cold != warm {
				t.Fatalf("a cached session diverged from the cold one:\n--- cold ---\n%s\n--- cached ---\n%s", cold, warm)
			}
			fresh := Options{Root: opts.Root, DisableCache: true}
			if uncached := run(string(script), fresh); uncached != cold {
				t.Fatalf("caching changed the transcript:\n--- uncached ---\n%s\n--- cached ---\n%s", uncached, cold)
			}
		})
	}
}

// Bootstrapping and importing are module work, counted apart from whatever the
// prompt's own inputs cost.
func TestSecondSessionDoesNoModuleWork(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "repl", "import")
	cache := newObjectCache()
	script := "import Geometry.Point\nGeometry.Point.origin\n:quit\n"
	run(script, Options{Root: root, Cache: cache})
	seen := newEvents()
	run(script, Options{Root: root, Cache: cache, Observe: seen.record})
	if work := seen.total("check", "elaborate", "semantic-lint"); work != 0 {
		t.Fatalf("a fresh cached session rechecked %d modules: %#v", work, seen.counts)
	}
	if seen.counts["checked-cache-hit"]["Geometry.Point"] != 1 || seen.total("checked-cache-hit") < 2 {
		t.Fatalf("prelude roots and imports were not reused: %#v", seen.counts)
	}
}

// A prompt is one transaction. An import that fails after an earlier one in
// the same input succeeded must leave the session as the prompt found it.
func TestFailedMultiImportLeavesNothingInstalled(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Good.fango", "module Good exposing (good)\ngood = 1\n")
	write("Bad.fango", "module Bad exposing (bad)\nbad : Int\nbad = \"no\"\n")
	var out strings.Builder
	session := NewSessionWith(&out, Options{Root: root})
	defer session.Close()
	session.submit("import Good\nimport Bad\n")
	if strings.Contains(out.String(), "loaded") {
		t.Fatalf("a failed input reported an import as loaded:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "TYPE MISMATCH") {
		t.Fatalf("the failing module did not report its error:\n%s", out.String())
	}
	// The whole input rolled back, so the earlier import left no name behind.
	out.Reset()
	session.submit("Good.good\n")
	if !strings.Contains(out.String(), "UNKNOWN QUALIFIER") {
		t.Fatalf("an import from a failed input stayed installed:\n%s", out.String())
	}
	out.Reset()
	session.submit("import Good\n")
	session.submit("Good.good\n")
	if !strings.Contains(out.String(), "loaded Good") || !strings.Contains(out.String(), "1 : Num a => a") {
		t.Fatalf("retrying after the failure did not behave like a clean session:\n%s", out.String())
	}
}

// The artifacts a failed transaction produced are immutable and valid; the
// retry reuses them rather than rechecking.
func TestFailedTransactionKeepsValidArtifacts(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Good.fango", "module Good exposing (good)\ngood = 1\n")
	write("Bad.fango", "module Bad exposing (bad)\nbad : Int\nbad = \"no\"\n")
	cache := newObjectCache()
	var failed strings.Builder
	session := NewSessionWith(&failed, Options{Root: root, Cache: cache})
	session.submit("import Good\nimport Bad\n")
	session.Close()
	if !strings.Contains(failed.String(), "TYPE MISMATCH") {
		t.Fatalf("the transaction did not fail:\n%s", failed.String())
	}
	seen := newEvents()
	got := run("import Good\nGood.good\n:quit\n", Options{Root: root, Cache: cache, Observe: seen.record})
	if !strings.Contains(got, "1 : Num a => a") {
		t.Fatalf("the retry did not behave like a clean session:\n%s", got)
	}
	if seen.counts["checked-cache-hit"]["Good"] != 1 || seen.counts["check"]["Good"] != 0 {
		t.Fatalf("the failed transaction discarded a valid artifact: %#v", seen.counts)
	}
}

func TestUnknownExposedNameLeavesSessionClean(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "repl", "import")
	got := run("import Geometry.Point exposing (nope)\nimport Geometry.Point\nGeometry.Point.origin\n:quit\n", Options{Root: root})
	if !strings.Contains(got, "UNKNOWN IMPORT") {
		t.Fatalf("an unknown exposed name was accepted:\n%s", got)
	}
	if !strings.Contains(got, "Point 0 0 : Point") {
		t.Fatalf("the session did not recover:\n%s", got)
	}
}

// The native worker is prepared before the transaction commits, so a failure
// after an earlier sidecar import neither installs it nor retires the worker
// the session is already running.
func TestFailedInputPreparesNoNativeWorker(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Sided.fango", "module Sided exposing (tag)\ntag : String -> String\ntag = native\n")
	write("Sided.native.go", "package native\n\nfunc Tag(s string) string { return s }\n")
	write("Bad.fango", "module Bad exposing (bad)\nbad : Int\nbad = \"no\"\n")
	var out strings.Builder
	session := NewSessionWith(&out, Options{Root: root})
	defer session.Close()
	before := len(session.installed)
	session.submit("import Sided\nimport Bad\n")
	if !strings.Contains(out.String(), "TYPE MISMATCH") {
		t.Fatalf("the transaction did not fail:\n%s", out.String())
	}
	if len(session.natives) != 0 || session.exec != nil {
		t.Fatalf("a failed input installed a native worker: %d sidecars, exec=%v", len(session.natives), session.exec != nil)
	}
	if len(session.installed) != before {
		t.Fatalf("a failed input left %d definitions installed", len(session.installed)-before)
	}
	out.Reset()
	session.submit("import Sided\n")
	session.submit("Sided.tag \"x\"\n")
	if !strings.Contains(out.String(), "loaded Sided") || !strings.Contains(out.String(), "x : String") {
		t.Fatalf("retrying after the failure did not behave like a clean session:\n%s", out.String())
	}
	if len(session.natives) != 1 || session.exec == nil {
		t.Fatal("the retry did not install the sidecar worker")
	}
}

// A splice that fails during an import is an ordinary failed transaction.
func TestFailedSpliceRollsBackTheInput(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Staged.fango", "module Staged exposing (value)\nvalue = $(missing)\n")
	var out strings.Builder
	session := NewSessionWith(&out, Options{Root: root})
	defer session.Close()
	before := len(session.installed)
	session.submit("import Staged\n")
	if strings.Contains(out.String(), "loaded Staged") {
		t.Fatalf("a failed splice reported its module as loaded:\n%s", out.String())
	}
	if len(session.installed) != before {
		t.Fatalf("a failed splice left %d definitions installed", len(session.installed)-before)
	}
	out.Reset()
	session.submit("1 + 1\n")
	if !strings.Contains(out.String(), "2 : Num a => a") {
		t.Fatalf("the session did not recover from a failed splice:\n%s", out.String())
	}
}
