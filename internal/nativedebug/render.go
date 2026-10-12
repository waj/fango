package nativedebug

import (
	"reflect"
	"strconv"
	"strings"

	"github.com/waj/fango/internal/debugger"
)

const previewDepth = 4
const previewItems = 50
const previewReads = 256
const previewNodes = 512

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
		"Cfg":   map[string]any{"FollowPointers": true, "MaxVariableRecurse": 1, "MaxStringLen": 256, "MaxArrayValues": 50, "MaxStructFields": 50},
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
func (r *valueReader) render(v variable, expr string, depth int) string {
	if r.nodes == 0 {
		return "…"
	}
	r.nodes--
	if v.Unreadable != "" {
		return "<" + v.Unreadable + ">"
	}
	if v.OnlyAddr {
		v = r.read(expr)
		if v.OnlyAddr {
			return "<unavailable>"
		}
		return r.render(v, expr, depth)
	}
	if depth < 0 {
		return "…"
	}
	name := baseType(v.Type)
	layout, known := r.c.Manifest.Types[name]
	if known && layout.Kind == "opaque" {
		return "<opaque " + layout.Name + ">"
	}
	if reflect.Kind(v.Kind) == reflect.Ptr {
		if len(v.Children) == 0 {
			return "<unavailable pointer>"
		}
		addr := v.Children[0].Addr
		if addr == 0 {
			return "<nil>"
		}
		if r.visiting[addr] {
			return "<cycle>"
		}
		r.visiting[addr] = true
		defer delete(r.visiting, addr)
		return r.render(v.Children[0], "(*("+expr+"))", depth)
	}
	if name == "fangobuild/fangort.List" {
		return r.list(v, expr, depth)
	}
	if known && layout.Kind != "sum" {
		return r.construct(v, expr, depth, layout)
	}
	if reflect.Kind(v.Kind) == reflect.Interface {
		if len(v.Children) == 0 {
			return "<opaque>"
		}
		// Delve's .(data) exposes an interface's concrete value without a call.
		return r.render(v.Children[0], "("+expr+").(data)", depth)
	}
	switch v.Type {
	case "bool":
		if v.Value == "true" {
			return "True"
		}
		if v.Value == "false" {
			return "False"
		}
	case "string":
		s := strings.ReplaceAll(strconv.Quote(v.Value), "#{", "\\#{")
		if v.Len > int64(len(v.Value)) {
			s += "…"
		}
		return s
	case "int32", "rune":
		n, err := strconv.ParseInt(v.Value, 10, 32)
		if err == nil {
			return strconv.QuoteRune(rune(n))
		}
	}
	switch reflect.Kind(v.Kind) {
	case reflect.Bool:
		if v.Value == "true" {
			return "True"
		}
		if v.Value == "false" {
			return "False"
		}
	case reflect.String:
		s := strings.ReplaceAll(strconv.Quote(v.Value), "#{", "\\#{")
		if v.Len > int64(len(v.Value)) {
			s += "…"
		}
		return s
	case reflect.Func:
		return "<function>"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		if v.Value != "" {
			return v.Value
		}
	}
	if name == "fangobuild/fangort.Unit" {
		return "()"
	}
	if _, ok := child(v, "Direct"); ok {
		if _, ok := child(v, "Exit"); ok {
			return "<function>"
		}
	}
	// Primitive fake responses in protocol tests and older Delve versions may
	// omit Kind. Never infer a nominal constructor from a generated spelling.
	switch v.Type {
	case "int64", "float64":
		if v.Value != "" {
			return v.Value
		}
	}
	return "<opaque; use raw print>"
}
func (r *valueReader) construct(v variable, expr string, depth int, layout debugger.NativeType) string {
	var ctor *debugger.NativeConstructor
	if layout.Kind == "tagged" {
		tag, _ := r.field(v, expr, "Tag")
		n, err := strconv.Atoi(tag.Value)
		if err != nil || tag.Unreadable != "" {
			return "<unreadable constructor tag>"
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
		return "<invalid constructor tag>"
	}
	if len(ctor.Fields) == 0 {
		if layout.Kind == "record" {
			return "{}"
		}
		return ctor.Name
	}
	if depth == 0 {
		if layout.Kind == "tuple" {
			return "(…)"
		}
		if layout.Kind == "record" {
			return "{ … }"
		}
		return ctor.Name + " …"
	}
	var parts []string
	for i, f := range ctor.Fields {
		if i == previewItems || r.nodes == 0 {
			parts = append(parts, "…")
			break
		}
		value, path := r.field(v, expr, f.Runtime)
		part := r.render(value, path, depth-1)
		if layout.Kind == "record" {
			part = f.Name + " = " + part
		} else if layout.Kind != "tuple" && strings.ContainsAny(part, " \t") && !strings.HasPrefix(part, "[") && !strings.HasPrefix(part, "{") && !strings.HasPrefix(part, "\"") && !strings.HasPrefix(part, "<") && !strings.HasPrefix(part, "(") && !strings.HasPrefix(part, "'") {
			part = "(" + part + ")"
		}
		parts = append(parts, part)
	}
	if layout.Kind == "record" {
		return "{ " + strings.Join(parts, ", ") + " }"
	}
	if layout.Kind == "tuple" {
		return "(" + strings.Join(parts, ", ") + ")"
	}
	return ctor.Name + " " + strings.Join(parts, " ")
}
func (r *valueReader) list(v variable, expr string, depth int) string {
	if depth == 0 {
		return "[…]"
	}
	var parts []string
	seen := map[uint64]bool{}
	for i := 0; i <= previewItems; i++ {
		node, nodeExpr := r.field(v, expr, "node")
		if node.Unreadable != "" {
			parts = append(parts, "<"+node.Unreadable+">")
			break
		}
		if len(node.Children) == 0 {
			node = r.read(nodeExpr)
		}
		if len(node.Children) == 0 {
			parts = append(parts, "<unavailable spine>")
			break
		}
		addr := node.Children[0].Addr
		if addr == 0 {
			break
		}
		if seen[addr] {
			parts = append(parts, "<cycle>")
			break
		}
		if i == previewItems || r.remaining == 0 || r.nodes == 0 {
			parts = append(parts, "…")
			break
		}
		seen[addr] = true
		// Field selectors implicitly dereference node. Fetch only the head;
		// recurse into the tail iteratively so list length doesn't eat depth.
		head, headExpr := r.field(node.Children[0], nodeExpr, "head")
		parts = append(parts, r.render(head, headExpr, depth-1))
		v, expr = r.field(node.Children[0], nodeExpr, "tail")
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func (c *Controller) preview(expr string) string {
	r := &valueReader{c: c, remaining: previewReads, nodes: previewNodes, visiting: map[uint64]bool{}}
	return r.render(r.read(expr), expr, previewDepth)
}
