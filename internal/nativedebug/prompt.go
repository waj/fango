package nativedebug

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

const help = `step (s)          enter the next Fango source location
next (n)          step over calls in the current activation
finish (f)        run until the current activation returns
continue (c)      run until a breakpoint or exit
break FILE:LINE   set an exact-line native breakpoint
breaks            list user breakpoints
delete ID         delete a user breakpoint
stack             show the native stack (including generated frames)
args / locals     inspect arguments or locals using Fango names
print NAME        inspect a visible Fango binding
raw args / locals inspect generated Go variables
raw print EXPR    inspect a read-only Go expression
quit (q)          terminate the launched process
Ctrl-C            interrupt execution
`

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
	fmt.Fprintf(out, "%s:%d\n", t.File, t.Line)
	lines := strings.Split(text, "\n")
	if t.Line > 0 && t.Line <= len(lines) {
		fmt.Fprintf(out, "  %s\n", lines[t.Line-1])
	}
}

type variable struct {
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
	last := ""
	for !c.State.Exited {
		fmt.Fprint(out, "debug> ")
		if !scanner.Scan() {
			return scanner.Err()
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			line = last
		}
		if line == "" {
			continue
		}
		last = line
		cmd, arg, _ := strings.Cut(line, " ")
		arg = strings.TrimSpace(arg)
		var err error
		moved := false
		switch cmd {
		case "q", "quit", "exit":
			return nil
		case "h", "help":
			fmt.Fprint(out, help)
		case "s", "step", "n", "next", "f", "finish", "c", "continue":
			if arg != "" {
				err = fmt.Errorf("%s takes no arguments", cmd)
				break
			}
			mode := map[string]string{"s": "step", "n": "next", "f": "finish", "c": "continue"}[cmd]
			if mode == "" {
				mode = cmd
			}
			err = c.Move(mode)
			moved = err == nil
		case "b", "break":
			var bp Breakpoint
			bp, err = c.Break(arg)
			if err == nil {
				fmt.Fprintf(out, "Breakpoint %d at %s:%d\n", bp.ID, bp.File, bp.Line)
			}
		case "breaks":
			var ids []int
			for id := range c.users {
				ids = append(ids, id)
			}
			sort.Ints(ids)
			for _, id := range ids {
				bp := c.users[id]
				fmt.Fprintf(out, "%d %s:%d\n", id, bp.File, bp.Line)
			}
		case "delete":
			var id int
			id, err = strconv.Atoi(arg)
			if err == nil {
				err = c.Delete(id)
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
					fmt.Fprintf(out, "%d %s %s:%d\n", i, name, f.File, f.Line)
				}
			}
		case "args", "locals", "print":
			err = c.inspect(cmd, arg, out)
		case "raw":
			command, expr, _ := strings.Cut(arg, " ")
			if command != "args" && command != "locals" && command != "print" {
				err = fmt.Errorf("usage: raw args | raw locals | raw print EXPR")
			} else {
				err = c.inspectRaw(command, strings.TrimSpace(expr), out)
			}
		default:
			err = fmt.Errorf("unknown command %q; type help", cmd)
		}
		if err != nil {
			fmt.Fprintf(out, "error: %v\n", err)
		}
		if moved {
			c.show(out)
		}
	}
	return nil
}
