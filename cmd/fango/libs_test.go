package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// libraryCases pins each optional library's own regression cases, as
// TestSuite pins the standard suite's: silently dropping one must not leave
// the gate green. Documentation cases are counted when they are generated.
var libraryCases = map[string]int{"web": 27}

// TestLibraries checks every optional library of libs/ with it enabled: its
// API documentation is complete, its suite and documentation examples pass
// under the selected backends, and each generator diagnostic fixture fails
// with its message.
func TestLibraries(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "libs")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		name := entry.Name()
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			testLibrary(t, name, filepath.Join(root, name, "tests"))
		})
	}
}

func testLibrary(t *testing.T, name, tests string) {
	cases, ok := libraryCases[name]
	if !ok {
		t.Fatalf("libraryCases has no count for the library %s", name)
	}
	stdout, stderr, code := runDoc(t, "--lib", name, "--strict")
	if code != 0 || stderr != "" {
		t.Fatalf("doc --lib %s --strict: code %d\n%s", name, code, stderr)
	}
	doc := decodeDoc(t, stdout)
	var texts []string
	for _, module := range doc.Modules {
		texts = append(texts, module.Documentation)
		for _, declaration := range module.Declarations {
			if declaration.TargetID == "" {
				texts = append(texts, declaration.Documentation)
			}
		}
	}
	examples, _, exampleCases := exampleTestModule(texts)

	dir := t.TempDir()
	err := filepath.WalkDir(tests, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(tests, path)
		if entry.IsDir() {
			if rel == "errors" || strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dir, rel), 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, rel), data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := os.ReadFile(filepath.Join(dir, "Main.fango"))
	if err != nil {
		t.Fatal(err)
	}
	combined := string(entry)
	if exampleCases > 0 {
		if err := os.WriteFile(filepath.Join(dir, "LibraryExamples.fango"), []byte(examples), 0o644); err != nil {
			t.Fatal(err)
		}
		// The suite's list of tests is the entry's first list literal.
		combined = strings.Replace(combined, "import Test\n", "import LibraryExamples\nimport Test\n", 1)
		combined = strings.Replace(combined, "[", "[LibraryExamples.tests, ", 1)
		if !strings.Contains(combined, "[LibraryExamples.tests, ") || !strings.Contains(combined, "import LibraryExamples\n") {
			t.Fatal("tests/Main.fango must import Test and list its tests in its first list literal")
		}
	}
	path := filepath.Join(dir, "Main.fango")
	if err := os.WriteFile(path, []byte(combined), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d regression cases and %d documentation cases", cases, exampleCases)
	expected := fmt.Sprintf("TEST RUN PASSED\n\nPassed:  %d\nFailed:  0\nSkipped: 0\nTodo:    0\n", cases+exampleCases)
	session := &compilationSession{libraries: []string{name}}
	t.Run("suite", func(t *testing.T) {
		runDifferentialCaseIn(t, path, session, cliRunner(path, name), fixtureInputs{}, expected)
	})

	fixtures, _ := filepath.Glob(filepath.Join(tests, "errors", "*.fango"))
	for _, fixture := range fixtures {
		if strings.HasPrefix(filepath.Base(fixture), ".") {
			continue // a build directory a manual check left behind
		}
		t.Run(strings.TrimSuffix(filepath.Base(fixture), ".fango"), func(t *testing.T) {
			want, err := os.ReadFile(strings.TrimSuffix(fixture, ".fango") + ".error")
			if err != nil {
				t.Fatal(err)
			}
			var diagnostics bytes.Buffer
			if _, _, _, _, _, ok := compileFileGraphSession(fixture, &diagnostics, session); ok {
				t.Fatalf("expected a compile error containing %q", strings.TrimSpace(string(want)))
			}
			if !strings.Contains(diagnostics.String(), strings.TrimSpace(string(want))) {
				t.Errorf("diagnostics missing %q:\n%s", strings.TrimSpace(string(want)), diagnostics.String())
			}
		})
	}
}

// The library tier from the command line: a program that imports a library
// module builds with --lib and is told which option it is missing without it.
func TestLibraryOption(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "main.fango")
	source := "import Web\n\nmain() = print (show (Web.parseTemplate \"/a/:b\"))\n"
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if code := run([]string{"check", path}, &bytes.Buffer{}, &stderr); code == 0 || !strings.Contains(stderr.String(), "enable it with `--lib web`") {
		t.Fatalf("check without --lib: code %d\n%s", code, stderr.String())
	}
	stderr.Reset()
	if code := run([]string{"check", "--lib", "web", path}, &bytes.Buffer{}, &stderr); code != 0 {
		t.Fatalf("check --lib web: code %d\n%s", code, stderr.String())
	}
	stderr.Reset()
	if code := run([]string{"check", "--lib", "nope", path}, &bytes.Buffer{}, &stderr); code == 0 || !strings.Contains(stderr.String(), "MISSING LIBRARY") {
		t.Fatalf("check --lib nope: code %d\n%s", code, stderr.String())
	}
	stderr.Reset()
	if _, errOut, code := runDoc(t, "--stdlib", "--lib", "web"); code != 2 || !strings.Contains(errOut, "exactly one of --stdlib or --lib") {
		t.Fatalf("doc --stdlib --lib: code %d\n%s", code, errOut)
	}
}
