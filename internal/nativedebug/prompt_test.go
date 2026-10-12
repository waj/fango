package nativedebug

import (
	"bytes"
	"strings"
	"testing"

	"github.com/waj/fango/internal/debugger"
)

func TestSharedPromptInspection(t *testing.T) {
	rpc := &inspectionRPC{args: []variable{{Name: "v_x", Type: "int64", Value: "20"}}}
	c := &Controller{RPC: rpc, Manifest: &Manifest{Scopes: []debugger.NativeScope{{File: "Main.fango", Line: 1, Bindings: []debugger.Binding{{Name: "x", Runtime: "v_x", Type: "Int"}}}}}, State: State{CurrentThread: &Thread{Location: Location{File: "Main.fango", Line: 1}}}}
	var out bytes.Buffer
	err := c.Prompt(strings.NewReader(":print\tx\nprint x + 1\n\n:frame 0\n:expand 1\n:locals extra\n:q\n"), &out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "x : Int = 20") != 2 || len(rpc.evaluated) != 2 {
		t.Fatalf("%s; reads %v", &out, rpc.evaluated)
	}
	for _, want := range []string{"debugger: usage: print NAME (expressions are not evaluated)", "invalid or expired value handle", "locals takes no arguments while stopped"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q:\n%s", want, &out)
		}
	}
}

func TestSharedPromptResumeAliasesAndRepeat(t *testing.T) {
	c, rpc := controller(stop{frames: []Frame{frame("main.V_run", 10, 2)}}, stop{frames: []Frame{frame("main.V_run", 10, 3)}})
	var out bytes.Buffer
	if err := c.Prompt(strings.NewReader(":n\nnext extra\n\n:q\n"), &out); err != nil {
		t.Fatal(err)
	}
	if strings.Join(rpc.commands, ",") != "next,next" {
		t.Fatalf("unexpected execution: %v\n%s", rpc.commands, &out)
	}
}
