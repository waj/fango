package nativedebug

import (
	"fmt"
	"reflect"
	"strconv"

	"github.com/waj/fango/internal/debugger"
)

// A reference belongs to one suspension and retains its inspection scope even
// when the frontend selects a different frame. No address is a public handle.
type valueRef struct {
	frame     int
	gid       int64
	value     variable
	expr      string
	cursors   map[int]listCursor
	addresses map[uint64]int
}
type listCursor struct {
	value variable
	expr  string
}

func (c *Controller) inspectValue(name, typ string, ref valueRef) debugger.Variable {
	r := c.reader(ref.frame)
	r.gid = ref.gid
	return c.inspectWithReader(name, typ, ref, r)
}

func (c *Controller) inspectWithReader(name, typ string, ref valueRef, r *valueReader) debugger.Variable {
	out := debugger.Variable{Name: name, Type: typ, Value: r.describe(ref.value, ref.expr, previewDepth)}
	switch out.Value.Kind {
	case debugger.ListValue, debugger.RecordValue, debugger.TupleValue, debugger.ConstructorValue:
		if len(out.Value.Fields) > 0 || out.Value.Truncated {
			if c.handles == nil {
				c.handles = map[uint64]*valueRef{}
			}
			c.nextHandle++
			out.Handle = c.nextHandle
			c.handles[out.Handle] = &ref
		}
	}
	return out
}

// Delve accepts an address cast to a quoted DWARF type. Anchoring loaded
// children keeps list paging paths constant in size rather than accumulating
// thousands of .node.tail selectors. The address and type come only from Delve.
func anchor(v variable, expr string) string {
	if v.Addr != 0 && v.Type != "" {
		return fmt.Sprintf("(*(*%s)(%#x))", strconv.Quote(v.Type), v.Addr)
	}
	return expr
}

func (r *valueReader) expansionTarget(v variable, expr string) (variable, string, debugger.NativeType, error) {
	seen := map[uint64]bool{}
	for r.nodes > 0 {
		r.nodes--
		if v.Unreadable != "" {
			return variable{}, "", debugger.NativeType{}, fmt.Errorf("%s", v.Unreadable)
		}
		if len(expr) > 16384 {
			return variable{}, "", debugger.NativeType{}, fmt.Errorf("native inspection path limit")
		}
		if v.OnlyAddr {
			v = r.read(expr)
			if v.OnlyAddr {
				return variable{}, "", debugger.NativeType{}, fmt.Errorf("unavailable value")
			}
			continue
		}
		name := baseType(v.Type)
		layout, known := r.c.Manifest.Types[name]
		if known && layout.Kind == "opaque" {
			break
		}
		if reflect.Kind(v.Kind) == reflect.Ptr {
			if len(v.Children) == 0 || v.Children[0].Addr == 0 {
				break
			}
			addr := v.Children[0].Addr
			if seen[addr] {
				return variable{}, "", debugger.NativeType{}, fmt.Errorf("cycle in value representation")
			}
			seen[addr] = true
			v, expr = v.Children[0], "(*("+expr+"))"
			continue
		}
		if name == "fangobuild/fangort.List" {
			return v, anchor(v, expr), debugger.NativeType{Kind: "list"}, nil
		}
		if known && layout.Kind != "sum" {
			return v, anchor(v, expr), layout, nil
		}
		if reflect.Kind(v.Kind) != reflect.Interface || len(v.Children) == 0 {
			break
		}
		v, expr = v.Children[0], "("+expr+").(data)"
	}
	return variable{}, "", debugger.NativeType{}, fmt.Errorf("value has no available structural children")
}

func (c *Controller) expand(handle uint64, offset int) ([]debugger.Variable, bool, error) {
	ref := c.handles[handle]
	if ref == nil {
		return nil, false, fmt.Errorf("invalid or expired value handle")
	}
	if offset < 0 {
		return nil, false, fmt.Errorf("offset must be non-negative")
	}
	r := c.reader(ref.frame)
	r.gid = ref.gid
	v, expr, layout, err := r.expansionTarget(ref.value, ref.expr)
	if err != nil {
		return nil, false, err
	}
	if layout.Kind == "list" {
		return c.expandList(r, ref, v, expr, offset)
	}
	ctor, reason := r.constructor(v, expr, layout)
	if reason != "" {
		return nil, false, fmt.Errorf("%s", reason)
	}
	var out []debugger.Variable
	for i := offset; i < len(ctor.Fields) && len(out) < previewItems; i++ {
		f := ctor.Fields[i]
		name := strconv.Itoa(i)
		if layout.Kind == "record" {
			name = f.Name
		}
		value, path := r.field(v, expr, f.Runtime)
		out = append(out, c.inspectValue(name, "", valueRef{frame: ref.frame, gid: ref.gid, value: value, expr: anchor(value, path)}))
	}
	return out, offset < len(ctor.Fields)-len(out), nil
}

func (c *Controller) expandList(r *valueReader, ref *valueRef, v variable, expr string, offset int) ([]debugger.Variable, bool, error) {
	if ref.cursors == nil {
		ref.cursors = map[int]listCursor{0: {v, expr}}
		ref.addresses = map[uint64]int{}
	}
	index := 0
	for at := range ref.cursors {
		if at <= offset && at > index {
			index = at
		}
	}
	v, expr = ref.cursors[index].value, ref.cursors[index].expr
	var out []debugger.Variable
	for visited := 0; visited < previewNodes; visited++ {
		if len(expr) > 16384 {
			return out, false, fmt.Errorf("native inspection path limit")
		}
		if r.remaining < 4 {
			ref.cursors[index] = listCursor{v, expr}
			return out, true, fmt.Errorf("list traversal read limit at offset %d; expand from this offset to continue", index)
		}
		node, path := r.field(v, expr, "node")
		if node.Unreadable != "" {
			return out, false, fmt.Errorf("list spine: %s", node.Unreadable)
		}
		if len(node.Children) == 0 {
			node = r.read(path)
		}
		if len(node.Children) == 0 {
			return out, false, fmt.Errorf("unavailable list spine at offset %d", index)
		}
		addr := node.Children[0].Addr
		if addr == 0 {
			return out, false, nil
		}
		if previous, exists := ref.addresses[addr]; exists && previous != index {
			return out, false, fmt.Errorf("list cycle at offset %d", index)
		}
		if len(out) == previewItems {
			ref.cursors[index] = listCursor{v, expr}
			return out, true, nil
		}
		ref.addresses[addr] = index
		// A fresh child reader gives each element the common preview budget.
		if index >= offset {
			head, headPath := r.field(node.Children[0], path, "head")
			out = append(out, c.inspectValue(strconv.Itoa(index), "", valueRef{frame: ref.frame, gid: ref.gid, value: head, expr: anchor(head, headPath)}))
		}
		v, expr = r.field(node.Children[0], path, "tail")
		if v.Unreadable != "" {
			return out, false, fmt.Errorf("list tail: %s", v.Unreadable)
		}
		expr = anchor(v, expr)
		index++
		if index%previewItems == 0 {
			ref.cursors[index] = listCursor{v, expr}
		}
	}
	ref.cursors[index] = listCursor{v, expr}
	return out, true, fmt.Errorf("list traversal limit at offset %d; expand from this offset to continue", index)
}
