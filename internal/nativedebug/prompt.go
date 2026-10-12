package nativedebug

import (
	"bufio"
	"fmt"
	"io"

	"github.com/waj/fango/internal/debugsession"
	"github.com/waj/fango/internal/debugui"
)

type variable struct {
	Addr                          uint64
	Kind                          uint
	OnlyAddr                      bool
	Name, Type, Value, Unreadable string
	Children                      []variable
	Len                           int64
}

func (c *Controller) rawVariables(command, expr string) ([]debugsession.RawValue, error) {
	scope := map[string]any{"GoroutineID": c.Goroutine(), "Frame": 0}
	cfg := map[string]any{"FollowPointers": true, "MaxVariableRecurse": 2, "MaxStringLen": 256, "MaxArrayValues": 50, "MaxStructFields": 50}
	if command == "print" {
		if expr == "" {
			return nil, fmt.Errorf("usage: print EXPR")
		}
		var out struct{ Variable variable }
		if err := c.call("Eval", map[string]any{"Scope": scope, "Expr": expr, "Cfg": cfg}, &out); err != nil {
			return nil, err
		}
		return []debugsession.RawValue{rawValue(out.Variable)}, nil
	}
	if command == "args" {
		var out struct{ Args []variable }
		if err := c.call("ListFunctionArgs", map[string]any{"Scope": scope, "Cfg": cfg}, &out); err != nil {
			return nil, err
		}
		return rawValues(out.Args), nil
	}
	var out struct{ Variables []variable }
	if err := c.call("ListLocalVars", map[string]any{"Scope": scope, "Cfg": cfg}, &out); err != nil {
		return nil, err
	}
	return rawValues(out.Variables), nil
}

func rawValue(v variable) debugsession.RawValue {
	return debugsession.RawValue{Name: v.Name, Type: v.Type, Value: v.Value, Unreadable: v.Unreadable,
		Children: rawValues(v.Children), Truncated: v.Len > int64(len(v.Children)) && len(v.Children) > 0}
}
func rawValues(values []variable) []debugsession.RawValue {
	var out []debugsession.RawValue
	for _, v := range values {
		out = append(out, rawValue(v))
	}
	return out
}
func (c *Controller) Prompt(input io.Reader, out io.Writer) error {
	backend := &SessionBackend{Controller: c}
	return c.prompt(input, out, backend, debugsession.New(backend))
}

func (c *Controller) prompt(input io.Reader, out io.Writer, backend *SessionBackend, session *debugsession.Session) error {
	terminal := &debugui.Terminal{Session: session, Features: debugui.Native(), Out: out}
	fmt.Fprintln(out, "Fango native debugger. Type help for commands.")
	terminal.Accept(backend.Snapshot())
	scanner := bufio.NewScanner(input)
	for terminal.Session.State() != "terminated" {
		fmt.Fprint(out, debugui.Prompt)
		if !scanner.Scan() {
			session.Accept(debugsession.Event{Kind: "terminated", Reason: "input closed"})
			return scanner.Err()
		}
		terminal.Handle(scanner.Text())
	}
	return nil
}
