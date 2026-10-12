package nativedebug

import (
	"reflect"
	"strconv"
	"strings"

	"github.com/waj/fango/internal/debugger"
)

const previewDepth = debugger.PreviewDepth
const previewItems = debugger.PreviewItems
const previewReads = 256
const previewNodes = debugger.PreviewNodes

// valueReader bounds both rendered structure and debugger traffic. It reads
// memory through identifiers, selectors and Delve's dynamic-data projection;
// it never calls target code, including Show or List accessors.
type valueReader struct {
	c         *Controller
	remaining int
	nodes     int
	visiting  map[uint64]bool
}

func (r *valueReader) read(expr string) variable {
	if r.remaining == 0 {
		return variable{Unreadable: "preview limit"}
	}
	r.remaining--
	var out struct{ Variable variable }
	err := r.c.call("Eval", map[string]any{
		"Scope": map[string]any{"GoroutineID": r.c.Goroutine(), "Frame": 0},
		"Expr":  expr,
		"Cfg":   map[string]any{"FollowPointers": true, "MaxVariableRecurse": 1, "MaxStringLen": debugger.PreviewStringBytes, "MaxArrayValues": previewItems, "MaxStructFields": previewItems},
	}, &out)
	if err != nil {
		return variable{Unreadable: "unavailable in this native frame"}
	}
	return out.Variable
}

