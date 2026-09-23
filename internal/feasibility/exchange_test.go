package feasibility

import (
	"testing"

	"github.com/waj/fango/runtime/fangort"
)

type question struct{ prompt string }

// This exercises the existing private Machine register, not Coroutine.advance
// (whose cursor transfer currently supplies Unit and discards final results).
type dialogue struct {
	pc      int
	owner   *fangort.YieldOwner
	initial string
	cleared bool
}

func (d *dialogue) Step(m *fangort.Machine) fangort.MachineStep {
	if d.pc == 0 {
		d.pc++
		return fangort.MachineStep{Kind: fangort.MachineSuspend, Owner: d.owner, Request: question{"hello " + d.initial}}
	}
	answer := m.TakeResult().(string) // Concrete edge type, as in generated frames.
	return fangort.MachineStep{Kind: fangort.MachineReturn, Value: len(answer)}
}

func (d *dialogue) Clear() { d.owner = nil; d.initial = ""; d.cleared = true }

func TestExistingMachineSupportsDistinctExchangeTypes(t *testing.T) {
	owner := fangort.NewYieldOwner()
	frame := &dialogue{owner: owner, initial: "Ada"}
	m := fangort.StartMachine(frame)
	if frame.pc != 0 {
		t.Fatal("factory executed eagerly")
	}
	first, err := m.Run()
	if err != nil || first.Done || first.Owner != owner || first.Request != (question{"hello Ada"}) {
		t.Fatalf("request: %#v, %v", first, err)
	}
	last, err := m.Resume("received")
	if err != nil || !last.Done || last.Value != 8 || !frame.cleared {
		t.Fatalf("typed reply/result: %#v, %v", last, err)
	}
}
