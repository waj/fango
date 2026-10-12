// Package debugui owns terminal command syntax and presentation shared by the
// interpreter and native debuggers. Execution and input lifetimes stay in hosts.
package debugui

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/waj/fango/internal/debugsession"
)

const Prompt = "debug> "

// Features describe the operations a host can currently serve. They also drive
// help, so unsupported commands never appear as working commands.
type Features struct {
	debugsession.Capabilities
	Quit, Stack, Locals, Interrupt string
}

func Interpreter() Features {
	return Features{Capabilities: debugsession.Capabilities{Name: "interpreter", Frame: true, Expand: true, Stop: true},
		Quit: "cancel this input and leave the REPL", Stack: "list source frames",
		Locals: "inspect parameters, locals, and captures", Interrupt: "cancel the current input"}
}
func Native() Features {
	return Features{Capabilities: debugsession.Capabilities{Name: "native", Frame: true, Expand: true, Args: true, Raw: true, Interrupt: true},
		Quit: "terminate the launched process", Stack: "list source frames",
		Locals: "inspect locals and captures", Interrupt: "interrupt execution"}
}

type commandSpec struct {
	name, alias, usage, description string
}

var commands = []commandSpec{
	{"break", "b", "break FILE:LINE", "set an exact-line breakpoint"},
	{"breaks", "", "breaks", "list breakpoints and their status"},
	{"delete", "", "delete ID", "remove a breakpoint"},
	{"continue", "c", "continue", "resume until a breakpoint or completion"},
	{"step", "s", "step", "enter the next Fango source location"},
	{"next", "n", "next", "step over calls"},
	{"finish", "f", "finish", "run until the activation returns"},
	{"stack", "", "stack", ""},
	{"frame", "", "frame INDEX", "select a frame"},
	{"args", "", "args", "inspect arguments"},
	{"locals", "", "locals", ""},
	{"print", "", "print NAME", "inspect one binding (expressions are not evaluated)"},
	{"expand", "", "expand HANDLE [OFFSET]", "inspect up to 50 children"},
	{"raw", "", "raw stack | args | locals | print EXPR", "inspect native frames, Go names and read-only Go expressions"},
	{"stop", "", "stop", "cancel this input and return to the REPL"},
	{"quit", "q", "quit", ""},
	{"help", "h", "help", "list debugger commands"},
}

type Command = debugsession.Request

// Parse validates a stopped command before any backend operation is attempted.
// Source expressions are never parsed here. Raw expressions are only passed to
// a backend that explicitly supports raw inspection.
func Parse(text string, features Features) (Command, error) {
	var cmd Command
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return cmd, nil
	}
	name := strings.TrimPrefix(fields[0], ":")
	if name == "exit" {
		name = "quit"
	}
	var spec *commandSpec
	for i := range commands {
		s := &commands[i]
		if name == s.name || name == s.alias && s.alias != "" {
			spec = s
			break
		}
	}
	if spec == nil {
		return cmd, fmt.Errorf("unknown command %q; only debugger commands are available while stopped; type help", fields[0])
	}
	if !features.Supports(spec.name) {
		return cmd, fmt.Errorf("%s is not supported by the %s debugger", spec.name, features.Name)
	}
	cmd.Kind = spec.name
	cmd.Arg = strings.TrimSpace(strings.TrimSpace(text)[len(fields[0]):])
	args := fields[1:]
	usage := func() (Command, error) {
		extra := ""
		if cmd.Kind == "print" {
			extra = " (expressions are not evaluated)"
		}
		return Command{}, fmt.Errorf("usage: %s%s", spec.usage, extra)
	}
	switch cmd.Kind {
	case "break":
		at := strings.LastIndex(cmd.Arg, ":")
		if at < 1 {
			return usage()
		}
		line, err := strconv.Atoi(cmd.Arg[at+1:])
		if err != nil || line < 1 {
			return Command{}, fmt.Errorf("line must be a positive integer")
		}
		cmd.File, cmd.Line = cmd.Arg[:at], line
	case "delete", "frame":
		if len(args) != 1 {
			return usage()
		}
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 0 || cmd.Kind == "delete" && n == 0 {
			return usage()
		}
		cmd.Index = n
	case "print":
		if len(args) != 1 {
			return usage()
		}
	case "expand":
		if len(args) < 1 || len(args) > 2 {
			return usage()
		}
		n, err := strconv.ParseUint(args[0], 10, 64)
		if err != nil || n == 0 {
			return usage()
		}
		cmd.Handle = n
		if len(args) == 2 {
			cmd.Offset, err = strconv.Atoi(args[1])
			if err != nil || cmd.Offset < 0 {
				return usage()
			}
		}
	case "raw":
		if len(args) == 0 {
			return usage()
		}
		cmd.RawCommand = args[0]
		cmd.Arg = strings.TrimSpace(cmd.Arg[len(args[0]):])
		switch cmd.RawCommand {
		case "args", "locals", "stack":
			if len(args) != 1 {
				return usage()
			}
		case "print":
			if cmd.Arg == "" {
				return usage()
			}
		default:
			return usage()
		}
	default:
		if len(args) != 0 {
			return Command{}, fmt.Errorf("%s takes no arguments while stopped", cmd.Kind)
		}
	}
	return cmd, nil
}

// Input retains the last valid command across stops in one execution. Invalid
// input does not replace it; a blank initial line does nothing.
type Input struct{ last string }

func (i *Input) Parse(text string, features Features) (Command, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		text = i.last
	}
	cmd, err := Parse(text, features)
	if err == nil && cmd.Kind != "" {
		i.last = text
	}
	return cmd, err
}

func Help(w io.Writer, features Features) {
	fmt.Fprintln(w, "debugger commands (optional ':' prefix; blank input repeats the last valid command):")
	for _, s := range commands {
		if !features.Supports(s.name) {
			continue
		}
		description := s.description
		switch s.name {
		case "quit":
			description = features.Quit
		case "stack":
			description = features.Stack
		case "locals":
			description = features.Locals
		}
		usage := s.usage
		if s.alias != "" {
			usage += " (" + s.alias + ")"
		}
		fmt.Fprintf(w, "  %-30s %s\n", usage, description)
	}
	fmt.Fprintf(w, "  %-30s %s\n", "Ctrl-C", features.Interrupt)
}