func child(v variable, name string) (variable, bool) {
	for _, c := range v.Children {
		if c.Name == name {
			return c, true
		}
	}
	return variable{}, false
}
func (r *valueReader) field(v variable, expr, name string) (variable, string) {
	expr = "(" + expr + ")." + name
	if c, ok := child(v, name); ok && !c.OnlyAddr {
		return c, expr
	}
	return r.read(expr), expr
}
func baseType(name string) string {
	name = strings.TrimLeft(name, "*")
	if i := strings.IndexByte(name, '['); i >= 0 {
		name = name[:i]
	}
	return strings.TrimSuffix(name, "_exit")
}
func (r *valueReader) describe(v variable, expr string, depth int) debugger.Value {
	if r.nodes == 0 {
		return debugger.Value{Kind: debugger.LimitValue}
	}
	r.nodes--
	if v.Unreadable != "" {
		return debugger.Value{Kind: debugger.UnavailableValue, Text: v.Unreadable}
	}
	if v.OnlyAddr {
		v = r.read(expr)
		if v.OnlyAddr {
			return debugger.Value{Kind: debugger.UnavailableValue, Text: "unavailable"}
		}
		return r.describe(v, expr, depth)
	}
	if depth < 0 {
		return debugger.Value{Kind: debugger.LimitValue}
	}
	name := baseType(v.Type)
	layout, known := r.c.Manifest.Types[name]
	if known && layout.Kind == "opaque" {
		return debugger.Value{Kind: debugger.OpaqueValue, Text: layout.Name}
	}
	if reflect.Kind(v.Kind) == reflect.Ptr {
		if len(v.Children) == 0 {
			return debugger.Value{Kind: debugger.UnavailableValue, Text: "unavailable pointer"}
		}
		addr := v.Children[0].Addr
		if addr == 0 {
			return debugger.Value{Kind: debugger.UnavailableValue, Text: "nil"}
		}
		if r.visiting[addr] {
			return debugger.Value{Kind: debugger.CycleValue}
		}
		r.visiting[addr] = true
		defer delete(r.visiting, addr)
		return r.describe(v.Children[0], "(*("+expr+"))", depth)
	}
	if name == "fangobuild/fangort.List" {
		return r.list(v, expr, depth)
	}
	if known && layout.Kind != "sum" {
		return r.construct(v, expr, depth, layout)
	}
	if reflect.Kind(v.Kind) == reflect.Interface {
		if len(v.Children) == 0 {
			return debugger.Value{Kind: debugger.OpaqueValue}
		}
		// Delve's .(data) exposes an interface's concrete value without a call.
		return r.describe(v.Children[0], "("+expr+").(data)", depth)
	}
	switch v.Type {
	case "bool":
		if v.Value == "true" {
			return debugger.Value{Kind: debugger.BoolValue, Text: "true"}
		}
		if v.Value == "false" {
			return debugger.Value{Kind: debugger.BoolValue, Text: "false"}
		}
	case "string":
		return debugger.StringPreview(v.Value, v.Len > int64(len(v.Value)))
	case "int32", "rune":
		n, err := strconv.ParseInt(v.Value, 10, 32)
		if err == nil {
			return debugger.Value{Kind: debugger.CharValue, Text: string(rune(n))}
		}
	}
	switch reflect.Kind(v.Kind) {
	case reflect.Bool:
		if v.Value == "true" {
			return debugger.Value{Kind: debugger.BoolValue, Text: "true"}
		}
		if v.Value == "false" {
			return debugger.Value{Kind: debugger.BoolValue, Text: "false"}
		}
	case reflect.String:
		return debugger.StringPreview(v.Value, v.Len > int64(len(v.Value)))
	case reflect.Func:
		return debugger.Value{Kind: debugger.FunctionValue}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		if v.Value != "" {
			return debugger.Value{Kind: debugger.NumberValue, Text: v.Value}
		}
	}
	if name == "fangobuild/fangort.Unit" {
		return debugger.Value{Kind: debugger.UnitValue}
	}
	if _, ok := child(v, "Direct"); ok {
		if _, ok := child(v, "Exit"); ok {
			return debugger.Value{Kind: debugger.FunctionValue}
		}
	}
	// Primitive fake responses in protocol tests and older Delve versions may
	// omit Kind. Never infer a nominal constructor from a generated spelling.
	switch v.Type {
	case "int64", "float64":
		if v.Value != "" {
			return debugger.Value{Kind: debugger.NumberValue, Text: v.Value}
		}
	}
	return debugger.Value{Kind: debugger.OpaqueValue}
}
func (r *valueReader) construct(v variable, expr string, depth int, layout debugger.NativeType) debugger.Value {
	var ctor *debugger.NativeConstructor
	if layout.Kind == "tagged" {
		tag, _ := r.field(v, expr, "Tag")
		n, err := strconv.Atoi(tag.Value)
		if err != nil || tag.Unreadable != "" {
			return debugger.Value{Kind: debugger.UnavailableValue, Text: "unreadable constructor tag"}
		}
		for i := range layout.Constructors {
			if layout.Constructors[i].Tag == n {
				ctor = &layout.Constructors[i]
				break
			}
		}
	} else if len(layout.Constructors) == 1 {
		ctor = &layout.Constructors[0]
	}
	if ctor == nil {
		return debugger.Value{Kind: debugger.UnavailableValue, Text: "invalid constructor tag"}
	}
	out := debugger.Value{Kind: debugger.ConstructorValue, Text: ctor.Name}
	if layout.Kind == "record" {
		out.Kind = debugger.RecordValue
	}
	if layout.Kind == "tuple" {
		out.Kind = debugger.TupleValue
	}
	for i, f := range ctor.Fields {
		if depth == 0 || i == previewItems || r.nodes == 0 {
			out.Truncated = true
			break
		}
		value, path := r.field(v, expr, f.Runtime)
		out.Fields = append(out.Fields, debugger.ValueField{Name: f.Name, Value: r.describe(value, path, depth-1)})
	}
	return out
}
func (r *valueReader) list(v variable, expr string, depth int) debugger.Value {
	out := debugger.Value{Kind: debugger.ListValue}
	seen := map[uint64]bool{}
	for i := 0; i <= previewItems; i++ {
		node, nodeExpr := r.field(v, expr, "node")
		if node.Unreadable != "" {
			out.Fields = append(out.Fields, debugger.ValueField{Value: debugger.Value{Kind: debugger.UnavailableValue, Text: node.Unreadable}})
			break
		}
		if len(node.Children) == 0 {
			node = r.read(nodeExpr)
		}
		if len(node.Children) == 0 {
			out.Fields = append(out.Fields, debugger.ValueField{Value: debugger.Value{Kind: debugger.UnavailableValue, Text: "unavailable spine"}})
			break
		}
		addr := node.Children[0].Addr
		if addr == 0 {
			break
		}
		if seen[addr] {
			out.Fields = append(out.Fields, debugger.ValueField{Value: debugger.Value{Kind: debugger.CycleValue}})
			break
		}
		if depth == 0 || i == previewItems || r.remaining == 0 || r.nodes == 0 {
			out.Truncated = true
			break
		}
		seen[addr] = true
		// Selectors implicitly dereference node; walk tails without spending depth.
		head, headExpr := r.field(node.Children[0], nodeExpr, "head")
		out.Fields = append(out.Fields, debugger.ValueField{Value: r.describe(head, headExpr, depth-1)})
		v, expr = r.field(node.Children[0], nodeExpr, "tail")
	}
	return out
}

func (c *Controller) preview(expr string) debugger.Value {
	r := &valueReader{c: c, remaining: previewReads, nodes: previewNodes, visiting: map[uint64]bool{}}
	return r.describe(r.read(expr), expr, previewDepth)
}
