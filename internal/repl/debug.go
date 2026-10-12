package repl

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/debugger"
	"github.com/waj/fango/internal/debugsession"
	"github.com/waj/fango/internal/debugui"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/eval"
	"github.com/waj/fango/internal/lexer"
	"github.com/waj/fango/internal/libroot"
	"github.com/waj/fango/internal/parser"
	"github.com/waj/fango/internal/source"
)

type debugExchange struct {
	event debugger.Event
	reply chan debugger.Command
}
type debugSource struct {
	source  *debugger.Source
	lines   map[int]bool
	changed bool
}
type replBreakpoint struct {
	debugger.Breakpoint
	invalid bool
}
type replDebug struct {
	terminal    *debugui.Terminal
	backend     *interpreterSession
	active      core.Expr
	events      chan debugExchange
	input       *linePump
	sources     map[string]*debugSource
	breakpoints []*replBreakpoint
	nextID      int
	step, quit  bool
}

func newReplDebug() *replDebug {
	return &replDebug{events: make(chan debugExchange), sources: map[string]*debugSource{}}
}

func (d *replDebug) DebugExchange(ctx context.Context, event debugger.Event) debugger.Command {
	x := debugExchange{event: event, reply: make(chan debugger.Command, 1)}
	select {
	case d.events <- x:
	case <-ctx.Done():
		return debugger.Command{Kind: "stop"}
	}
	select {
	case cmd := <-x.reply:
		return cmd
	case <-ctx.Done():
		return debugger.Command{Kind: "stop"}
	}
}

// waitEvaluation also serves worker and in-process debug requests. The input
// pump remains exclusively owned by the frontend while execution is paused.
func (s *Session) waitEvaluation(replies <-chan eval.LevelReply) eval.LevelReply {
	if s.debug == nil {
		return <-replies
	}
	for {
		select {
		case result := <-replies:
			if s.debug.terminal != nil && s.debug.terminal.Session.State() != "terminated" {
				s.debug.terminal.Session.Accept(debugsession.Event{Kind: "completed"})
			}
			return result
		case exchange := <-s.debug.events:
			cmd := debugger.Command{Kind: "continue", ReplaceBreakpoints: true}
			if !s.debug.quit {
				cmd = s.debugPrompt(exchange.event)
			}
			exchange.reply <- cmd
		}
	}
}

func (s *Session) debugRefresh(expr core.Expr) []string {
	var messages []string
	if s.debug == nil {
		return nil
	}
	if expr == nil {
		expr = s.debug.active
	}
	found := map[string]*debugSource{}
	visit := func(e core.Expr) {
		site := e.DebugSite()
		if site == nil || site.Source == nil {
			return
		}
		d := found[site.Source.Name]
		if d == nil || d.source.Hash != site.Source.Hash {
			d = &debugSource{source: site.Source, lines: map[int]bool{}}
			found[site.Source.Name] = d
		}
		d.lines[site.Line] = true
	}
	for _, def := range s.installed {
		core.Inspect(def.Body, visit)
	}
	names := make([]string, 0, len(s.promptDefs))
	for name := range s.promptDefs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		core.Inspect(s.promptDefs[name].Body, visit)
	}
	core.Inspect(expr, visit)
	s.debug.sources = found
	for name, src := range found {
		path := filepath.Join(s.options.Root, filepath.FromSlash(name))
		if strings.HasPrefix(name, "<stdlib>/") {
			if root, err := libroot.Root(); err == nil {
				path = filepath.Join(root, "stdlib", filepath.FromSlash(strings.TrimPrefix(name, "<stdlib>/")))
			}
		} else if strings.HasPrefix(name, "<") {
			continue
		}
		if data, err := os.ReadFile(path); err == nil {
			src.changed = fmt.Sprintf("%x", sha256.Sum256(data)) != src.source.Hash
		} else if os.IsNotExist(err) {
			src.changed = true
		}
	}
	for _, bp := range s.debug.breakpoints {
		current := found[bp.File]
		if current == nil {
			continue
		}
		if (current.changed || bp.Hash != "" && bp.Hash != current.source.Hash) && !bp.invalid {
			bp.invalid = true
			messages = append(messages, fmt.Sprintf("breakpoint %d invalidated: source changed; set it again", bp.ID))
		}
		if !current.changed && bp.Hash == "" && current.lines[bp.Line] {
			bp.Hash = current.source.Hash
		}
	}
	return messages
}

