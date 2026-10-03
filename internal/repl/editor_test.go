package repl

import (
	"io"
	"strings"
	"testing"
)

// scriptedEditor answers each request with the next scripted line and keeps
// what the session asked for and recorded.
type scriptedEditor struct {
	t        *testing.T
	out      *tailWriter
	cancel   func()
	lines    []func(*scriptedEditor) inputLine
	requests []lineRequest
	tails    []string
	history  []string
}

func (e *scriptedEditor) readLine(req lineRequest) inputLine {
	e.requests = append(e.requests, req)
	if req.program {
		e.tails = append(e.tails, e.out.tail())
	}
	if len(e.lines) == 0 {
		return inputLine{err: io.EOF, last: true}
	}
	next := e.lines[0]
	e.lines = e.lines[1:]
	line := next(e)
	e.out.reset()
	return line
}

func (e *scriptedEditor) record(line string) { e.history = append(e.history, line) }
func (e *scriptedEditor) Close()             {}

func typed(text string) func(*scriptedEditor) inputLine {
	return func(*scriptedEditor) inputLine { return inputLine{data: []byte(text + "\n")} }
}

func runScripted(t *testing.T, lines ...func(*scriptedEditor) inputLine) (*scriptedEditor, string) {
	t.Helper()
	ed := &scriptedEditor{t: t, lines: lines}
	var out strings.Builder
	RunWith(strings.NewReader(""), &out, Options{Interactive: true, lineEditor: func(tail *tailWriter, cancel func()) lineEditor {
		ed.out, ed.cancel = tail, cancel
		return ed
	}})
	return ed, out.String()
}

func TestEditorPromptsAndHistory(t *testing.T) {
	ed, out := runScripted(t,
		typed("double x ="),
		typed("    x * 2"),
		typed(""),
		typed("double 21"),
	)
	if !strings.Contains(out, "42 : ") {
		t.Fatalf("multi-line definition through the editor:\n%s", out)
	}
	if strings.Contains(out, "\n> ") || strings.Contains(out, "\n| ") {
		t.Errorf("the session printed a prompt the editor owns:\n%q", out)
	}
	want := []lineRequest{{prompt: "> "}, {prompt: "| "}, {prompt: "| ", suggestion: "    "}, {prompt: "> "}, {prompt: "> "}}
	if len(ed.requests) != len(want) {
		t.Fatalf("requests = %+v, want %+v", ed.requests, want)
	}
	for i := range want {
		if ed.requests[i] != want[i] {
			t.Errorf("request %d = %+v, want %+v", i, ed.requests[i], want[i])
		}
	}
	if got := strings.Join(ed.history, "|"); got != "double x =|    x * 2|double 21" {
		t.Errorf("history = %q", got)
	}
}

func TestEditorProgramReadRepaintsPartialLine(t *testing.T) {
	ed, out := runScripted(t,
		typed("ask() ="),
		typed(`    IO.write "Name? "`),
		typed("    readLine ()"),
		typed(""),
		typed("ask()"),
		typed("Ada"),
	)
	if !strings.Contains(out, `Just (Line { text = "Ada"`) {
		t.Fatalf("program read through the editor:\n%s", out)
	}
	if len(ed.tails) != 1 || ed.tails[0] != "Name? " {
		t.Errorf("program read tails = %q, want the partial output line", ed.tails)
	}
	if strings.Contains(strings.Join(ed.history, "|"), "Ada") {
		t.Errorf("program input reached the history: %q", ed.history)
	}
}

func TestEditorEndOfInputOnlyEndsTheProgramRead(t *testing.T) {
	_, out := runScripted(t,
		typed("readLine ()"),
		func(*scriptedEditor) inputLine { return inputLine{err: io.EOF} },
		typed("1 + 1"),
	)
	if !strings.Contains(out, "Nothing") || !strings.Contains(out, "2 : ") {
		t.Fatalf("Ctrl-D during a program read ended the session:\n%s", out)
	}
}

func TestEditorAbortClearsContinuation(t *testing.T) {
	_, out := runScripted(t,
		typed("double x ="),
		func(*scriptedEditor) inputLine { return inputLine{err: errAbortedInput} },
		typed("double"),
	)
	if !strings.Contains(out, "NAMING ERROR") && !strings.Contains(out, "not defined") {
		t.Fatalf("an aborted continuation still defined double:\n%s", out)
	}
}

func TestEditorAbortInterruptsProgramRead(t *testing.T) {
	cancelled := false
	_, out := runScripted(t,
		typed("readLine ()"),
		func(e *scriptedEditor) inputLine {
			cancelled = true
			e.cancel()
			return inputLine{err: errAbortedInput}
		},
		typed("1 + 1"),
	)
	if !cancelled || !strings.Contains(out, "interrupted") || !strings.Contains(out, "2 : ") {
		t.Fatalf("Ctrl-C during a program read:\n%s", out)
	}
}

func TestTailWriter(t *testing.T) {
	var sink strings.Builder
	tw := &tailWriter{w: &sink}
	io.WriteString(tw, "first\nNa")
	io.WriteString(tw, "me? ")
	if got := tw.tail(); got != "Name? " {
		t.Errorf("tail = %q", got)
	}
	io.WriteString(tw, "\x1b[1mbold")
	if got := tw.tail(); got != "" {
		t.Errorf("tail with a control sequence = %q", got)
	}
	io.WriteString(tw, "\r"+strings.Repeat("x", maxTail+1))
	if got := tw.tail(); got != "" {
		t.Errorf("overlong tail = %q", got)
	}
	io.WriteString(tw, "\nok")
	if got := tw.tail(); got != "ok" || sink.String() != "first\nName? \x1b[1mbold\r"+strings.Repeat("x", maxTail+1)+"\nok" {
		t.Errorf("tail = %q, output = %q", got, sink.String())
	}
}

// Ctrl-D ends the innermost handler level, and the prompt shows how many are
// installed; with none left it leaves the session.
func TestEditorCtrlDEndsLevels(t *testing.T) {
	eof := func(*scriptedEditor) inputLine { return inputLine{err: io.EOF} }
	ed, out := runScripted(t,
		typed("import State"),
		typed("with State.run 1"),
		typed("with State.run 2"),
		typed("State.get()"),
		eof,
		typed("State.get()"),
		eof,
		typed("41 + 1"),
		eof,
		typed("unreachable"),
	)
	var prompts []string
	for _, req := range ed.requests {
		prompts = append(prompts, req.prompt)
	}
	if got, want := strings.Join(prompts, ""), "> > 1> 2> 2> 1> 1> > > "; got != want {
		t.Errorf("prompts = %q, want %q", got, want)
	}
	if !strings.Contains(out, "2 : Int") || !strings.Contains(out, "1 : Int") || !strings.Contains(out, "42 : ") {
		t.Fatalf("levels through the editor (%q):\n%s", prompts, out)
	}
	if len(ed.lines) != 1 {
		t.Errorf("Ctrl-D with no level installed did not leave the session")
	}
}
