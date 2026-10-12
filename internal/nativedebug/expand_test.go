package nativedebug

import (
	"fmt"
	"strings"
	"testing"

	"github.com/waj/fango/internal/debugger"
	"github.com/waj/fango/internal/debugsession"
)

type scopedRPC struct {
	*fakeRPC
	values map[int]map[string]variable
	scopes []int
}

func (r *scopedRPC) Call(method string, in, out any) error {
	if method == "RPCServer.Eval" || method == "RPCServer.ListFunctionArgs" || method == "RPCServer.ListLocalVars" {
		frame := in.(map[string]any)["Scope"].(map[string]any)["Frame"].(int)
		r.scopes = append(r.scopes, frame)
		if method == "RPCServer.Eval" {
			expr := in.(map[string]any)["Expr"].(string)
			if v, ok := r.values[frame][expr]; ok {
				return reply(out, map[string]any{"Variable": v})
			}
			return fmt.Errorf("missing %s in frame %d", expr, frame)
		}
		var values []variable
		for name, v := range r.values[frame] {
			v.Name = name
			values = append(values, v)
		}
		return reply(out, map[string]any{"Args": values, "Variables": values})
	}
	return r.fakeRPC.Call(method, in, out)
}

func TestFrameInspectionAndHandleLifetime(t *testing.T) {
	c, rpc := controller(stop{frames: []Frame{frame("parent", 1, 2)}})
	rpc.frames = []Frame{frame("child", 2, 1), frame("parent", 1, 2)}
	r := &scopedRPC{fakeRPC: rpc, values: map[int]map[string]variable{
		0: {"v_x": {Type: "main.Box"}, "(v_x).F0": scalar("", "int64", "10")},
		1: {"v_x": scalar("", "int64", "20")},
	}}
	c.RPC = r
	c.Manifest.Types = map[string]debugger.NativeType{"main.Box": {Kind: "constructor", Constructors: []debugger.NativeConstructor{{Name: "Box", Fields: []debugger.NativeField{{Runtime: "F0"}}}}}}
	for _, line := range []int{1, 2} {
		c.Manifest.Scopes = append(c.Manifest.Scopes, debugger.NativeScope{File: "Main.fango", Line: line, Bindings: []debugger.Binding{{Name: "x", Runtime: "v_x", Type: "Int"}}})
	}
	b := &SessionBackend{Controller: c}
	s := debugsession.New(b)
	stop := s.Accept(b.Snapshot())
	execute := func(req debugsession.Request) debugsession.Event {
		t.Helper()
		e, err := s.Execute(req)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	value := execute(debugsession.Request{Kind: "print", Arg: "x"}).Variables[0]
	if value.Handle == 0 {
		t.Fatal("missing structural handle")
	}
	selected := execute(debugsession.Request{Kind: "frame", Index: 1})
	if selected.Frames[0].Name != "parent" {
		t.Fatal(selected)
	}
	rpc.failStack = true
	if _, err := s.Execute(debugsession.Request{Kind: "stack"}); err == nil || s.SelectedFrame() != 1 {
		t.Fatal("failed stack request changed selected scope")
	}
	rpc.failStack = false
	for _, command := range []string{"print", "args", "locals"} {
		req := debugsession.Request{Kind: command}
		if command == "print" {
			req.Arg = "x"
		}
		event := execute(req)
		if command != "locals" && debugger.RenderValue(event.Variables[0].Value) != "20" {
			t.Fatal(event)
		}
	}
	execute(debugsession.Request{Kind: "raw", RawCommand: "print", Arg: "v_x"})
	if r.scopes[len(r.scopes)-1] != 1 {
		t.Fatal("raw did not use selected frame")
	}
	page := execute(debugsession.Request{Kind: "expand", Handle: value.Handle})
	if len(page.Variables) != 1 || debugger.RenderValue(page.Variables[0].Value) != "10" || r.scopes[len(r.scopes)-1] != 0 {
		t.Fatal("handle lost its owning frame", page)
	}
	execute(debugsession.Request{Kind: "next"})
	if s.SelectedFrame() != 0 || len(c.handles) != 0 {
		t.Fatal("resume retained frame or backend handles")
	}
	if _, err := s.Execute(debugsession.Request{Kind: "expand", Handle: value.Handle}); err == nil {
		t.Fatal("stale handle accepted")
	}
	if _, err := s.Execute(debugsession.Request{Kind: "frame", Index: 0, Epoch: stop.Epoch}); err == nil {
		t.Fatal("stale frame accepted")
	}
}

func TestNativeListPagingBoundsAndCycles(t *testing.T) {
	empty := listNode(0, variable{}, variable{})
	long := empty
	for i := 600; i > 0; i-- {
		long = listNode(uint64(i), scalar("", "int64", fmt.Sprint(i)), long)
	}
	c := &Controller{Manifest: &Manifest{}, RPC: &renderRPC{}}
	root := c.inspectValue("xs", "List Int", valueRef{value: long, expr: "xs"})
	// Outsize skips stop with a usable checkpoint, not an unbounded walk.
	page, more, err := c.expand(root.Handle, 1000000)
	if err == nil || !strings.Contains(err.Error(), "offset 512") || len(page) != 0 || !more {
		t.Fatal(page, more, err)
	}
	page, more, err = c.expand(root.Handle, 512)
	if err != nil || len(page) != 50 || !more || debugger.RenderValue(page[0].Value) != "513" {
		t.Fatal(page, more, err)
	}
	page, more, err = c.expand(root.Handle, 562)
	if err != nil || len(page) != 38 || more || debugger.RenderValue(page[37].Value) != "600" {
		t.Fatal(page, more, err)
	}
	page, more, err = c.expand(root.Handle, 0)
	if err != nil || len(page) != 50 || !more {
		t.Fatal("revisited page mistaken for cycle", err)
	}
	cycle := listNode(1, scalar("", "int64", "1"), listNode(1, variable{}, empty))
	root = c.inspectValue("xs", "List Int", valueRef{value: cycle, expr: "xs"})
	page, more, err = c.expand(root.Handle, 0)
	if err == nil || !strings.Contains(err.Error(), "cycle") || len(page) != 1 || more {
		t.Fatal(page, more, err)
	}
}

func TestExpansionActivePayloadAndOpaqueBoundary(t *testing.T) {
	c := &Controller{RPC: &renderRPC{}, Manifest: &Manifest{Types: map[string]debugger.NativeType{
		"main.Maybe":  {Kind: "tagged", Constructors: []debugger.NativeConstructor{{Name: "Nothing", Tag: 0}, {Name: "Just", Tag: 1, Fields: []debugger.NativeField{{Runtime: "F1_0"}}}}},
		"main.Handle": {Kind: "opaque", Name: "Handle"},
	}}}
	value := variable{Type: "main.Maybe[int64]", Children: []variable{scalar("Tag", "uint8", "1"), scalar("F1_0", "int64", "42"), {Name: "inactive", Unreadable: "must not be read"}}}
	root := c.inspectValue("x", "Maybe Int", valueRef{value: value, expr: "x"})
	page, more, err := c.expand(root.Handle, 0)
	if err != nil || more || len(page) != 1 || page[0].Name != "0" || debugger.RenderValue(page[0].Value) != "42" {
		t.Fatal(page, more, err)
	}
	page, more, err = c.expand(root.Handle, 1000000)
	if err != nil || more || len(page) != 0 {
		t.Fatal(page, more, err)
	}
	value.Children[0].Value = "0"
	for _, v := range []variable{value, {Type: "main.Handle"}, {Unreadable: "not available"}} {
		if got := c.inspectValue("x", "", valueRef{value: v, expr: "x"}); got.Handle != 0 {
			t.Fatal("nonexpandable value exposed a handle", got)
		}
	}
}
