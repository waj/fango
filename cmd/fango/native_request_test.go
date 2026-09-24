package main

import (
	"path/filepath"
	"testing"
)

func TestNativeRequestModuleDifferential(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "modules", "native_requests", "Main.fango")
	runDifferentialCase(t, path, cliRunner(path))
}
