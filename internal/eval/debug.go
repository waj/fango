package eval

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/debugger"
	"github.com/waj/fango/internal/types"
	"github.com/waj/fango/runtime/fangort"
)

var debugIDs atomic.Uint64

type debugValue struct {
	value Value
}

// A debugExecution belongs to one input, including its handler-level child
// evaluator. The evaluator owns all inspection; no value crosses its heap.
type debugExecution struct {
	config   debugger.Config
	host     debugger.Host
	mode     string
	target   uint64
	last     string
	stopping bool
	handles  map[uint64]debugValue
}

func newDebug(config *debugger.Config, host debugger.Host) *debugExecution {
	if config == nil || host == nil {
		return nil
	}
	d := &debugExecution{config: *config, host: host, mode: "continue"}
	if config.Step {
		d.mode = "step"
	}
	return d
}

func (c *IOContext) DebugExchange(ctx context.Context, event debugger.Event) debugger.Command {
	if c.DebugHost == nil {
		return debugger.Command{Kind: "stop"}
	}
	return c.DebugHost.DebugExchange(ctx, event)
}

func (in *interp) debugPoint(e core.Expr, fr *Frame) error {
	d := in.debug
	if d == nil || in.compileTime || d.stopping || e.DebugSite() == nil {
		return nil
	}
	site := e.DebugSite()
	if len(in.trace) == 0 {
		in.trace = append(in.trace, traceFrame{name: "<expression>"})
	}
	f := &in.trace[len(in.trace)-1]
	if f.debugID == 0 {
		f.debugID = debugIDs.Add(1)
	}
	f.debugSite, f.debugScope = site, fr
	key := fmt.Sprintf("%d:%s:%s:%d:%d", f.debugID, site.Source.Name, site.Source.Hash, site.Start, site.End)
	if key == d.last {
		return nil
	}
	d.last = key
	reason := ""
	for _, bp := range d.config.Breakpoints {
		if bp.File == site.Source.Name && bp.Hash == site.Source.Hash && bp.Line == site.Line {
			reason = fmt.Sprintf("breakpoint %d", bp.ID)
			break
		}
	}
	if reason == "" {
		switch d.mode {
		case "step":
			reason = "step"
		case "next", "finish":
			present := false
			for _, frame := range in.trace {
				if frame.debugID == d.target {
					present = true
				}
			}
			if !present || d.mode == "next" && f.debugID == d.target {
				reason = "step"
			}
		}
	}
	if reason == "" {
		return nil
	}
	d.handles = map[uint64]debugValue{}
	defer func() { d.handles = nil }()
	frames, refs := in.debugFrames()
	event := debugger.Event{Kind: "stopped", Reason: reason, Frames: frames}
	for {
		cmd := d.host.DebugExchange(in.ctx, event)
		event = debugger.Event{Kind: "inspection"}
		if cmd.ReplaceBreakpoints {
			d.config.Breakpoints = cmd.Breakpoints
		}
		switch cmd.Kind {
		case "continue", "step", "next", "finish":
			d.mode, d.target = cmd.Kind, f.debugID
			if cmd.Kind == "next" || cmd.Kind == "finish" {
				if cmd.Frame < 0 || cmd.Frame >= len(refs) {
					event.Error = "invalid frame"
					continue
				}
				d.target = refs[cmd.Frame].debugID
			}
			return nil
		case "stop":
			d.stopping = true
			return context.Canceled
		case "stack":
			event.Frames = frames
		case "locals", "print":
			if cmd.Frame < 0 || cmd.Frame >= len(refs) {
				event.Error = "invalid frame"
				break
			}
			frame := refs[cmd.Frame]
			found := false
			for _, binding := range frame.debugSite.Bindings {
				if cmd.Kind == "print" && binding.Name != cmd.Name {
					continue
				}
				found = true
				v, ok := frame.debugScope.lookup(binding.Runtime)
				if !ok {
					event.Variables = append(event.Variables, debugger.Variable{Name: binding.Name, Type: binding.Type, Value: "<unavailable>"})
					continue
				}
				event.Variables = append(event.Variables, in.debugVariable(binding.Name, binding.Type, v))
			}
			sort.Slice(event.Variables, func(i, j int) bool { return event.Variables[i].Name < event.Variables[j].Name })
			if cmd.Kind == "print" && !found {
				event.Error = "name is not a local or captured binding in this frame"
			}
		case "expand":
			value, ok := d.handles[cmd.Handle]
			if !ok {
				event.Error = "invalid or expired value handle"
				break
			}
			if cmd.Offset < 0 {
				event.Error = "offset must be non-negative"
				break
			}
			event.Variables, event.More = in.debugChildren(value.value, cmd.Offset)
		default:
			event.Error = "unknown debugger command"
		}
	}
}

