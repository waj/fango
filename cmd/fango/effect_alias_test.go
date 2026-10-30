package main

import (
	"path/filepath"
	"testing"
)

func TestEffectAliasAcrossModules(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "modules", "effect_alias", "Main.fango")
	runDifferentialCase(t, path, cliRunner(path))
}
