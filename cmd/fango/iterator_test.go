package main

import (
	"path/filepath"
	"testing"
)

func TestIndependentCursorConsumers(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "modules", "iterator_consumers", "Main.fango")
	runDifferentialCase(t, path, cliRunner(path))
}
