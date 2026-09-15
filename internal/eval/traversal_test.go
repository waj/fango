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
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	a := sup.FreshRigid(types.General)
	con := &types.TCon{Unique: sup.NextUnique(), Name: "Maybe.Maybe", Args: []types.Type{a}}
	nothing := &types.CtorInfo{Name: "Maybe.Nothing", Index: 0, Result: con}
	just := &types.CtorInfo{Name: "Maybe.Just", Index: 1, Fields: []types.Type{a}, Result: con}
	adt := &types.ADTInfo{Con: con, Params: []*types.TVar{a}, Ctors: []*types.CtorInfo{nothing, just}}
	result := &types.TCon{Unique: con.Unique, Name: con.Name, Args: []types.Type{b.Int}}
	cursor := &types.TCon{Unique: sup.NextUnique(), Name: types.IteratorTypeName, Args: []types.Type{b.Int, b.Unit}}
	control := types.Control{Transport: types.Machine}
	fn := &types.TFun{Arg: cursor, Ret: result, Control: control}
	p := &core.Prog{Intrinsics: map[string]bool{types.IteratorNextName: true}, ADTs: []*types.ADTInfo{adt}, Defs: []core.Def{
		{Name: "producer", Type: b.Unit, Control: control, Body: &core.Seq{
			First: &core.Suspend{Request: machineInt(b, 42), Ty: b.Unit}, Then: &core.UnitLit{Ty: b.Unit}, Ty: b.Unit}},
		{Name: types.IteratorNextName, Type: fn, Params: []string{"cursor"}, ParamCaptures: []types.CaptureVar{sup.FreshCapture()}, Control: control,
			Body: &core.IteratorNext{Cursor: &core.VarRef{Name: "cursor", Local: true, Ty: cursor}, Result: adt, Access: types.ExclusiveAdvance, Ty: result}},
	}}
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
		caller := startMachineTest(t, p, mp, types.IteratorNextName, []Value{it})
		event, err := caller.Run()
		if err != nil || !event.Done || event.Exit != nil {
			t.Fatalf("pull %d: %#v, %v", i, event, err)
		}
		value, ok := event.Value.(*CtorVal)
		if !ok {
			t.Fatalf("pull %d returned %T", i, event.Value)
		}
		if i == 0 {
			if value.Ctor != just || len(value.Fields) != 1 || value.Fields[0] != int64(42) {
				t.Fatalf("first pull = %#v", value)
			}
		} else if value.Ctor != nothing || len(value.Fields) != 0 {
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
			if value.Ctor.Name != "Maybe.Just" || value.Fields[0] != int64(42) {
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
	if !ok || item.Ctor.Name != "Maybe.Just" || item.Fields[0] != int64(42) {
		t.Fatalf("value: %#v", value)
	}
}
