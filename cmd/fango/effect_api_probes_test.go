package main

import (
	"bytes"
	"testing"
)

// Scoped memory constructors discharge their local cursor permission.
func TestReaderConstructorsArePure(t *testing.T) {
	path := writeModuleFile(t, t.TempDir(), "Main.fango", `import Bytes
import Reader

parse : Bytes.Bytes -> Bytes.Bytes
parse bytes = Reader.overBytes bytes ({ reader -> Reader.readUpTo reader 1 })

main() = print (parse Bytes.empty)
`)
	var out, errs bytes.Buffer
	if code := run([]string{"check", path}, &out, &errs); code != 0 {
		t.Fatal(errs.String())
	}
}
