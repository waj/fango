package main

import (
	"path/filepath"
	"testing"
)

func TestIndependentPullAbstraction(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "modules", "pull", "Main.fango")
	runDifferentialCase(t, path, cliRunner(path))
}

func TestPrivateFailurePayloads(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "modules", "failure_reports", "Main.fango")
	runDifferentialCase(t, path, cliRunner(path))
}
