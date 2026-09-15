package main

import (
	"path/filepath"
	"testing"
)

func TestStreamIntegration(t *testing.T) {
	for _, name := range []string{"stream_operations", "stream_demand", "stream_cleanup", "stream_file", "stream_parser", "stream_stored_callbacks", "failure_reports"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("..", "..", "testdata", "run", name+".fango")
			runDifferentialCase(t, path, cliRunner(path))
		})
	}
}

func TestFailureReportCapture(t *testing.T) {
	for _, name := range []string{"err_failure_report_resource_escape", "err_failure_report_stored_escape", "failure_report_borrow"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("..", "..", "testdata", "run", name+".fango")
			runDifferentialCase(t, path, cliRunner(path))
		})
	}
}

func TestPrivateFailurePayloads(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "modules", "failure_reports", "Main.fango")
	runDifferentialCase(t, path, cliRunner(path))
}

func TestMachineStoredEffectfulResult(t *testing.T) {
	for _, name := range []string{"state_independent_result", "scope_state", "state_handlers"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("..", "..", "testdata", "run", name+".fango")
			runDifferentialCase(t, path, cliRunner(path))
		})
	}
}
