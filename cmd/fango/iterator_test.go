package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIndependentCursorConsumers(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "modules", "iterator_consumers", "Main.fango")
	runDifferentialCase(t, path, cliRunner(path))
}

func TestImportedCursorContractRejectsEscape(t *testing.T) {
	dir := t.TempDir()
	consumers, err := os.ReadFile(filepath.Join("..", "..", "testdata", "modules", "iterator_consumers", "Consumers.fango"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Consumers.fango"), consumers, 0600); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(dir, "Main.fango")
	if err := os.WriteFile(entry, []byte(`import Consumers
import Stream

escape() = Stream.withCursor (Stream.generate (\_ -> Stream.yield 1)) Consumers.escape

main() = print "unreachable"
`), 0600); err != nil {
		t.Fatal(err)
	}
	runErrorCase(t, entry, "RESOURCE ESCAPES")
}

func TestImportedScopeContractRejectsSuspendingCallbacks(t *testing.T) {
	dir := t.TempDir()
	wrapper := `module Resource exposing (withResource)
import Scope

withResource acquire release use = Scope.bracket acquire release use
`
	if err := os.WriteFile(filepath.Join(dir, "Resource.fango"), []byte(wrapper), 0600); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"acquire", "release"} {
		t.Run(phase, func(t *testing.T) {
			acquire, release := "quiet", "quiet"
			if phase == "acquire" {
				acquire = "pause"
			} else {
				release = "pause"
			}
			entry := filepath.Join(dir, "Main.fango")
			program := `import Resource
import Stream
import Iterator

quiet() = ()
pause() = Stream.yield 1

main() = Stream.forEach print (Stream.generate (\_ -> Resource.withResource ` + acquire + ` ` + release + ` quiet))
`
			if err := os.WriteFile(entry, []byte(program), 0600); err != nil {
				t.Fatal(err)
			}
			runErrorCase(t, entry, "SUSPENDING RESOURCE CALLBACK")
		})
	}
}
