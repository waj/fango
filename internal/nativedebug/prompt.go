package nativedebug

import (
	"bufio"
	"fmt"
	"github.com/waj/fango/internal/debugui"
	"io"
	"sort"
)

func (c *Controller) show(out io.Writer) {
	if c.State.Exited {
		fmt.Fprintf(out, "Process exited with status %d\n", c.State.ExitStatus)
		return
	}
	t := c.State.CurrentThread
	if t == nil {
		fmt.Fprintln(out, "Stopped (no current thread)")
		return
	}
	text, ok := c.Manifest.Sources[t.File]
	if !ok {
		fmt.Fprintf(out, "Stopped at %s:%d (generated or native code; use step to reach Fango)\n", t.File, t.Line)
		return
	}
	debugui.Stopped(out, c.State.StopReason)
	loc := debugui.Location{File: t.File, Text: text, Line: t.Line}
	if span, enclosing := c.currentSpan(t.File, t.Line); span != nil {
		loc.Start, loc.End, loc.Precision = span.Start, span.End, debugui.Exact
		if enclosing {
			loc.Precision = debugui.Enclosing
		}
	}
	fmt.Fprintln(out, loc)
	debugui.Excerpt(out, loc)
}

type variable struct {
	Addr                          uint64
	Kind                          uint
	OnlyAddr                      bool
	Name, Type, Value, Unreadable string
	Children                      []variable
	Len                           int64
}

func showVariable(w io.Writer, v variable, indent string) {
	if v.Unreadable != "" {
		fmt.Fprintf(w, "%s%s = <unreadable: %s>\n", indent, v.Name, v.Unreadable)
		return
	}
	fmt.Fprintf(w, "%s%s : %s = %s\n", indent, v.Name, v.Type, v.Value)
	for _, child := range v.Children {
		showVariable(w, child, indent+"  ")
	}
	if v.Len > int64(len(v.Children)) && len(v.Children) > 0 {
		fmt.Fprintf(w, "%s  …\n", indent)
	}
}
func (c *Controller) inspectRaw(command, expr string, w io.Writer) error {
	scope := map[string]any{"GoroutineID": c.Goroutine(), "Frame": 0}
	cfg := map[string]any{"FollowPointers": true, "MaxVariableRecurse": 2, "MaxStringLen": 256, "MaxArrayValues": 50, "MaxStructFields": 50}
	if command == "print" {
		if expr == "" {
			return fmt.Errorf("usage: print EXPR")
		}
		var out struct{ Variable variable }
		if err := c.call("Eval", map[string]any{"Scope": scope, "Expr": expr, "Cfg": cfg}, &out); err != nil {
			return err
		}
		showVariable(w, out.Variable, "")
		return nil
	}
	if command == "args" {
		var out struct{ Args []variable }
		if err := c.call("ListFunctionArgs", map[string]any{"Scope": scope, "Cfg": cfg}, &out); err != nil {
			return err
		}
		for _, v := range out.Args {
			showVariable(w, v, "")
		}
		return nil
	}
	var out struct{ Variables []variable }
	if err := c.call("ListLocalVars", map[string]any{"Scope": scope, "Cfg": cfg}, &out); err != nil {
		return err
	}
	for _, v := range out.Variables {
		showVariable(w, v, "")
	}
	return nil
}

func (c *Controller) Prompt(input io.Reader, out io.Writer) error {
	fmt.Fprintln(out, "Fango native debugger. Type help for commands.")
	c.show(out)
	scanner := bufio.NewScanner(input)
	var commands debugui.Input
	for !c.State.Exited {
		fmt.Fprint(out, debugui.Prompt)
		if !scanner.Scan() {
			return scanner.Err()
		}
		cmd, err := commands.Parse(scanner.Text(), debugui.Native())
		if err != nil {
			debugui.Error(out, err)
			continue
		}
		moved := false
		switch cmd.Kind {
		case "":
			continue
		case "quit":
			return nil
		case "help":
			debugui.Help(out, debugui.Native())
		case "step", "next", "finish", "continue":
			err = c.Move(cmd.Kind)
			moved = err == nil
		case "break":
			var bp Breakpoint
			bp, err = c.Break(cmd.Arg)
			if err == nil {
				debugui.Breakpoint(out, bp.ID, bp.File, bp.Line, "verified")
			}
		case "breaks":
			var ids []int
			for id := range c.users {
				ids = append(ids, id)
			}
			sort.Ints(ids)
			if len(ids) == 0 {
				fmt.Fprintln(out, "no breakpoints")
			}
			for _, id := range ids {
				bp := c.users[id]
				debugui.Breakpoint(out, id, bp.File, bp.Line, "verified")
			}
		case "delete":
			err = c.Delete(cmd.Index)
			if err == nil {
				fmt.Fprintf(out, "deleted breakpoint %d\n", cmd.Index)
			}
		case "stack":
			var frames []Frame
			frames, err = c.Frames()
			if err == nil {
				for i, f := range frames {
					name := "?"
					if f.Function != nil {
						name = f.Function.Name
					}
					debugui.Frame(out, i, name, debugui.Location{File: f.File, Line: f.Line})
				}
			}
		case "args", "locals", "print":
			err = c.inspect(cmd.Kind, cmd.Arg, out)
		case "raw":
			err = c.inspectRaw(cmd.RawCommand, cmd.Arg, out)
		}
		if err != nil {
			debugui.Error(out, err)
		}
		if moved {
			c.show(out)
		}
	}
	return nil
}
