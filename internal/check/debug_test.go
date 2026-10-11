package check

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/debugger"
)

func TestDebugCacheProfileAndPositions(t *testing.T) {
	dir := t.TempDir()
	entry := filepath.Join(dir, "Main.fango")
	source := "{-# no-prelude #-}\nmodule Main exposing (main)\nmain = \"hello\"\n"
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(entry, []byte(s), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(source)
	cache := newMemoryObjectCache()
	compile := func(debug bool, line int) {
		t.Helper()
		result, diags, err := (&Session{Debug: debug, Cache: cache}).Compile(entry)
		if err != nil || len(diags) > 0 {
			t.Fatalf("compile: %v %v", err, diags)
		}
		found := false
		for _, d := range result.Program.Defs {
			if d.Name != "Main.main" {
				continue
			}
			found = true
			site := d.Body.DebugSite()
			if !debug && site != nil {
				t.Fatal("ordinary compile reused debug metadata")
			}
			if debug && (site == nil || site.Line != line) {
				t.Fatalf("debug position: %+v", site)
			}
		}
		if !found {
			t.Fatal("main missing")
		}
	}
	compile(false, 3)
	compile(true, 3)
	compile(true, 3)
	compile(false, 3)
	write("-- moved\n" + source)
	compile(true, 4)
	compile(true, 4)
}

func TestDebugMetadataDoesNotChangeCanonicalFingerprint(t *testing.T) {
	expr := &core.StringLit{Val: "hello"}
	before := canonicalDigest(expr, &ModuleObject{})
	expr.SetDebugSite(&debugger.Site{Source: &debugger.Source{Name: "X", Text: "moved source", Hash: "hash"}, Line: 100})
	if after := canonicalDigest(expr, &ModuleObject{}); after != before {
		t.Fatal("source metadata changed semantic fingerprint")
	}
}
