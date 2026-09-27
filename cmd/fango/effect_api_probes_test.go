package main

import (
	"path/filepath"
	"testing"
)

// These programs exercise the existing synchronous runtime. Their specialized
// effects do not stand in for scope polymorphism or a generic scheduling API.
func TestEffectAPIProbes(t *testing.T) {
	for _, name := range []string{"effect_api_memory_reader", "task_domain_setup"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("..", "..", "testdata", "run", name+".fango")
			runDifferentialCase(t, path, cliRunner(path))
		})
	}
}

func TestTaskRequiresChildInterpretation(t *testing.T) {
	path := writeModuleFile(t, t.TempDir(), "Main.fango", `import Task

effect Database
    lookup : Int -> Int

worker context number = lookup number

main() = Task.scope (\scope ->
    handle ignore (Task.spawn scope worker 1) of
        lookup number -> resume number)
`)
	runErrorCase(t, path, "EFFECT MISMATCH")
}
