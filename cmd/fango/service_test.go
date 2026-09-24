package main

import (
	"path/filepath"
	"testing"
)

func TestSharedServiceModuleDifferential(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "modules", "shared_services", "Main.fango")
	runDifferentialCase(t, path, cliRunner(path))
}
