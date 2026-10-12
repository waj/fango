package debugui

import (
	"fmt"
	"io"

	"github.com/waj/fango/internal/debugsession"
)

// Terminal adapts text commands and structured responses. It owns no input
// reader or backend lifecycle: REPL input pumps and native scanners can host it.
type Terminal struct {
	Session  *debugsession.Session
	Features Features
	Out      io.Writer
	Input    Input
}

func (t *Terminal) Handle(text string) (debugsession.Event, error) {
	features := t.Features
	features.Capabilities = t.Session.Capabilities()
	cmd, err := t.Input.Parse(text, features)
	if err != nil {
		Error(t.Out, err)
		return debugsession.Event{}, err
	}
	if cmd.Kind == "" {
		return debugsession.Event{}, nil
	}
	if cmd.Kind == "help" {
		Help(t.Out, features)
		return debugsession.Event{}, nil
	}
	event, err := t.Session.Execute(cmd)
	t.Present(event)
	if err != nil {
		Error(t.Out, err)
	}
	return event, err
}

func (t *Terminal) Accept(event debugsession.Event) {
	t.Present(t.Session.Accept(event))
}

func (t *Terminal) Present(event debugsession.Event) {
	for _, message := range event.Messages {
		fmt.Fprintln(t.Out, message)
	}
	if event.Error != "" {
		Error(t.Out, fmt.Errorf("%s", event.Error))
	}
	switch event.Kind {
	case "stopped":
		Stopped(t.Out, event.Reason)
		if len(event.Frames) > 0 {
			t.frame(0, event.Frames[0], true)
		}
	case "frames":
		for i, frame := range event.Frames {
			t.frame(i, frame, false)
		}
	case "frame":
		if len(event.Frames) > 0 {
			t.frame(event.Index, event.Frames[0], true)
		}
	case "inspection":
		for _, v := range event.Variables {
			Variable(t.Out, v)
		}
		if event.More {
			fmt.Fprintln(t.Out, "more children; use expand HANDLE OFFSET")
		}
	case "raw":
		for _, v := range event.Raw {
			rawValue(t.Out, v, "")
		}
	case "breakpoints":
		if len(event.Breakpoints) == 0 {
			fmt.Fprintln(t.Out, "no breakpoints")
		}
		for _, bp := range event.Breakpoints {
			Breakpoint(t.Out, bp.ID, bp.File, bp.Line, bp.Status)
		}
	case "deleted":
		fmt.Fprintf(t.Out, "deleted breakpoint %d\n", event.Index)
	case "terminated":
		if event.Reason == "exited" {
			fmt.Fprintf(t.Out, "Process exited with status %d\n", event.ExitStatus)
		}
	}
}
func (t *Terminal) frame(index int, frame debugsession.Frame, excerpt bool) {
	if frame.Name != "" {
		Frame(t.Out, index, frame.Name, frame.Location)
	} else {
		fmt.Fprintln(t.Out, frame.Location)
	}
	if excerpt {
		Excerpt(t.Out, frame.Location)
	}
}

func rawValue(w io.Writer, v debugsession.RawValue, indent string) {
	if v.Unreadable != "" {
		fmt.Fprintf(w, "%s%s = <unreadable: %s>\n", indent, v.Name, v.Unreadable)
		return
	}
	fmt.Fprintf(w, "%s%s : %s = %s\n", indent, v.Name, v.Type, v.Value)
	for _, child := range v.Children {
		rawValue(w, child, indent+"  ")
	}
	if v.Truncated {
		fmt.Fprintf(w, "%s  …\n", indent)
	}
}
