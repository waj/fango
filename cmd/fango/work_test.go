package main

import (
	"path/filepath"
	"testing"
)

func TestWorkModuleDifferential(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "modules", "work_packages", "Main.fango")
	runDifferentialCase(t, path, cliRunner(path))
}
