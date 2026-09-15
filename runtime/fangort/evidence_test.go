package fangort

import (
	"reflect"
	"testing"
)

func readerRow(value int) *EvidenceRow {
	return ExtendEvidenceRow(nil, EvidenceBinding{Name: "Reader", Family: EvidenceFamily{Direct: value, Exit: value, Machine: value}})
}

type rowProducerFrame struct {
	pc, limit int
	owner     *YieldOwner
	row       *EvidenceRow
	cleanup   *[]int
	fail      bool
}

func (f *rowProducerFrame) Step(m *Machine) MachineStep {
	if f.pc == 0 {
		row, cleanup := f.row, f.cleanup
		m.PushCleanup(func() *ExitRequest {
			*cleanup = append(*cleanup, RowEvidence[int](row, "Reader", MachineEvidence))
			return nil
		})
	} else {
		m.TakeResult()
	}
	if f.pc == f.limit {
		if f.fail {
			return MachineStep{Kind: MachineExit, Exit: &ExitRequest{Target: RowEvidence[*ExitTarget](f.row, "Fail", MachineEvidence)}}
		}
		return MachineStep{Kind: MachineReturn, Value: UnitValue}
	}
	f.pc++
	return MachineStep{Kind: MachineSuspend, Owner: f.owner, Request: RowEvidence[int](f.row, "Reader", MachineEvidence)}
}
func (f *rowProducerFrame) Clear() { *f = rowProducerFrame{} }

func TestCursorEvidenceRefreshesPullAndRestoresCleanupBoundary(t *testing.T) {
	for _, stop := range []int{0, 2, 100000} {
		owner := NewYieldOwner()
		var cleanup []int
		evidence := NewCursorEvidence(readerRow(-1))
		cursor := StartCursorWithEvidence(owner, evidence, &rowProducerFrame{owner: owner, row: evidence.Row(), limit: 100000, cleanup: &cleanup})
		for i := range stop {
			value, present, exit, err := cursor.NextWithEvidence(readerRow(i))
			if err != nil || exit != nil || !present || value != i {
				t.Fatalf("pull %d: %v %v %v %v", i, value, present, exit, err)
			}
			if evidence.row.tail != evidence.boundary || evidence.row.tail.tail != nil {
				t.Fatal("completed pull retained its handler or grew an evidence chain")
			}
		}
		if exit, err := cursor.Close(); exit != nil || err != nil {
			t.Fatalf("close: %v %v", exit, err)
		}
		want := []int{-1}
		if stop == 0 {
			want = nil
		}
		if !reflect.DeepEqual(cleanup, want) || evidence.row.tail != nil || evidence.boundary != nil {
			t.Fatalf("cleanup=%v evidence=%+v", cleanup, evidence)
		}
	}
}

type rowConsumerFrame struct {
	pc      int
	cursor  *MachineIterator
	target  *ExitTarget
	results *[]CursorResult
}

func (f *rowConsumerFrame) Step(m *Machine) MachineStep {
	if f.pc > 0 {
		*f.results = append(*f.results, m.TakeResult().(CursorResult))
	}
	if f.pc == 3 {
		return MachineStep{Kind: MachineReturn, Value: UnitValue}
	}
	f.pc++
	row := ExtendEvidenceRow(readerRow(f.pc), EvidenceBinding{Name: "Fail", Family: EvidenceFamily{Machine: f.target}})
	return MachineStep{Kind: MachineAdvance, Cursor: f.cursor, Evidence: row}
}
func (f *rowConsumerFrame) Clear() { *f = rowConsumerFrame{} }

