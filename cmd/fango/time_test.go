package main

import (
	"path/filepath"
	"testing"
)

func TestTimeBillingExample(t *testing.T) {
	t.Parallel()
	path := filepath.Join("..", "..", "examples", "billing.fango")
	runDifferentialCase(t, path, cliRunner(path))
}
