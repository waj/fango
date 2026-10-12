package nativedebug

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/waj/fango/internal/debugger"
)

type renderRPC struct {
	values map[string]variable
	calls  int
}

func (r *renderRPC) Call(method string, in, out any) error {
	if method != "RPCServer.Eval" {
		return fmt.Errorf("unexpected RPC %s", method)
	}
	r.calls++
	expr := in.(map[string]any)["Expr"].(string)
	v, ok := r.values[expr]
	if !ok {
		return fmt.Errorf("unexpected expression %s", expr)
	}
	return reply(out, map[string]any{"Variable": v})
}
func scalar(name, ty, value string) variable { return variable{Name: name, Type: ty, Value: value} }
func TestFangoValueRendering(t *testing.T) {
	fields := []debugger.NativeField{{Runtime: "F0"}}
	layouts := map[string]debugger.NativeType{
		"main.Box":    {Name: "Box", Kind: "constructor", Constructors: []debugger.NativeConstructor{{Name: "Box", Fields: fields}}},
		"main.Person": {Name: "Person", Kind: "record", Constructors: []debugger.NativeConstructor{{Fields: []debugger.NativeField{{Name: "name", Runtime: "F0"}, {Name: "age", Runtime: "F1"}}}}},
		"main.Maybe":  {Kind: "tagged", Constructors: []debugger.NativeConstructor{{Name: "Nothing", Tag: 0}, {Name: "Just", Tag: 1, Fields: []debugger.NativeField{{Runtime: "F1_0"}}}}},
		"main.Handle": {Kind: "opaque", Name: "Handle"},
		"main.Pair":   {Kind: "tuple", Constructors: []debugger.NativeConstructor{{Fields: []debugger.NativeField{{Runtime: "F0"}, {Runtime: "F1"}}}}},
	}
	for _, tc := range []struct {
		v    variable
		want string
	}{
		{scalar("", "bool", "true"), "True"},
		{scalar("", "bool", "false"), "False"},
		{scalar("", "int32", "955"), "'λ'"},
		{variable{Type: "string", Value: "hi\n", Len: 3}, `"hi\n"`},
		{variable{Type: "string", Value: "#{value}", Len: 8}, `"\#{value}"`},
		{variable{Type: "string", Value: "ab", Len: 100}, `"ab"…`},
		{variable{Type: "main.Person", Children: []variable{scalar("F0", "string", "Ada"), scalar("F1", "int64", "42")}}, `{ name = "Ada", age = 42 }`},
		{variable{Type: "main.Maybe[int64]", Children: []variable{scalar("Tag", "uint8", "1"), scalar("F1_0", "int64", "42")}}, "Just 42"},
		{variable{Type: "main.Maybe[int64]", Children: []variable{scalar("Tag", "uint8", "0"), {Name: "F1_0", Unreadable: "inactive payload"}}}, "Nothing"},
		{variable{Type: "main.Maybe[int64]", Children: []variable{scalar("Tag", "uint8", "9")}}, "<invalid constructor tag>"},
		{variable{Type: "main.Box[main.Box[int64]]", Children: []variable{{Name: "F0", Type: "main.Box[int64]", Children: []variable{scalar("F0", "int64", "7")}}}}, "Box (Box 7)"},
		{variable{Type: "main.Box[int32]", Children: []variable{scalar("F0", "int32", "32")}}, "Box ' '"},
		{variable{Type: "main.Box[main.Pair[int64,string]]", Children: []variable{{Name: "F0", Type: "main.Pair[int64,string]", Children: []variable{scalar("F0", "int64", "7"), scalar("F1", "string", "hi")}}}}, `Box (7, "hi")`},
		{variable{Type: "*main.Handle", Kind: uint(reflect.Ptr)}, "<opaque Handle>"},
		{variable{Type: "main.Pair[int64,string]", Children: []variable{scalar("F0", "int64", "7"), scalar("F1", "string", "hi")}}, `(7, "hi")`},
		{variable{Kind: uint(reflect.Func)}, "<function>"},
		{variable{Unreadable: "cannot read memory"}, "<cannot read memory>"},
		{variable{Type: "foreign.Struct"}, "<opaque; use raw print>"},
	} {
		rpc := &renderRPC{values: map[string]variable{"v": tc.v}}
		c := &Controller{RPC: rpc, Manifest: &Manifest{Types: layouts}}
		if got := c.preview("v"); got != tc.want {
			t.Errorf("%+v: got %q, want %q", tc.v, got, tc.want)
		}
		if rpc.calls != 1 {
			t.Fatalf("unexpected target read for loaded fields: %d", rpc.calls)
		}
	}
}
func listNode(addr uint64, head variable, tail variable) variable {
	head.Name = "head"
	tail.Name = "tail"
	return variable{Type: "fangobuild/fangort.List[int64]", Children: []variable{{Name: "node", Kind: uint(reflect.Ptr), Children: []variable{{Addr: addr, Children: []variable{head, tail}}}}}}
}
func TestListBoundsAndCycles(t *testing.T) {
	empty := listNode(0, variable{}, variable{})
	long := empty
	for i := 60; i > 0; i-- {
		long = listNode(uint64(i), scalar("", "int64", fmt.Sprint(i)), long)
	}
	cycle := listNode(1, scalar("", "int64", "1"), listNode(1, variable{}, empty))
	for _, tc := range []struct {
		v              variable
		prefix, suffix string
	}{
		{empty, "[]", "[]"},
		{long, "[1, 2, 3,", "50, …]"},
		{cycle, "[1, <cycle>]", "[1, <cycle>]"},
	} {
		rpc := &renderRPC{values: map[string]variable{"v": tc.v}}
		c := &Controller{RPC: rpc, Manifest: &Manifest{}}
		out := c.preview("v")
		if !strings.HasPrefix(out, tc.prefix) || !strings.HasSuffix(out, tc.suffix) {
			t.Fatal(out)
		}
		if rpc.calls != 1 {
			t.Fatal("walked beyond loaded list")
		}
	}
}
func TestRenderReadAndNodeBudgets(t *testing.T) {
	rpc := &renderRPC{values: map[string]variable{"v": scalar("", "int64", "1")}}
	r := &valueReader{c: &Controller{RPC: rpc, Manifest: &Manifest{}}, remaining: 1, nodes: 1, visiting: map[uint64]bool{}}
	if got := r.render(r.read("v"), "v", 4); got != "1" {
		t.Fatal(got)
	}
	if got := r.render(r.read("v"), "v", 4); got != "…" {
		t.Fatal(got)
	}
	if rpc.calls != 1 {
		t.Fatal("read budget exceeded")
	}
}
