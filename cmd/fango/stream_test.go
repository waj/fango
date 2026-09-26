package main

import (
	"path/filepath"
	"testing"
)

func TestStreamIntegration(t *testing.T) {
	for _, name := range []string{"stream_operations", "stream_demand", "stream_cleanup", "stream_file", "stream_parser", "stream_stored_callbacks", "stream_evidence_shadow", "stream_recovery", "stream_handler", "failure_reports"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("..", "..", "testdata", "run", name+".fango")
			runDifferentialCase(t, path, batchRunner(path))
		})
	}
}

func TestIndependentPullAbstraction(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "modules", "pull", "Main.fango")
	runDifferentialCase(t, path, cliRunner(path))
}

func TestFailureReportCapture(t *testing.T) {
	for _, name := range []string{"failure_report_borrow"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("..", "..", "testdata", "run", name+".fango")
			runDifferentialCase(t, path, batchRunner(path))
		})
	}
}

func TestResidualCallbackRepresentations(t *testing.T) {
	for _, name := range []string{"classes_locals", "iterator_helpers", "list_deep", "row_kind_adt", "scope_owned_traversal", "state_independent_result", "stdlib_words_foldl", "stream_staging", "user_operators"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("..", "..", "testdata", "run", name+".fango")
			runDifferentialCase(t, path, batchRunner(path))
		})
	}
}

func TestPrivateFailurePayloads(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "modules", "failure_reports", "Main.fango")
	runDifferentialCase(t, path, cliRunner(path))
}

func TestStoredEffectfulResult(t *testing.T) {
	for _, name := range []string{"state_independent_result", "scope_state", "state_handlers"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("..", "..", "testdata", "run", name+".fango")
			runDifferentialCase(t, path, batchRunner(path))
		})
	}
}
