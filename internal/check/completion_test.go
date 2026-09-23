package check

import (
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/execcodec"
	"github.com/waj/fango/internal/machine"
	"github.com/waj/fango/internal/types"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompletionContractsSurviveModuleAndExecutionCodecs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Main.fango")
	if err := os.WriteFile(path, []byte("import Completion\nmain = Completion.replay (Completion.capture (\\_ -> 42))\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cache := newMemoryObjectCache()
	compileEvents(t, path, cache)
	result, events := compileEvents(t, path, cache)
	if events["checked-cache-hit"]["Completion"] != 1 {
		t.Fatalf("completion module not restored: %#v", events)
	}
	lowered, errs := machine.Lower(result.Program, result.Checker.B)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	encoded, err := execcodec.Encode(&execcodec.Payload{Program: result.Program, Machine: lowered})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := execcodec.Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if errs := core.Lint(decoded.Program, result.Checker.B); len(errs) != 0 {
		t.Fatal(errs)
	}
	if errs := machine.Lint(decoded.Machine); len(errs) != 0 {
		t.Fatal(errs)
	}
	var node *core.Completion
	for i := range decoded.Program.Defs {
		d := &decoded.Program.Defs[i]
		if d.Name == types.CompletionReplayName {
			node = d.Body.(*core.Completion)
		}
	}
	if node == nil {
		t.Fatal("completion intrinsic missing after round trip")
	}
	node.Row = nil
	var text strings.Builder
	for _, err := range core.Lint(decoded.Program, result.Checker.B) {
		text.WriteString(err.Error())
	}
	if !strings.Contains(text.String(), "residual row argument") {
		t.Fatalf("missing replay proof accepted: %s", text.String())
	}
}
