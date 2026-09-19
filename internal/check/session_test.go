package check

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDependencyStateIsConsumerIndependent(t *testing.T) {
	d := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(d, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("Lib.fango", "{-# no-prelude #-}\nmodule Lib exposing (id, main)\nid x = x\nmain x y = x\n")
	one := write("One.fango", "{-# no-prelude #-}\nmodule One exposing (main)\nimport Lib\nmain = Lib.main (Lib.id ()) ()\n")
	two := write("Two.fango", "{-# no-prelude #-}\nmodule Two exposing (main)\nimport Lib\nmain = Lib.main () (Lib.id ())\n")
	compile := func(entry string) *Result {
		r, diagnostics, internalErr := (&Session{}).Compile(entry)
		if internalErr != nil {
			t.Fatal(internalErr)
		}
		if len(diagnostics) != 0 {
			t.Fatal(diagnostics)
		}
		return r
	}
	find := func(r *Result) any {
		for _, state := range r.States {
			if state.Name == "Lib" {
				if state.Schemes["Lib.id"].Body == nil || state.Workers["Lib.id"] != 1 {
					t.Fatalf("incomplete Lib state: %#v", state)
				}
				if _, ok := state.Captures["Lib.id"]; !ok {
					t.Fatalf("missing capture summary: %#v", state.Captures)
				}
				return state
			}
		}
		t.Fatal("Lib state missing")
		return nil
	}
	if a, b := find(compile(one)), find(compile(two)); !reflect.DeepEqual(a, b) {
		t.Fatal("dependency state changed between consumers")
	}
}