func TestMachineCursorEvidenceClosesFailedProducerAndAllowsAnotherRead(t *testing.T) {
	owner := NewYieldOwner()
	var cleanup []int
	evidence := NewCursorEvidence(readerRow(-1))
	cursor := StartCursorWithEvidence(owner, evidence, &rowProducerFrame{owner: owner, row: evidence.Row(), limit: 1, cleanup: &cleanup, fail: true})
	target := &ExitTarget{}
	var results []CursorResult
	event, err := StartMachine(&rowConsumerFrame{cursor: cursor, target: target, results: &results}).Run()
	if err != nil || !event.Done || len(results) != 3 {
		t.Fatalf("consumer: %+v %v results=%v", event, err, results)
	}
	if !results[0].Present || results[0].Value != 1 || results[1].Exit == nil || results[1].Exit.Target != target || results[2].Present || results[2].Exit != nil {
		t.Fatalf("results=%+v", results)
	}
	// Failure cleanup runs with the failing pull's handler before that pull
	// completes. Early-stop cleanup uses the scope boundary instead.
	if !reflect.DeepEqual(cleanup, []int{2}) || evidence.row.tail != nil || evidence.boundary != nil {
		t.Fatalf("cleanup=%v evidence=%+v", cleanup, evidence)
	}
}

func TestEvidenceRowsShadowWithoutMutatingLexicalBindings(t *testing.T) {
	outer := readerRow(1)
	inner := ExtendEvidenceRow(outer, EvidenceBinding{Name: "Reader", Family: EvidenceFamily{Machine: 2}})
	if RowEvidence[int](outer, "Reader", MachineEvidence) != 1 || RowEvidence[int](inner, "Reader", MachineEvidence) != 2 {
		t.Fatal("row extension changed lexical evidence")
	}
	if ExtendEvidenceRow(outer) != outer {
		t.Fatal("empty forwarding allocated another row")
	}
}

type foreignRowFrame struct {
	pc         int
	outer, own *YieldOwner
	row        *EvidenceRow
	cleanup    *[]int
}

func (f *foreignRowFrame) Step(m *Machine) MachineStep {
	f.pc++
	if f.pc == 1 {
		row, cleanup := f.row, f.cleanup
		m.PushCleanup(func() *ExitRequest {
			*cleanup = append(*cleanup, RowEvidence[int](row, "Reader", MachineEvidence))
			return nil
		})
		return MachineStep{Kind: MachineSuspend, Owner: f.outer, Request: RowEvidence[int](f.row, "Reader", MachineEvidence)}
	}
	m.TakeResult()
	if f.pc == 2 {
		return MachineStep{Kind: MachineSuspend, Owner: f.own, Request: RowEvidence[int](f.row, "Reader", MachineEvidence)}
	}
	return MachineStep{Kind: MachineReturn, Value: UnitValue}
}
func (f *foreignRowFrame) Clear() { *f = foreignRowFrame{} }

func TestCursorEvidenceSurvivesForeignSuspensionAndNestedCleanup(t *testing.T) {
	for _, stop := range []int{1, 2} {
		outerOwner, innerOwner := NewYieldOwner(), NewYieldOwner()
		outerRow := NewCursorEvidence(readerRow(-1))
		innerRow := NewCursorEvidence(outerRow.Row())
		var cleanup []int
		inner := StartCursorWithEvidence(innerOwner, innerRow, &foreignRowFrame{outer: outerOwner, own: innerOwner, row: innerRow.Row(), cleanup: &cleanup})
		outer := StartCursorWithEvidence(outerOwner, outerRow, &forwardingFrame{owner: outerOwner, input: inner, row: outerRow.Row()})
		for i := 1; i <= stop; i++ {
			value, present, exit, err := outer.NextWithEvidence(readerRow(i))
			if err != nil || exit != nil || !present || value != i {
				t.Fatalf("pull %d: %v %v %v %v", i, value, present, exit, err)
			}
			if inner.busy != (i == 1) {
				t.Fatalf("pull %d: inner advancement borrow=%v", i, inner.busy)
			}
		}
		if exit, err := outer.Close(); exit != nil || err != nil {
			t.Fatalf("close: %v %v", exit, err)
		}
		if !reflect.DeepEqual(cleanup, []int{-1}) || outerRow.row.tail != nil || innerRow.row.tail != nil {
			t.Fatalf("nested cleanup=%v", cleanup)
		}
	}
}
