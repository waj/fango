package main

import (
	"bytes"
	"testing"
)

func TestStreamCallbackModuleIsConsumerIndependent(t *testing.T) {
	root := t.TempDir()
	writeModuleFile(t, root, "Callbacks.fango", `module Callbacks exposing (Box, make, call)
type Box a e = Box (() ->{e} a)
make : (() ->{e} a) -> Box a e
make action = Box action
call : Box a e ->{e} a
call (Box action) = action()
`)
	var original []byte
	for _, test := range []struct{ name, imports, body, output string }{
		{"Direct", "", `print (Callbacks.call (Callbacks.make { _ -> 7 }))`, "7\n"},
		{"Exit", "import Fail\n", `print (Fail.attempt { _ -> Callbacks.call (Callbacks.make { _ -> if True then Fail.fail "failed" else 7 }) })`, "Err failed\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry := writeModuleFile(t, root, "Main.fango", "import Callbacks\n"+test.imports+"main() = "+test.body+"\n")
			got := generatedFile(t, emittedProject(t, entry), "modules/Callbacks/module.go")
			if original == nil {
				original = got
			} else if !bytes.Equal(original, got) {
				t.Fatalf("%s consumer changed dependency output", test.name)
			}
			writeModuleFile(t, root, "Main.expected", test.output)
			runDifferentialCase(t, entry, cliRunner(entry))
		})
	}
}
