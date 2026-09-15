package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIndependentCursorConsumers(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "modules", "iterator_consumers", "Main.fango")
	runDifferentialCase(t, path, cliRunner(path))
}

func TestImportedCursorContractRejectsEscape(t *testing.T) {
	dir := t.TempDir()
	consumers, err := os.ReadFile(filepath.Join("..", "..", "testdata", "modules", "iterator_consumers", "Consumers.fango"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Consumers.fango"), consumers, 0600); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(dir, "Main.fango")
	if err := os.WriteFile(entry, []byte(`import Consumers
import Generator

escape() = Generator.withIterator (\_ -> Generator.yield 1) Consumers.escape

main() = print "unreachable"
`), 0600); err != nil {
		t.Fatal(err)
	}
	runErrorCase(t, entry, "RESOURCE ESCAPES")
}
