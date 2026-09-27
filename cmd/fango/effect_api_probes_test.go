package main

import (
	"path/filepath"
	"testing"
)

// These programs exercise scoped memory readers and domain effects in both
// synchronous backends. The specialized task probe is not a generic Async API.
func TestEffectAPIProbes(t *testing.T) {
	for _, name := range []string{"effect_api_memory_reader", "reader_pure_operations", "reader_scoped_memory", "reader_scoped_domain", "task_domain_setup"} {
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

// The generic reader record is effect-polymorphic, but its existing native
// reference-backed constructors must not acquire a pure type by adaptation.
func TestReaderConstructorsRetainIO(t *testing.T) {
	path := writeModuleFile(t, t.TempDir(), "Main.fango", `import Bytes
import Reader

parse : Bytes.Bytes -> Bytes.Bytes
parse bytes = Reader.overBytes bytes (\reader -> Reader.readUpTo reader 1)

main = parse Bytes.empty
`)
	runErrorCase(t, path, "EFFECT MISMATCH")
}
