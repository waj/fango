package repl

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/peterh/liner"
)

// historyFile holds the edited prompt lines of every terminal session.
const historyFile = ".fango_history"

// lineEditor is the line source of an interactive session, which also keeps
// the history of submitted prompt lines.
type lineEditor interface {
	lineSource
	record(line string)
	Close()
}

// editor is the terminal line source: liner owns stdin for prompt and
// program lines alike, so the pump keeps a single reader. Ctrl-C reaches it
// as a key rather than SIGINT; cancel forwards it to the running evaluation.
type editor struct {
	line    *liner.State
	out     *tailWriter
	cancel  func()
	history string
	last    string
}

func newEditor(out *tailWriter, cancel func()) *editor {
	e := &editor{line: liner.NewLiner(), out: out, cancel: cancel}
	e.line.SetCtrlCAborts(true)
	if home, err := os.UserHomeDir(); err == nil {
		e.history = filepath.Join(home, historyFile)
		if f, err := os.Open(e.history); err == nil {
			_, _ = e.line.ReadHistory(f)
			f.Close()
		}
	}
	return e
}

func (e *editor) readLine(req lineRequest) inputLine {
	prompt := req.prompt
	if req.program {
		// liner prints its prompt from column 0, so a program read repaints
		// the partial line the program already wrote in its place.
		prompt = e.out.tail()
		if prompt != "" {
			fmt.Fprint(e.out, "\r")
		}
	}
	text, err := e.line.PromptWithSuggestion(prompt, req.suggestion, -1)
	switch {
	case err == nil:
		e.out.reset()
		return inputLine{data: []byte(text + "\n")}
	case errors.Is(err, liner.ErrPromptAborted):
		e.out.reset()
		e.cancel()
		return inputLine{err: errAbortedInput}
	case errors.Is(err, io.EOF):
		// Ctrl-D ends this read only; the terminal keeps accepting input.
		return inputLine{err: io.EOF}
	default:
		return inputLine{err: err, last: true}
	}
}

// record appends a submitted prompt line to the history, skipping a repeat of
// the previous entry.
func (e *editor) record(line string) {
	if line == e.last {
		return
	}
	e.last = line
	e.line.AppendHistory(line)
}

// Close saves the history and restores the terminal mode.
func (e *editor) Close() {
	if e.history != "" {
		if f, err := os.Create(e.history); err == nil {
			_, _ = e.line.WriteHistory(f)
			f.Close()
		}
	}
	_ = e.line.Close()
}

// maxTail bounds the partial output line a program read repaints as its
// prompt; a longer one is left to scroll with the edited text.
const maxTail = 200

// tailWriter remembers the output written since the last line break.
type tailWriter struct {
	w        io.Writer
	mu       sync.Mutex
	buf      []byte
	overflow bool
}

func (t *tailWriter) Write(p []byte) (int, error) {
	n, err := t.w.Write(p)
	t.mu.Lock()
	defer t.mu.Unlock()
	written := p[:n]
	if i := strings.LastIndexAny(string(written), "\r\n"); i >= 0 {
		t.buf, t.overflow = t.buf[:0], false
		written = written[i+1:]
	}
	if !t.overflow {
		t.buf = append(t.buf, written...)
		if len(t.buf) > maxTail {
			t.buf, t.overflow = t.buf[:0], true
		}
	}
	return n, err
}

func (t *tailWriter) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf, t.overflow = t.buf[:0], false
}

// tail is the partial line, or empty when it cannot serve as a liner prompt:
// too long, invalid UTF-8, or holding control characters such as colours.
func (t *tailWriter) tail() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.overflow || !utf8.Valid(t.buf) {
		return ""
	}
	s := string(t.buf)
	if strings.IndexFunc(s, func(r rune) bool { return unicode.Is(unicode.C, r) }) >= 0 {
		return ""
	}
	return s
}
