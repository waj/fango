package eval

import (
	"context"
	"io"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/core/coretest"
	"github.com/waj/fango/internal/types"
)

func TestTypedCursorAdvancementStartsOnDemandAndStaysExhausted(t *testing.T) {
	p, b := coretest.SynchronousCursorScope()
	adt := p.ADTs[0]
	suspended, finished, closed := adt.Ctors[0], adt.Ctors[1], adt.Ctors[2]
	producerDef := core.Def{Name: "producer", Type: b.Unit, Control: types.Control{Transport: types.Machine}, Body: &core.Seq{
		First: &core.Suspend{Request: machineInt(b, 42), Ty: b.Unit}, Then: &core.UnitLit{Ty: b.Unit}, Ty: b.Unit}}
	p.Defs = []core.Def{producerDef, p.Defs[0]}
	if errs := core.InferCaptures(p, b); len(errs) != 0 {
		t.Fatal(errs)
	}
	mp := lowerMachineTest(t, p, b)
	producer := startMachineTest(t, p, mp, "producer", nil)
	it := &MachineIteratorSession{session: producer}
	if it.started || producer.Stats().Steps != 0 {
		t.Fatal("constructing the cursor started production")
	}
	for i := 0; i < 4; i++ {
		caller := startMachineTest(t, p, mp, types.CoroutineAdvanceName, []Value{it, struct{}{}})
		event, err := caller.Run()
		if err != nil || !event.Done || event.Exit != nil {
			t.Fatalf("pull %d: %#v, %v", i, event, err)
		}
		value, ok := event.Value.(*CtorVal)
		if !ok {
			t.Fatalf("pull %d returned %T", i, event.Value)
		}
		if i == 0 {
			if value.Ctor != suspended || len(value.Fields) != 1 || value.Fields[0] != int64(42) {
				t.Fatalf("first pull = %#v", value)
			}
		} else if i == 1 {
			if value.Ctor != finished || len(value.Fields) != 1 {
				t.Fatalf("completion: %#v", value)
			}
		} else if value.Ctor != closed || len(value.Fields) != 0 {
			t.Fatalf("exhausted pull %d = %#v", i, value)
		}
		if it.busy {
			t.Fatal("completed advancement retained its exclusive borrow")
		}
	}
	if len(producer.frames) != 0 || producer.traversal != nil {
		t.Fatal("exhausted producer retained execution storage")
	}
}

func TestMachineCursorScopeClosesOnReturnAndAbandon(t *testing.T) {
	p, b := coretest.CursorScope()
	if errs := core.InferCaptures(p, b); len(errs) != 0 {
		t.Fatal(errs)
	}
	mp := lowerMachineTest(t, p, b)
	for _, abandon := range []bool{false, true} {
		session := startMachineTest(t, p, mp, p.Entry, nil)
		event, err := session.Run()
		if err != nil || event.Done || event.Request != int64(99) {
			t.Fatalf("consumer pause: %#v %v", event, err)
		}
		if len(session.cleanups) != 1 {
			t.Fatalf("pending owners: %d", len(session.cleanups))
		}
		if abandon {
			if exit, err := session.Abandon(); err != nil || exit != nil {
				t.Fatalf("abandon: %#v %v", exit, err)
			}
		} else {
			event, err = session.Resume(struct{}{})
			if err != nil || !event.Done || event.Exit != nil {
				t.Fatalf("return: %#v %v", event, err)
			}
			value := event.Value.(*CtorVal)
			if value.Ctor.Name != "Runtime.Coroutine.Suspended" || value.Fields[0] != int64(42) {
				t.Fatalf("result: %#v", value)
			}
		}
		if len(session.cleanups) != 0 || len(session.frames) != 0 || session.traversal != nil {
			t.Fatal("scope retained execution state")
		}
	}
}

func TestSynchronousScopeDrivesMachineConsumer(t *testing.T) {
	p, b := coretest.SynchronousCursorScope()
	if errs := core.InferCaptures(p, b); len(errs) != 0 {
		t.Fatal(errs)
	}
	mp := lowerMachineTest(t, p, b)
	env := NewEnv()
	env.DefineProg(p)
	if err := env.DefineMachineProg(mp); err != nil {
		t.Fatal(err)
	}
	value, err := Force(context.Background(), p.Entry, env, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	item, ok := value.(*CtorVal)
	if !ok || item.Ctor.Name != "Runtime.Coroutine.Suspended" || item.Fields[0] != int64(42) {
		t.Fatalf("value: %#v", value)
	}
}
