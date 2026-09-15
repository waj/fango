package main

import (
	"path/filepath"
	"testing"
)

func TestStreamIntegration(t *testing.T) {
	for _, name := range []string{"stream_operations", "stream_demand", "stream_cleanup", "stream_file", "stream_parser", "stream_stored_callbacks"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("..", "..", "testdata", "run", name+".fango")
			runDifferentialCase(t, path, cliRunner(path))
		})
	}
}

func TestMachineStoredEffectfulResult(t *testing.T) {
	for _, name := range []string{"state_independent_result", "scope_state", "state_handlers"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("..", "..", "testdata", "run", name+".fango")
			runDifferentialCase(t, path, cliRunner(path))
		})
	}
}
