package nativedebug

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/waj/fango/internal/debugger"
	"github.com/waj/fango/internal/debugsession"
	"github.com/waj/fango/internal/debugui"
)

type inspectionRPC struct {
	args, locals []variable
	evaluated    []string
}

func (r *inspectionRPC) Call(method string, in, out any) error {
	switch strings.TrimPrefix(method, "RPCServer.") {
	case "ListFunctionArgs":
		return reply(out, map[string]any{"Args": r.args})
	case "ListLocalVars":
		return reply(out, map[string]any{"Variables": r.locals})
	case "Eval":
		expr := in.(map[string]any)["Expr"].(string)
		r.evaluated = append(r.evaluated, expr)
		for _, v := range append(r.args, r.locals...) {
			if v.Name == expr {
				return reply(out, map[string]any{"Variable": v})
			}
		}
		if expr == "v_capture" {
			return reply(out, map[string]any{"Variable": variable{Name: expr, Type: "int64", Value: "17"}})
		}
		return fmt.Errorf("unavailable")
	}
	return fmt.Errorf("unexpected RPC %s", method)
}
func TestSourceInspection(t *testing.T) {
	r := &inspectionRPC{
		args:   []variable{{Name: "v_arg", Type: "int64", Value: "3"}, {Name: "ev_Num", Type: "dictionary"}},
		locals: []variable{{Name: "v_inner", Type: "int64", Value: "7"}, {Name: "v_outer", Type: "int64", Value: "99"}, {Name: "t_anf", Value: "42"}},
	}
	bindings := []debugger.Binding{
		{Name: "argument", Runtime: "v_arg", Type: "Int"},
		{Name: "shadow", Runtime: "v_inner", Type: "Int"},
		{Name: "capture", Runtime: "v_capture", Type: "Int"},
		{Name: "absent", Runtime: "v_absent", Type: "Int"},
	}
	c := &Controller{RPC: r, Manifest: &Manifest{Scopes: []debugger.NativeScope{{File: "Main.fango", Line: 12, Bindings: bindings}}}, State: State{CurrentThread: &Thread{Location: Location{File: "Main.fango", Line: 12}, GoroutineID: 1}}}
	inspect := func(command, name string, out io.Writer) error {
		backend := &SessionBackend{Controller: c}
		session := debugsession.New(backend)
		session.Accept(backend.Snapshot())
		req := debugsession.Request{Kind: command, Arg: name}
		if command == "raw" {
			req.RawCommand, req.Arg = name, ""
		}
		event, err := session.Execute(req)
		(&debugui.Terminal{Out: out}).Present(event)
		return err
	}
	for _, tc := range []struct{ command, name, want string }{
		{"args", "", "argument : Int = 3"},
		{"locals", "", "shadow : Int = 7"},
		{"print", "capture", "capture : Int = 17"},
		{"print", "absent", "absent : Int = <unavailable in this native frame>"},
	} {
		var out bytes.Buffer
		if err := inspect(tc.command, tc.name, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), tc.want) {
			t.Fatalf("%s %s: %s", tc.command, tc.name, out.String())
		}
		for _, hidden := range []string{"ev_Num", "t_anf", "v_outer", "99"} {
			if strings.Contains(out.String(), hidden) {
				t.Fatalf("leaked synthetic or shadowed variable: %s", out.String())
			}
		}
	}
	before := len(r.evaluated)
	if err := inspect("print", "shadow + 1", &bytes.Buffer{}); err == nil {
		t.Fatal("accepted an expression as a source name")
	}
	if len(r.evaluated) != before {
		t.Fatal("executed unknown source expression")
	}
	var raw bytes.Buffer
	if err := inspect("raw", "locals", &raw); err != nil || !strings.Contains(raw.String(), "t_anf") {
		t.Fatalf("raw locals: %s, %v", raw.String(), err)
	}
	c.Manifest.Scopes = append(c.Manifest.Scopes, debugger.NativeScope{File: "Main.fango", Line: 12, Bindings: []debugger.Binding{{Name: "shadow", Runtime: "v_outer", Type: "Int"}}})
	var ambiguous bytes.Buffer
	if err := inspect("print", "shadow", &ambiguous); err != nil || !strings.Contains(ambiguous.String(), "multiple lexical scopes") {
		t.Fatalf("ambiguous scope guessed: %s, %v", ambiguous.String(), err)
	}
}
