package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The language/library checks and documentation examples share one Fango
// graph and one compiled executable. Differential mode also executes Core.
// The remaining run fixtures exercise diagnostics, entry contracts, isolated
// process inputs, or source shapes consumed by the structural tests.
func TestSuite(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "testdata", "suite")
	dir := t.TempDir()
	files, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := os.ReadFile(filepath.Join(root, "Main.fango"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		name := file.Name()
		if file.IsDir() || !(strings.HasSuffix(name, ".fango") || strings.HasSuffix(name, ".native.go")) {
			continue
		}
		if strings.HasSuffix(name, ".fango") && name != "Main.fango" {
			module := strings.TrimSuffix(name, ".fango")
			if !strings.Contains(string(entry), "\nimport "+module+"\n") {
				t.Fatalf("%s is missing from Main.fango's suite", name)
			}
		}
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	stdout, stderr, code := runDoc(t, "--stdlib")
	if code != 0 {
		t.Fatalf("doc: %s", stderr)
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
	examples, assertions, cases := exampleTestModule(texts)
	if assertions < 10 {
		t.Fatalf("only %d documentation assertions", assertions)
	}
	if err := os.WriteFile(filepath.Join(dir, "LibraryExamples.fango"), []byte(examples), 0o644); err != nil {
		t.Fatal(err)
	}
	combined := strings.Replace(string(entry), "import Test\n", "import Test\nimport LibraryExamples\n", 1)
	combined = strings.Replace(combined, "[ ", "[ LibraryExamples.tests\n            , ", 1)
	path := filepath.Join(dir, "Main.fango")
	if err := os.WriteFile(path, []byte(combined), 0o644); err != nil {
		t.Fatal(err)
	}
	// Pin the checked-in suite's case count too: silently dropping a case
	// must not leave the gate green. Documentation cases are generated above.
	expected := fmt.Sprintf("TEST RUN PASSED\n\nPassed:  %d\nFailed:  0\nSkipped: 0\nTodo:    0\n", 397+cases)
	t.Logf("397 regression cases and %d documentation cases (%d assertions) in one executable", cases, assertions)
	t.Logf("execution backends: %s", strings.Join(executionTestBackends(), ", "))
	runDifferentialCaseWith(t, path, cliRunner(path), fixtureInputs{}, expected)
}
