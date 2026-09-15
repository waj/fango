package main

import (
	"bytes"
	"path/filepath"
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
		{"Direct", "", `print (Callbacks.call (Callbacks.make (\_ -> 7)))`, "7\n"},
		{"Exit", "import Fail\n", `print (Fail.attempt (\_ -> Callbacks.call (Callbacks.make (\_ -> if True then Fail.fail "failed" else 7))))`, "Err failed\n"},
		{"Machine", "import Stream\n", `print (Stream.toList (Stream.generate (\_ -> Callbacks.call (Callbacks.make (\_ -> Stream.yield 7)))))`, "[7]\n"},
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

func TestYieldedResourcesRespectProducerLifetime(t *testing.T) {
	const prelude = `import Stream
import Iterator
import Scope
import Maybe exposing (Maybe(..))
{-# resource #-}
type Handle = Handle Int
type Box a = Box a
withHandle action = Scope.bracket (\_ -> Handle 7) (\_ -> ()) action
read (Handle value) = value
identity value = value
`
	for _, test := range []struct{ name, body string }{
		{"alias", `source() = Stream.generate (\_ -> withHandle (\resource -> Stream.yield (identity resource)))`},
		{"adt", `source() = Stream.generate (\_ -> withHandle (\resource -> Stream.yield (Just (Box resource))))`},
		{"closure", `source() = Stream.generate (\_ -> withHandle (\resource -> Stream.yield (\_ -> read resource)))`},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			entry := writeModuleFile(t, root, "Main.fango", prelude+test.body+"\nmain() = Stream.forEach (\\_ -> ()) (source())\n")
			runErrorCase(t, entry, "RESOURCE ESCAPES")
		})
	}
	root := t.TempDir()
	entry := writeModuleFile(t, root, "Main.fango", prelude+`main() = withHandle (\resource ->
    source = Stream.generate (\_ -> Stream.yield (Box (\_ -> read resource)))
    Stream.forEach (\(Box action) -> print (action())) source)
`)
	writeModuleFile(t, root, "Main.expected", "7\n")
	runDifferentialCase(t, entry, cliRunner(filepath.Clean(entry)))
}