func (in *interp) debugFrames() ([]debugger.Frame, []*traceFrame) {
	var frames []debugger.Frame
	var refs []*traceFrame
	for i := len(in.trace) - 1; i >= 0; i-- {
		f := &in.trace[i]
		if f.debugSite == nil {
			continue
		}
		frames = append(frames, debugger.Frame{ID: f.debugID, Name: f.name, Site: f.debugSite})
		refs = append(refs, f)
	}
	return frames, refs
}

func (in *interp) debugVariable(name, typ string, v Value) debugger.Variable {
	out := debugger.Variable{Name: name, Type: typ, Value: in.debugPreview(v, 2)}
	if in.debugExpandable(v) {
		out.Handle = debugIDs.Add(1)
		in.debug.handles[out.Handle] = debugValue{value: v}
	}
	return out
}

func (in *interp) debugExpandable(v Value) bool {
	switch v := v.(type) {
	case *CtorVal:
		adt := in.env.adts[v.Ctor.Result.Unique]
		return len(v.Fields) > 0 && (adt == nil || !adt.Resource && !adt.NativeIndexed)
	case fangort.List[Value]:
		return !v.IsEmpty()
	}
	return false
}

func (in *interp) debugPreview(v Value, depth int) string {
	switch v := v.(type) {
	case nil:
		return "<unavailable>"
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	case bool:
		if v {
			return "True"
		}
		return "False"
	case rune:
		return strconv.QuoteRune(v)
	case string:
		n := 0
		for i := range v {
			if n == 256 {
				return strconv.Quote(v[:i]) + "…"
			}
			n++
		}
		return strconv.Quote(v)
	case struct{}:
		return "()"
	case *Closure:
		return "<function>"
	case *CtorVal:
		name := types.SurfaceName(v.Ctor.Name)
		adt := in.env.adts[v.Ctor.Result.Unique]
		if adt != nil && (adt.Resource || adt.NativeIndexed) {
			return "<opaque " + types.SurfaceName(adt.Con.Name) + ">"
		}
		if len(v.Fields) == 0 {
			return name
		}
		if depth == 0 {
			return name + " …"
		}
		parts := []string{}
		for i, field := range v.Fields {
			if i == 50 {
				parts = append(parts, "…")
				break
			}
			part := in.debugPreview(field, depth-1)
			if adt != nil && adt.IsRecord() && i < len(adt.RecordFields) {
				part = adt.RecordFields[i].Name + " = " + part
			}
			parts = append(parts, part)
		}
		if adt != nil && adt.IsRecord() {
			return "{ " + strings.Join(parts, ", ") + " }"
		}
		return name + " (" + strings.Join(parts, ", ") + ")"
	case fangort.List[Value]:
		if depth == 0 {
			return "[…]"
		}
		parts := []string{}
		for !v.IsEmpty() && len(parts) < 50 {
			parts = append(parts, in.debugPreview(v.Head(), depth-1))
			v = v.Tail()
		}
		if !v.IsEmpty() {
			parts = append(parts, "…")
		}
		return "[" + strings.Join(parts, ", ") + "]"
	default:
		return "<opaque>"
	}
}

func (in *interp) debugChildren(value Value, offset int) ([]debugger.Variable, bool) {
	var out []debugger.Variable
	switch v := value.(type) {
	case *CtorVal:
		adt := in.env.adts[v.Ctor.Result.Unique]
		for i := offset; i < len(v.Fields) && len(out) < 50; i++ {
			name := strconv.Itoa(i)
			if adt != nil && adt.IsRecord() && i < len(adt.RecordFields) {
				name = adt.RecordFields[i].Name
			}
			typ := ""
			if i < len(v.Ctor.Fields) {
				typ = types.Show(v.Ctor.Fields[i])
			}
			out = append(out, in.debugVariable(name, typ, v.Fields[i]))
		}
		return out, offset+len(out) < len(v.Fields)
	case fangort.List[Value]:
		for i := 0; i < offset && !v.IsEmpty(); i++ {
			v = v.Tail()
		}
		for !v.IsEmpty() && len(out) < 50 {
			out = append(out, in.debugVariable(strconv.Itoa(offset+len(out)), "", v.Head()))
			v = v.Tail()
		}
		return out, !v.IsEmpty()
	}
	return out, false
}
