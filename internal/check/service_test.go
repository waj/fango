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
		{"local protocol", "{-# service #-}\neffect Bad\n    send : a ->{Service.Invocation a ()} ()\nmain() = ()\n", "SERVICE PROTOCOL"},
		{"different protocols", "{-# service #-}\neffect Bad\n    first : Int ->{Service.Invocation Int ()} ()\n    second : String ->{Service.Invocation String ()} ()\nmain() = ()\n", "SERVICE PROTOCOL"},
		{"forged slot", "main() = handle Service.invoke 1 of\n    Service.invoke value -> resume ()\n", "INVOCATION AUTHORITY"},
		{"arbitrary callback", "producer pause () = Service.run (\\_ -> ()) (\\_ -> Service.invoke 1)\nmain() = Coroutine.with producer (\\cursor ->\n    ignored = Coroutine.advance cursor ()\n    ())\n", "INVOCATION AUTHORITY"},
		{"missing slot", "main() = Service.invoke 1\n", "EFFECT"},
		{"retained slot", "type Saved = Saved (() -> ())\n{-# service #-}\neffect Bad\n    leak : () ->{Service.Invocation Int ()} Saved\nmain() = handle () of\n    leak () -> resume (Saved (\\_ -> Service.invoke 1))\n", "EFFECT"},
		{"wrong protocol", "producer pause () = Service.run pause (\\_ -> Service.invoke 1)\nmain() = Coroutine.with producer (\\cursor -> case Coroutine.advance cursor () of\n    Coroutine.Suspended value -> print (value ++ \"bad\")\n    _ -> ())\n", "MISSING INSTANCE"},
		{"mutable context", "{-# service #-}\neffect Bad\n    send : Int ->{Service.Invocation Int ()} ()\nmain() = handle () with state = 0 of\n    send value -> resume () with value\n", "SERVICE STATE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "Main.fango")
			if err := os.WriteFile(path, []byte("import Coroutine\nimport Service\nimport Work\n"+tc.source), 0600); err != nil {
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
	source := `import Coroutine
import Service
import Work
parent pause () = Service.run pause (\_ -> Coroutine.scope (\scope ->
    child = Work.register (Coroutine.facet scope) (\_ () -> Service.invoke 1)
    ignored = Work.advance (Work.owner scope) child ()
    ()))
main() = Coroutine.with parent (\cursor ->
    ignored = Coroutine.advance cursor ()
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
