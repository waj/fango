package modules

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/libroot"
)

// libraryRoot copies the checkout's standard library into a fresh library
// root and adds the given files beneath it, so a test can give the root
// libraries of its own. Resolution is process-wide: the caller must not run in
// parallel.
func libraryRoot(t *testing.T, files map[string]string) {
	t.Helper()
	real, err := libroot.Root()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	stdlib := filepath.Join(real, "stdlib")
	err = filepath.WalkDir(stdlib, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		if d.IsDir() || !(strings.HasSuffix(path, ".fango") || strings.HasSuffix(path, ".go")) {
			return nil
		}
		rel, _ := filepath.Rel(real, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		write(t, dir, filepath.ToSlash(rel), string(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for rel, body := range files {
		write(t, dir, rel, body)
	}
	t.Cleanup(libroot.SetForTest(dir))
}

func titles(errs []diag.Error) string {
	var out []string
	for _, err := range errs {
		out = append(out, err.Title+": "+err.Body)
	}
	return strings.Join(out, "\n")
}

func TestLibraryResolution(t *testing.T) {
	libraryRoot(t, map[string]string{
		"libs/demo/Demo.fango":       "module Demo exposing (greeting)\nimport Demo.Parts\ngreeting = Demo.Parts.word\n",
		"libs/demo/Demo/Parts.fango": "module Demo.Parts exposing (word)\nword = \"hi\"\n",
		"libs/demo/tests/Main.fango": "import Demo\nmain() = print Demo.greeting\n",
		"libs/other/Demo.fango":      "module Demo exposing ()\n",
		"libs/shadow/List.fango":     "module List exposing ()\n",
		"libs/Upper/Nope.fango":      "module Nope exposing ()\n",
	})
	if names, err := libroot.Libraries(); err != nil || strings.Join(names, ",") != "demo,other,shadow" {
		t.Fatalf("libraries = %v, %v", names, err)
	}
	if names, err := (LibraryProvider{Name: "demo"}).Modules(); err != nil || strings.Join(names, ",") != "Demo,Demo.Parts" {
		t.Fatalf("demo modules = %v, %v", names, err)
	}

	t.Run("enabled", func(t *testing.T) {
		entry := write(t, t.TempDir(), "main.fango", "import Demo\nmain() = print Demo.greeting\n")
		result, errs := LoadWithOptions(entry, LoadOptions{Libraries: []string{"demo"}})
		if len(errs) > 0 {
			t.Fatal(titles(errs))
		}
		paths := map[string]string{}
		for _, m := range result.Manifest {
			paths[m.Module] = m.Path
		}
		if paths["Demo"] != "<libs>/demo/Demo.fango" || paths["Demo.Parts"] != "<libs>/demo/Demo/Parts.fango" {
			t.Fatalf("manifest paths: %v", paths)
		}
		physical, ok := PhysicalPath(paths["Demo.Parts"])
		if !ok {
			t.Fatal("no physical path")
		}
		if logical, ok := LogicalPath(physical); !ok || logical != paths["Demo.Parts"] {
			t.Fatalf("logical path of %s = %q, %v", physical, logical, ok)
		}
	})

	for _, tc := range []struct {
		name, title, body string
		libraries         []string
		local             map[string]string
	}{
		{name: "disabled", title: "MISSING MODULE", body: "enable it with `--lib demo`"},
		{name: "unknown", title: "MISSING LIBRARY", body: "Available libraries: demo, other, shadow.", libraries: []string{"nope"}},
		{name: "local collision", title: "RESERVED MODULE", body: "is provided by the library `demo`", libraries: []string{"demo"}, local: map[string]string{"Demo.fango": "module Demo exposing ()\n"}},
		{name: "conflict", title: "LIBRARY CONFLICT", body: "both enabled libraries `demo` and `other`", libraries: []string{"demo", "other"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for rel, body := range tc.local {
				write(t, dir, rel, body)
			}
			entry := write(t, dir, "main.fango", "import Demo\nmain() = print Demo.greeting\n")
			_, errs := LoadWithOptions(entry, LoadOptions{Libraries: tc.libraries})
			if len(errs) == 0 || errs[0].Title != tc.title || !strings.Contains(errs[0].Body, tc.body) {
				t.Fatalf("want %s containing %q, got:\n%s", tc.title, tc.body, titles(errs))
			}
		})
	}

	t.Run("stdlib shadow", func(t *testing.T) {
		entry := write(t, t.TempDir(), "main.fango", "import List\nmain() = print 1\n")
		_, errs := LoadWithOptions(entry, LoadOptions{Libraries: []string{"shadow"}})
		if len(errs) == 0 || errs[0].Title != "RESERVED MODULE" || !strings.Contains(errs[0].Body, "the library `shadow` cannot also provide it") {
			t.Fatalf("got:\n%s", titles(errs))
		}
	})

	t.Run("entry name", func(t *testing.T) {
		entry := write(t, t.TempDir(), "Demo.fango", "module Demo exposing (main)\nmain() = print 1\n")
		_, errs := LoadWithOptions(entry, LoadOptions{Libraries: []string{"demo"}})
		if len(errs) == 0 || errs[0].Title != "RESERVED MODULE" {
			t.Fatalf("got:\n%s", titles(errs))
		}
	})

	t.Run("editor opens library file", func(t *testing.T) {
		root, _ := libroot.Root()
		entry := filepath.Join(root, "libs", "demo", "Demo", "Parts.fango")
		result, errs := LoadWithOptions(entry, LoadOptions{Root: filepath.Join(root, "libs", "demo"), Libraries: []string{"demo"}, AllowBundledEntry: true})
		if len(errs) > 0 {
			t.Fatal(titles(errs))
		}
		if last := result.Manifest[len(result.Manifest)-1]; last.Path != "<libs>/demo/Demo/Parts.fango" {
			t.Fatalf("entry path = %s", last.Path)
		}
	})

	t.Run("library test entry", func(t *testing.T) {
		root, _ := libroot.Root()
		if _, errs := LoadWithOptions(filepath.Join(root, "libs", "demo", "tests", "Main.fango"), LoadOptions{Libraries: []string{"demo"}}); len(errs) > 0 {
			t.Fatal(titles(errs))
		}
	})

	t.Run("session graph", func(t *testing.T) {
		g, _, errs := NewGraph(t.TempDir(), []string{"demo"})
		if len(errs) > 0 {
			t.Fatal(titles(errs))
		}
		if !strings.Contains(strings.Join(g.Modules(), ","), "Demo.Parts") {
			t.Fatal("session graph does not list library modules")
		}
	})
}
