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
	commands    debugui.Input
	active      core.Expr
	events      chan debugExchange
	input       *linePump
	sources     map[string]*debugSource
	breakpoints []*replBreakpoint
	nextID      int
	step, quit  bool
	frames      []debugger.Frame
	selected    int
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

func (s *Session) debugRefresh(expr core.Expr) {
	if s.debug == nil {
		return
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
			fmt.Fprintf(s.out, "breakpoint %d invalidated: source changed; set it again\n", bp.ID)
		}
		if !current.changed && bp.Hash == "" && current.lines[bp.Line] {
			bp.Hash = current.source.Hash
		}
	}
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
	s.debug.commands = debugui.Input{}
	s.debug.active = expr
	s.debugRefresh(expr)
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
	s.debugRefresh(nil)
	switch cmd.Kind {
	case "break":
		line, file := cmd.Line, s.debugFile(cmd.File)
		var bp *replBreakpoint
		for _, old := range s.debug.breakpoints {
			if old.File == file && old.Line == line {
				bp = old
				break
			}
		}
		if bp == nil {
			s.debug.nextID++
			bp = &replBreakpoint{Breakpoint: debugger.Breakpoint{ID: s.debug.nextID, File: file, Line: line}}
			s.debug.breakpoints = append(s.debug.breakpoints, bp)
		}
		bp.Hash = ""
		bp.invalid = false
		if src := s.debug.sources[file]; src != nil && src.lines[line] {
			bp.Hash = src.source.Hash
			bp.invalid = src.changed
		}
		s.debugPrintBreakpoint(bp)
	case "breaks":
		if len(s.debug.breakpoints) == 0 {
			fmt.Fprintln(s.out, "no breakpoints")
		}
		for _, bp := range s.debug.breakpoints {
			s.debugPrintBreakpoint(bp)
		}
	case "delete":
		id := cmd.Index
		for i, bp := range s.debug.breakpoints {
			if bp.ID == id {
				s.debug.breakpoints = append(s.debug.breakpoints[:i], s.debug.breakpoints[i+1:]...)
				fmt.Fprintf(s.out, "deleted breakpoint %d\n", id)
				return true
			}
		}
		debugui.Error(s.out, fmt.Errorf("unknown breakpoint ID"))
	}
	return true
}

func (s *Session) debugPrintBreakpoint(bp *replBreakpoint) {
	status := "pending import"
	if bp.invalid {
		status = "source changed"
	} else if bp.Hash != "" {
		status = "verified"
	} else if s.debug.sources[bp.File] != nil {
		status = "unresolved: no executable site on this line"
	}
	debugui.Breakpoint(s.out, bp.ID, bp.File, bp.Line, status)
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
	d := s.debug
	if event.Kind == "stopped" {
		d.frames, d.selected = event.Frames, 0
		debugui.Stopped(s.out, event.Reason)
		if len(d.frames) > 0 {
			s.debugPrintFrame(0, true)
		}
	}
	if event.Error != "" {
		debugui.Error(s.out, fmt.Errorf("%s", event.Error))
	}
	for _, v := range event.Variables {
		debugui.Variable(s.out, v)
	}
	if event.More {
		fmt.Fprintln(s.out, "more children; use expand HANDLE OFFSET")
	}
	if d.input == nil {
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
			return debugger.Command{Kind: "stop"}
		}
		parsed, err := d.commands.Parse(string(data), debugui.Interpreter())
		if err != nil {
			debugui.Error(s.out, err)
			continue
		}
		command := debugger.Command{Kind: parsed.Kind, Frame: d.selected}
		switch parsed.Kind {
		case "":
			continue
		case "break", "breaks", "delete":
			s.debugCommand(":" + parsed.Kind + " " + parsed.Arg)
		case "continue", "step", "next", "finish", "stop":
			command.Breakpoints = s.debugBreakpoints()
			command.ReplaceBreakpoints = true
			return command
		case "quit":
			d.quit = true
			return debugger.Command{Kind: "stop"}
		case "stack":
			for i := range d.frames {
				s.debugPrintFrame(i, false)
			}
		case "frame":
			if parsed.Index >= len(d.frames) {
				debugui.Error(s.out, fmt.Errorf("invalid frame"))
				continue
			}
			d.selected = parsed.Index
			s.debugPrintFrame(d.selected, true)
		case "locals":
			return command
		case "print":
			command.Name = parsed.Arg
			return command
		case "expand":
			command.Handle, command.Offset = parsed.Handle, parsed.Offset
			return command
		case "help":
			debugui.Help(s.out, debugui.Interpreter())
		}
	}
}

func (s *Session) debugPrintFrame(index int, excerpt bool) {
	f := s.debug.frames[index]
	site := f.Site
	loc := debugui.Location{File: site.Source.Name, Text: site.Source.Text,
		Line: site.Line, Column: site.Column, Start: site.Start, End: site.End, Precision: debugui.Exact}
	debugui.Frame(s.out, index, f.Name, loc)
	if excerpt {
		debugui.Excerpt(s.out, loc)
	}
}

func (s *Session) debugHelp() {
	debugui.Help(s.out, debugui.Interpreter())
	fmt.Fprintln(s.out, "At the ordinary REPL prompt, use colon commands; :step EXPR starts stepping an expression.")
}
