package main

import (
	"path/filepath"
	"testing"
)

func TestTaskIntegration(t *testing.T) {
	for _, name := range []string{"task_transfer", "task_named_workers", "task_cancellation", "stream_explicit_state"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("..", "..", "testdata", "run", name+".fango")
			runDifferentialCase(t, path, cliRunner(path))
		})
	}
}

func TestTaskBoundaryDiagnostics(t *testing.T) {
	for _, test := range []struct{ name, declarations, worker, input string }{
		{"scope", "worker context scope = 1", "worker", "scope"},
		{"partial worker", "worker a context input = input", "worker 1", "1"},
		{"closure", "", `\context value -> value`, "1"},
		{"function input", "worker context input = input()", "worker", `(\_ -> 1)`},
		{"function result", "worker context input = \\_ -> input", "worker", "1"},
		{"reference", "worker context ref = Runtime.Ref.read ref", "worker", "(Runtime.Ref.new 1)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := writeModuleFile(t, t.TempDir(), "Main.fango", "import Task\nimport Runtime.Ref\n"+test.declarations+"\nmain() = Task.scope (\\scope ->\n    ignore (Task.spawn scope ("+test.worker+") "+test.input+"))\n")
			runErrorCase(t, path, "TASK BOUNDARY")
		})
	}
}
