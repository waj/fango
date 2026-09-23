package main

import (
	"path/filepath"
	"testing"
)

func TestCompletionModuleDifferential(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "modules", "completion_adapters", "Main.fango")
	runDifferentialCase(t, path, cliRunner(path))
}
