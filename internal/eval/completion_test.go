package eval

import (
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
	"testing"
)

func TestOwnerStopBypassesInterpreterCompletion(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	p := &core.Prog{Defs: []core.Def{{Name: "producer", Type: b.Unit, Control: types.Control{Transport: types.Machine}, Body: &core.Suspend{Request: &core.IntLit{Val: 1, Ty: b.Int}, Ty: b.Unit}}}}
	program := lowerMachineTest(t, p, b)
	session := startMachineTest(t, p, program, "producer", nil)
	event, err := session.Run()
	if err != nil || event.Done {
		t.Fatalf("suspend: %#v %v", event, err)
	}
	cleanup := &ExitRequest{Payload: []Value{"cleanup"}}
	session.cleanups = append(session.cleanups, machineCleanupEntry{sync: func() (*ExitRequest, error) { return cleanup, nil }})
	session.handlers = append(session.handlers, machineHandler{completionBind: "published", frameDepth: len(session.frames)})
	exit, err := session.Abandon()
	if err != nil || exit != cleanup || session.cause != terminalOwnerStop {
		t.Fatalf("stop: cause=%v exit=%#v err=%v", session.cause, exit, err)
	}
	if len(session.frames)+len(session.handlers)+len(session.cleanups) != 0 || !session.finished {
		t.Fatal("owner stop retained completion frames")
	}
}
