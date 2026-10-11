package repl

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/debugger"
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
	s.debugRefresh(nil)
	switch parts[0] {
	case ":break":
		arg := strings.TrimSpace(strings.TrimPrefix(text, ":break"))
		at := strings.LastIndex(arg, ":")
		if at < 1 {
			fmt.Fprintln(s.out, "usage: :break FILE:LINE")
			return true
		}
		line, err := strconv.Atoi(arg[at+1:])
		if err != nil || line < 1 {
			fmt.Fprintln(s.out, "line must be a positive integer")
			return true
		}
		file := s.debugFile(arg[:at])
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
	case ":breaks":
		if len(s.debug.breakpoints) == 0 {
			fmt.Fprintln(s.out, "no breakpoints")
		}
		for _, bp := range s.debug.breakpoints {
			s.debugPrintBreakpoint(bp)
		}
	case ":delete":
		if len(parts) != 2 {
			fmt.Fprintln(s.out, "usage: :delete ID")
			return true
		}
		id, err := strconv.Atoi(parts[1])
		if err != nil {
			fmt.Fprintln(s.out, "invalid breakpoint ID")
			return true
		}
		for i, bp := range s.debug.breakpoints {
			if bp.ID == id {
				s.debug.breakpoints = append(s.debug.breakpoints[:i], s.debug.breakpoints[i+1:]...)
				fmt.Fprintf(s.out, "deleted breakpoint %d\n", id)
				return true
			}
		}
		fmt.Fprintln(s.out, "unknown breakpoint ID")
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
	fmt.Fprintf(s.out, "breakpoint %d: %s:%d (%s)\n", bp.ID, bp.File, bp.Line, status)
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
		fmt.Fprintln(s.out, "stopped: "+event.Reason)
		if len(d.frames) > 0 {
			s.debugPrintFrame(0, true)
		}
	}
	if event.Error != "" {
		fmt.Fprintln(s.out, "debugger: "+event.Error)
	}
	for _, v := range event.Variables {
		fmt.Fprintf(s.out, "%s = %s", v.Name, v.Value)
		if v.Type != "" {
			fmt.Fprintf(s.out, " : %s", v.Type)
		}
		if v.Handle != 0 {
			fmt.Fprintf(s.out, " [handle %d]", v.Handle)
		}
		fmt.Fprintln(s.out)
	}
	if event.More {
		fmt.Fprintln(s.out, "more children; use :expand HANDLE OFFSET")
	}
	if d.input == nil {
		return debugger.Command{Kind: "stop"}
	}
	for {
		if !s.options.Interactive {
			fmt.Fprint(s.out, "debug> ")
		}
		data, err := d.input.next(lineRequest{prompt: "debug> "})
		if err != nil {
			if err == io.EOF && !s.options.Interactive {
				d.quit = true
			}
			return debugger.Command{Kind: "stop"}
		}
		text := strings.TrimSpace(string(data))
		parts := strings.Fields(text)
		if len(parts) == 0 {
			continue
		}
		if s.debugCommand(text) {
			continue
		}
		command := debugger.Command{Frame: d.selected}
		switch parts[0] {
		case ":continue", ":step", ":next", ":finish", ":stop":
			if len(parts) != 1 {
				fmt.Fprintln(s.out, "this command takes no arguments while stopped")
				continue
			}
			command.Kind = strings.TrimPrefix(parts[0], ":")
			command.Breakpoints = s.debugBreakpoints()
			command.ReplaceBreakpoints = true
			return command
		case ":quit", ":q":
			d.quit = true
			return debugger.Command{Kind: "stop"}
		case ":stack":
			for i := range d.frames {
				s.debugPrintFrame(i, false)
			}
		case ":frame":
			if len(parts) != 2 {
				fmt.Fprintln(s.out, "usage: :frame INDEX")
				continue
			}
			i, err := strconv.Atoi(parts[1])
			if err != nil || i < 0 || i >= len(d.frames) {
				fmt.Fprintln(s.out, "invalid frame")
				continue
			}
			d.selected = i
			s.debugPrintFrame(i, true)
		case ":locals":
			command.Kind = "locals"
			return command
		case ":print":
			if len(parts) != 2 {
				fmt.Fprintln(s.out, "usage: :print NAME (expressions are not evaluated)")
				continue
			}
			command.Kind, command.Name = "print", parts[1]
			return command
		case ":expand":
			if len(parts) < 2 || len(parts) > 3 {
				fmt.Fprintln(s.out, "usage: :expand HANDLE [OFFSET]")
				continue
			}
			handle, err := strconv.ParseUint(parts[1], 10, 64)
			if err != nil {
				fmt.Fprintln(s.out, "invalid handle")
				continue
			}
			offset := 0
			if len(parts) == 3 {
				offset, err = strconv.Atoi(parts[2])
			}
			if err != nil || offset < 0 {
				fmt.Fprintln(s.out, "invalid offset")
				continue
			}
			command.Kind, command.Handle, command.Offset = "expand", handle, offset
			return command
		case ":help":
			fmt.Fprint(s.out, debugHelp)
		default:
			fmt.Fprintln(s.out, "only debugger commands are available while stopped; :help lists them")
		}
	}
}

func (s *Session) debugPrintFrame(index int, excerpt bool) {
	f := s.debug.frames[index]
	site := f.Site
	fmt.Fprintf(s.out, "#%d %s at %s:%d:%d\n", index, f.Name, site.Source.Name, site.Line, site.Column)
	if excerpt {
		file := source.NewFile(site.Source.Name, []byte(site.Source.Text))
		fmt.Fprintln(s.out, file.Excerpt(source.Span{File: file, Start: site.Start, End: site.End}))
	}
}

const debugHelp = `debugger commands:
  :break FILE:LINE  set an exact-line breakpoint
  :breaks          list breakpoints
  :delete ID       remove a breakpoint
  :step EXPR       start stepping an expression (ordinary prompt)
  :continue        resume execution
  :step            step into the next source expression
  :next            step over calls
  :finish          step out of the selected frame
  :stack           list frames
  :frame INDEX     select a frame for inspection
  :locals          inspect parameters, locals, and captures
  :print NAME      inspect one binding without evaluation
  :expand ID [N]   inspect up to 50 children starting at offset N
  :stop            cancel this input
  :quit            cancel this input and leave the REPL
`
