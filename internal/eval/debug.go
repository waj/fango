package eval

import (
	"context"
	"fmt"
	"sort"
	"strconv"
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
					event.Variables = append(event.Variables, debugger.Variable{Name: binding.Name, Type: binding.Type, Value: debugger.Value{Kind: debugger.UnavailableValue}})
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
	out := debugger.Variable{Name: name, Type: typ, Value: in.debugPreview(v)}
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

// The reader owns traversal of the interpreter heap. Only detached structure
// crosses the worker protocol; formatting is shared with native inspection.
func (in *interp) debugPreview(v Value) debugger.Value {
	r := debugValueReader{in: in, nodes: debugger.PreviewNodes, visiting: map[*CtorVal]bool{}}
	return r.describe(v, debugger.PreviewDepth)
}

type debugValueReader struct {
	in       *interp
	nodes    int
	visiting map[*CtorVal]bool
}

func (r *debugValueReader) describe(v Value, depth int) debugger.Value {
	if r.nodes == 0 || depth < 0 {
		return debugger.Value{Kind: debugger.LimitValue}
	}
	r.nodes--
	switch v := v.(type) {
	case nil:
		return debugger.Value{Kind: debugger.UnavailableValue}
	case int64:
		return debugger.Value{Kind: debugger.NumberValue, Text: strconv.FormatInt(v, 10)}
	case float64:
		return debugger.Value{Kind: debugger.NumberValue, Text: strconv.FormatFloat(v, 'g', -1, 64)}
	case bool:
		return debugger.Value{Kind: debugger.BoolValue, Text: strconv.FormatBool(v)}
	case rune:
		return debugger.Value{Kind: debugger.CharValue, Text: string(v)}
	case string:
		return debugger.StringPreview(v, false)
	case struct{}:
		return debugger.Value{Kind: debugger.UnitValue}
	case *Closure:
		return debugger.Value{Kind: debugger.FunctionValue}
	case *CtorVal:
		if v == nil {
			return debugger.Value{Kind: debugger.UnavailableValue}
		}
		adt := r.in.env.adts[v.Ctor.Result.Unique]
		if adt != nil && (adt.Resource || adt.NativeIndexed) {
			return debugger.Value{Kind: debugger.OpaqueValue, Text: types.SurfaceName(adt.Con.Name)}
		}
		if r.visiting[v] {
			return debugger.Value{Kind: debugger.CycleValue}
		}
		r.visiting[v] = true
		defer delete(r.visiting, v)
		out := debugger.Value{Kind: debugger.ConstructorValue, Text: types.SurfaceName(v.Ctor.Name)}
		if v.Ctor.Result.Name == "Tuple.Pair" || v.Ctor.Result.Name == "Tuple.Triple" {
			out.Kind = debugger.TupleValue
		} else if adt != nil && adt.IsRecord() {
			out.Kind = debugger.RecordValue
		}
		for i, field := range v.Fields {
			if depth == 0 || i == debugger.PreviewItems || r.nodes == 0 {
				out.Truncated = true
				break
			}
			f := debugger.ValueField{Value: r.describe(field, depth-1)}
			if out.Kind == debugger.RecordValue && i < len(adt.RecordFields) {
				f.Name = adt.RecordFields[i].Name
			}
			out.Fields = append(out.Fields, f)
		}
		return out
	case fangort.List[Value]:
		out := debugger.Value{Kind: debugger.ListValue}
		for !v.IsEmpty() {
			if depth == 0 || len(out.Fields) == debugger.PreviewItems || r.nodes == 0 {
				out.Truncated = true
				break
			}
			out.Fields = append(out.Fields, debugger.ValueField{Value: r.describe(v.Head(), depth-1)})
			v = v.Tail()
		}
		return out
	default:
		return debugger.Value{Kind: debugger.OpaqueValue}
	}
}

func (in *interp) debugChildren(value Value, offset int) ([]debugger.Variable, bool) {
	var out []debugger.Variable
	switch v := value.(type) {
	case *CtorVal:
		adt := in.env.adts[v.Ctor.Result.Unique]
		for i := offset; i < len(v.Fields) && len(out) < debugger.PreviewItems; i++ {
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
		for !v.IsEmpty() && len(out) < debugger.PreviewItems {
			out = append(out, in.debugVariable(strconv.Itoa(offset+len(out)), "", v.Head()))
			v = v.Tail()
		}
		return out, !v.IsEmpty()
	}
	return out, false
}
