package fangort

import "testing"

type exchangeDriver struct {
	cursor  *MachineIterator
	reply   any
	close   bool
	waiting bool
}

func (f *exchangeDriver) Step(m *Machine) MachineStep {
	if !f.waiting {
		f.waiting = true
		return MachineStep{Kind: MachineAdvance, Cursor: f.cursor, Reply: f.reply, Close: f.close}
	}
	return MachineStep{Kind: MachineReturn, Value: m.TakeResult()}
}
func (f *exchangeDriver) Clear() { *f = exchangeDriver{} }

func exchange(t *testing.T, cursor *MachineIterator, reply any, close bool) CursorResult {
	t.Helper()
	driver := StartMachine(&exchangeDriver{cursor: cursor, reply: reply, close: close})
	event, err := driver.Run()
	if err != nil || !event.Done || event.Exit != nil {
		t.Fatalf("exchange: %+v, %v", event, err)
	}
	return event.Value.(CursorResult)
}

type typedExchangeFrame struct {
	owner            *YieldOwner
	cursor           *MachineIterator
	initial          string
	cleared, cleaned *bool
	t                *testing.T
	waiting          bool
}

func (f *typedExchangeFrame) Step(m *Machine) MachineStep {
	if !f.waiting {
		if exit, err := f.cursor.Close(); err == nil || exit != nil || f.cursor.done {
			f.t.Fatalf("busy close consumed the owner: %v, %v", exit, err)
		}
		cleaned := f.cleaned
		m.PushCleanup(func() *ExitRequest { *cleaned = true; return nil })
		f.waiting = true
		return MachineStep{Kind: MachineSuspend, Owner: f.owner, Request: len(f.initial)}
	}
	return MachineStep{Kind: MachineReturn, Value: m.TakeResult().(string) == "reply"}
}
func (f *typedExchangeFrame) Clear() { *f.cleared = true; *f = typedExchangeFrame{} }

func TestCoroutineTypedExchangeAndTerminalClearing(t *testing.T) {
	for _, close := range []bool{false, true} {
		t.Run(map[bool]string{false: "finish", true: "close"}[close], func(t *testing.T) {
			owner := NewYieldOwner()
			evidence := NewCursorEvidence(&EvidenceRow{})
			factoryCalls := 0
			cleared, cleaned := false, false
			var cursor *MachineIterator
			cursor = StartMachineCoroutine(owner, evidence, func(input any) MachineFrame {
				factoryCalls++
				return &typedExchangeFrame{owner: owner, cursor: cursor, initial: input.(string), cleared: &cleared, cleaned: &cleaned, t: t}
			})
			if factoryCalls != 0 || cursor.machine != nil {
				t.Fatal("producer started eagerly")
			}
			first := exchange(t, cursor, "initial", false)
			if cursor.machine.traversal != nil {
				t.Fatal("innermost pause retained an unnecessary traversal")
			}
			if !first.Present || first.Finished || first.Value != 7 || factoryCalls != 1 || cleared || cleaned {
				t.Fatalf("initial reply/suspension: %+v calls=%d cleared=%v cleaned=%v", first, factoryCalls, cleared, cleaned)
			}
			last := exchange(t, cursor, "reply", close)
			if last.Present || last.Exit != nil || last.Finished != !close || !close && last.Value != true {
				t.Fatalf("terminal result: %+v", last)
			}
			if !cleared || !cleaned || cursor.start != nil || cursor.busy || !cursor.done || evidence.boundary != nil || evidence.row.tail != nil {
				t.Fatal("terminal owner retained producer/evidence or skipped cleanup")
			}
			m := cursor.machine
			if m.hasCursorResult || m.cursorResult.Value != nil || m.cursorResult.Exit != nil {
				t.Fatal("terminal machine retained an advancement result")
			}
			if len(m.frames) != 0 || len(m.cleanups) != 0 || len(m.handlers) != 0 || len(m.states) != 0 {
				t.Fatal("terminal machine retained frames")
			}
			for range 2 {
				again := exchange(t, cursor, "ignored", false)
				if again.Present || again.Finished || again.Value != nil || again.Exit != nil {
					t.Fatalf("terminal result delivered twice: %+v", again)
				}
			}
		})
	}
}

func TestCoroutineCloseBeforeStartClearsLazyFactory(t *testing.T) {
	evidence := NewCursorEvidence(&EvidenceRow{})
	cursor := StartMachineCoroutine(NewYieldOwner(), evidence, func(any) MachineFrame { t.Fatal("closed producer started"); return nil })
	for range 2 {
		if exit, err := cursor.Close(); exit != nil || err != nil {
			t.Fatalf("close: %v %v", exit, err)
		}
	}
	if !cursor.done || cursor.start != nil || cursor.machine != nil || evidence.boundary != nil || evidence.row.tail != nil {
		t.Fatal("close retained lazy producer")
	}
	if result := exchange(t, cursor, 42, false); result.Present || result.Finished || result.Value != nil {
		t.Fatalf("closed owner restarted: %+v", result)
	}
}