func (s *Session) debugBreakpoints() []debugger.Breakpoint {
	out := []debugger.Breakpoint{}
	for _, bp := range s.debug.breakpoints {
		if !bp.invalid && bp.Hash != "" {
			out = append(out, bp.Breakpoint)
		}
	}
	return out
}

func (s *Session) debugConfig(expr core.Expr) *debugger.Config {
	if s.debug == nil {
		return nil
	}
	s.ensureDebugSession()
	s.debug.terminal.Input = debugui.Input{}
	s.debug.backend.pending = nil
	s.debug.terminal.Session.Accept(debugsession.Event{Kind: "running"})
	s.debug.active = expr
	for _, message := range s.debugRefresh(expr) {
		fmt.Fprintln(s.out, message)
	}
	config := &debugger.Config{Step: s.debug.step, Breakpoints: s.debugBreakpoints()}
	s.debug.step = false
	return config
}

func (s *Session) debugFile(name string) string {
	name = filepath.ToSlash(filepath.Clean(name))
	if _, ok := s.debug.sources[name]; ok {
		return name
	}
	if filepath.IsAbs(name) {
		// A checkout can be both the session root and the library root.
		// Resolve its stdlib subtree before the more general source root.
		if lib, err := libroot.Root(); err == nil {
			if rel, err := filepath.Rel(filepath.Join(lib, "stdlib"), name); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return "<stdlib>/" + filepath.ToSlash(rel)
			}
		}
		root, _ := filepath.Abs(s.options.Root)
		if s.options.Root == "" {
			root, _ = filepath.Abs(".")
		}
		if rel, err := filepath.Rel(root, name); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(rel)
		}
	}
	return name
}

// debugCommand handles commands valid at either prompt. It never evaluates a
// source expression or changes checker state while execution is suspended.
func (s *Session) debugCommand(text string) bool {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return false
	}
	switch parts[0] {
	case ":break", ":breaks", ":delete":
	default:
		return false
	}
	if s.debug == nil {
		fmt.Fprintln(s.out, "start with fango repl --debug to use debugger commands")
		return true
	}
	cmd, err := debugui.Parse(text, debugui.Interpreter())
	if err != nil {
		debugui.Error(s.out, err)
		return true
	}
	s.ensureDebugSession()
	event, err := s.debug.terminal.Session.Execute(cmd)
	s.debug.terminal.Present(event)
	if err != nil {
		debugui.Error(s.out, err)
	}
	return true
}

func (s *Session) debugStepExpression(text string) {
	if s.debug == nil {
		fmt.Fprintln(s.out, "start with fango repl --debug to step an expression")
		return
	}
	f := source.NewFile("<repl>", []byte(text))
	toks, errs := lexer.Lex(f)
	if len(errs) == 0 {
		_, errs = parser.ParseExprInput(toks, f)
	}
	if len(errs) > 0 {
		diag.Render(s.out, errs)
		return
	}
	s.debug.step = true
	defer func() { s.debug.step = false }()
	s.input(text, true)
}

func (s *Session) debugPrompt(event debugger.Event) debugger.Command {
	s.ensureDebugSession()
	d := s.debug
	d.terminal.Accept(d.backend.event(event))
	if d.input == nil {
		d.terminal.Session.Accept(debugsession.Event{Kind: "running"})
		return debugger.Command{Kind: "stop"}
	}
	for {
		if !s.options.Interactive {
			fmt.Fprint(s.out, debugui.Prompt)
		}
		data, err := d.input.next(lineRequest{prompt: debugui.Prompt})
		if err != nil {
			if err == io.EOF && !s.options.Interactive {
				d.quit = true
			}
			kind := "running"
			if d.quit {
				kind = "terminated"
			}
			d.terminal.Session.Accept(debugsession.Event{Kind: kind, Reason: "input closed"})
			return debugger.Command{Kind: "stop"}
		}
		d.terminal.Handle(string(data))
		if d.backend.pending != nil {
			command := *d.backend.pending
			d.backend.pending = nil
			return command
		}
	}
}

func (s *Session) debugHelp() {
	debugui.Help(s.out, debugui.Interpreter())
	fmt.Fprintln(s.out, "At the ordinary REPL prompt, use colon commands; :step EXPR starts stepping an expression.")
}
