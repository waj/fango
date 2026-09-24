package check

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServiceContracts(t *testing.T) {
	for _, tc := range []struct{ name, source, want string }{
		{"missing protocol", "{-# service #-}\neffect Bad\n    send : Int -> ()\nmain() = ()\n", "SERVICE PROTOCOL"},
		{"local protocol", "{-# service #-}\neffect Bad\n    send : a ->{Runtime.Service.Invocation a ()} ()\nmain() = ()\n", "SERVICE PROTOCOL"},
		{"different protocols", "{-# service #-}\neffect Bad\n    first : Int ->{Runtime.Service.Invocation Int ()} ()\n    second : String ->{Runtime.Service.Invocation String ()} ()\nmain() = ()\n", "SERVICE PROTOCOL"},
		{"forged slot", "main() = handle Runtime.Service.invoke 1 of\n    Runtime.Service.invoke value -> resume ()\n", "INVOCATION AUTHORITY"},
		{"arbitrary callback", "producer pause () = Runtime.Service.run (\\_ -> ()) (\\_ -> Runtime.Service.invoke 1)\nmain() = Runtime.Coroutine.with producer (\\cursor ->\n    ignored = Runtime.Coroutine.advance cursor ()\n    ())\n", "INVOCATION AUTHORITY"},
		{"missing slot", "main() = Runtime.Service.invoke 1\n", "EFFECT"},
		{"retained slot", "type Saved = Saved (() -> ())\n{-# service #-}\neffect Bad\n    leak : () ->{Runtime.Service.Invocation Int ()} Saved\nmain() = handle () of\n    leak () -> resume (Saved (\\_ -> Runtime.Service.invoke 1))\n", "EFFECT"},
		{"wrong protocol", "producer pause () = Runtime.Service.run pause (\\_ -> Runtime.Service.invoke 1)\nmain() = Runtime.Coroutine.with producer (\\cursor -> case Runtime.Coroutine.advance cursor () of\n    Runtime.Coroutine.Suspended value -> print (value ++ \"bad\")\n    _ -> ())\n", "MISSING INSTANCE"},
		{"mutable context", "{-# service #-}\neffect Bad\n    send : Int ->{Runtime.Service.Invocation Int ()} ()\nmain() = handle () with state = 0 of\n    send value -> resume () with value\n", "SERVICE STATE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "Main.fango")
			if err := os.WriteFile(path, []byte("import Runtime.Coroutine\nimport Runtime.Service\nimport Runtime.Work\n"+tc.source), 0600); err != nil {
				t.Fatal(err)
			}
			_, diagnostics, err := (&Session{}).Compile(path)
			if err != nil {
				t.Fatal(err)
			}
			var messages []string
			for _, d := range diagnostics {
				messages = append(messages, d.Title+" "+d.Body)
			}
			got := strings.Join(messages, "\n")
			if !strings.Contains(got, tc.want) {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestServiceSlotCannotTransferToIndependentWork(t *testing.T) {
	source := `import Runtime.Coroutine
import Runtime.Service
import Runtime.Work
parent pause () = Runtime.Service.run pause (\_ -> Runtime.Coroutine.scope (\scope ->
    child = Runtime.Work.register (Runtime.Coroutine.facet scope) (\_ () -> Runtime.Service.invoke 1)
    ignored = Runtime.Work.advance (Runtime.Work.owner scope) child ()
    ()))
main() = Runtime.Coroutine.with parent (\cursor ->
    ignored = Runtime.Coroutine.advance cursor ()
    ())
`
	path := filepath.Join(t.TempDir(), "Main.fango")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	_, ds, err := (&Session{}).Compile(path)
	if err != nil {
		t.Fatal(err)
	}
	var messages []string
	for _, d := range ds {
		messages = append(messages, d.Title+" "+d.Body)
	}
	if got := strings.Join(messages, "\n"); !strings.Contains(got, "TRANSFER") {
		t.Fatalf("expected invocation transfer rejection, got %s", got)
	}
}
