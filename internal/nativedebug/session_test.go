package nativedebug

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/debugger"
	"github.com/waj/fango/internal/debugsession"
)

func TestSessionSourceSnapshotsAndStructuredInspection(t *testing.T) {
	rpc := &inspectionRPC{args: []variable{{Name: "v_x", Type: "int64", Value: "20"}}}
	const text = "main() = x\n"
	c := &Controller{RPC: rpc, Manifest: &Manifest{Sources: map[string]string{"Main.fango": text},
		Scopes: []debugger.NativeScope{{File: "Main.fango", Line: 1, Span: &debugger.NativeSpan{Start: 9, End: 10}, Bindings: []debugger.Binding{{Name: "x", Runtime: "v_x", Type: "Int"}}}}},
		State: State{CurrentThread: &Thread{Location: Location{File: "Main.fango", Line: 1}}}}
	b := &SessionBackend{Controller: c}
	s := debugsession.New(b)
	event := s.Accept(b.Snapshot())
	loc := event.Frames[0].Location
	if loc.Precision != debugsession.Exact || loc.Text != text || len(loc.Hash) != 64 {
		t.Fatal(loc)
	}
	sources, err := s.Execute(debugsession.Request{Kind: "sources"})
	if err != nil || len(sources.Sources) != 1 || sources.Sources[0].Hash != loc.Hash {
		t.Fatal(sources, err)
	}
	values, err := s.Execute(debugsession.Request{Kind: "print", Arg: "x", Epoch: event.Epoch})
	if err != nil || len(values.Variables) != 1 || debugger.RenderValue(values.Variables[0].Value) != "20" || values.Variables[0].Type != "Int" {
		t.Fatal(values, err)
	}
	raw, err := s.Execute(debugsession.Request{Kind: "raw", RawCommand: "print", Arg: "v_x"})
	if err != nil || len(raw.Raw) != 1 || raw.Raw[0].Name != "v_x" {
		t.Fatal(raw, err)
	}
	_, err = s.Execute(debugsession.Request{Kind: "expand", Handle: 1})
	if err == nil || !strings.Contains(err.Error(), "invalid or expired") {
		t.Fatal(err)
	}
}

func TestSessionMoveFailureRejectsStaleInspection(t *testing.T) {
	c, rpc := controller()
	rpc.failCreateAt = 1
	b := &SessionBackend{Controller: c}
	s := debugsession.New(b)
	s.Accept(b.Snapshot())
	if _, err := s.Execute(debugsession.Request{Kind: "next"}); err == nil || s.State() != "unknown" {
		t.Fatal(s.State(), err)
	}
	if _, err := s.Execute(debugsession.Request{Kind: "locals"}); err == nil {
		t.Fatal("inspected unverified process state")
	}
}

func TestSourceStackNamesAndNativeBoundary(t *testing.T) {
	c, rpc := controller()
	c.Manifest.EntryModule = "Main"
	generated := frame("main.V_Main_dot_run.func1", 3, 1)
	generated.File = "<fango-generated>"
	native := frame("runtime.gopanic", 4, 1)
	native.File = "runtime/panic.go"
	recursive := frame("main.V_Main_dot_run", 2, 1)
	rpc.frames = []Frame{recursive, generated, recursive, frame("main.V__lift_Main_dot_outer_1_inner", 1, 1), frame("main.callbackBody3", 0, 1), native}
	b := &SessionBackend{Controller: c}
	s := debugsession.New(b)
	event := s.Accept(b.Snapshot())
	want := []string{"Main.run", "Main.run", "Main.outer.inner", "Main.<lambda>"}
	if len(event.Frames) != len(want) {
		t.Fatalf("source activations lost: %+v", event)
	}
	for i, name := range want {
		if event.Frames[i].Name != name {
			t.Fatalf("frame %d: %q, want %q", i, event.Frames[i].Name, name)
		}
	}
	if _, err := s.Execute(debugsession.Request{Kind: "frame", Index: 1}); err != nil {
		t.Fatal(err)
	}
	raw, err := s.Execute(debugsession.Request{Kind: "raw", RawCommand: "stack"})
	if err != nil || raw.Kind != "raw-frames" || len(raw.Frames) != 6 || raw.Frames[1].Name != generated.Function.Name || s.SelectedFrame() != 1 {
		t.Fatal(raw, err)
	}
	selected, err := s.Execute(debugsession.Request{Kind: "frame", Index: 1})
	if err != nil || selected.Frames[0].Name != "Main.run" {
		t.Fatal(selected, err)
	}
	if index, err := b.nativeFrame(1); err != nil || index != 2 {
		t.Fatal(index, err)
	}
	if _, err := b.nativeFrame(4); err == nil {
		t.Fatal("accepted hidden frame as source index")
	}
	// An interrupt outside source retains the actual PC at frame zero.
	rpc.frames = []Frame{native, generated, recursive}
	c.State.CurrentThread.Location = native.Location
	event = s.Accept(b.Snapshot())
	if len(event.Frames) != 2 || event.Frames[0].Name != "<native> runtime.gopanic" || event.Frames[0].Location.File != native.File || s.SelectedFrame() != 0 || len(event.Messages) == 0 {
		t.Fatal(event)
	}
	if index, err := b.nativeFrame(1); err != nil || index != 2 {
		t.Fatal(index, err)
	}
	// Missing symbol information never erases a known source activation.
	rpc.frames = []Frame{frame("unrecognized", 1, 1)}
	rpc.frames[0].Function = nil
	event = b.Snapshot()
	if len(event.Frames) != 1 || event.Frames[0].Name != "<unknown Fango function>" {
		t.Fatal(event)
	}
}

func TestFilteredCallerStepping(t *testing.T) {
	for _, mode := range []string{"next", "finish"} {
		t.Run(mode, func(t *testing.T) {
			child := frame("main.V_child", 3, 1)
			parent := frame("main.V_parent", 2, 2)
			caller := frame("main.V_caller", 1, 3)
			generated := frame("main.wrapper", 4, 1)
			generated.File = "<fango-generated>"
			c, rpc := controller(stop{frames: []Frame{parent, caller}}, stop{frames: []Frame{caller}})
			rpc.frames = []Frame{child, generated, parent, caller}
			b := &SessionBackend{Controller: c}
			s := debugsession.New(b)
			s.Accept(b.Snapshot())
			if _, err := s.Execute(debugsession.Request{Kind: "frame", Index: 1}); err != nil {
				t.Fatal(err)
			}
			e, err := s.Execute(debugsession.Request{Kind: mode})
			want := "parent"
			if mode == "finish" {
				want = "caller"
			}
			if err != nil || e.Kind != "stopped" || e.Frames[0].Name != want || s.SelectedFrame() != 0 {
				t.Fatal(e, err)
			}
			if rpc.commands[0] != "stepOut" {
				t.Fatal(rpc.commands)
			}
		})
	}
}
