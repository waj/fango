// Package objectcodec encodes compiler-owned typed graphs without serializing
// callbacks or implementation structs. Pointer nodes are reference-numbered,
// preserving sharing and cycles across types, declaration metadata, and Core.
package objectcodec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"sort"
	"strconv"

	"github.com/waj/fango/internal/source"
)

const maxNodes = 1_000_000

type Context struct {
	Types   map[string]reflect.Type
	Sources map[string]*source.File
}

type graph struct {
	Root  value   `json:"root"`
	Nodes []value `json:"nodes,omitempty"`
}

type value struct {
	Kind    string  `json:"kind"`
	Type    string  `json:"type,omitempty"`
	Ref     int     `json:"ref,omitempty"`
	String  string  `json:"string,omitempty"`
	Integer string  `json:"integer,omitempty"`
	Bits    string  `json:"bits,omitempty"`
	Boolean bool    `json:"boolean,omitempty"`
	Fields  []field `json:"fields,omitempty"`
	Items   []value `json:"items,omitempty"`
	Entries []entry `json:"entries,omitempty"`
	Source  string  `json:"source,omitempty"`
	Start   int     `json:"start,omitempty"`
	End     int     `json:"end,omitempty"`
	Text    string  `json:"text,omitempty"`
	Before  string  `json:"before,omitempty"`
	After   string  `json:"after,omitempty"`
}

type field struct {
	Name  string `json:"name"`
	Value value  `json:"value"`
}
type entry struct {
	Key   value `json:"key"`
	Value value `json:"value"`
}

var spanType = reflect.TypeOf(source.Span{})

func typeName(t reflect.Type) string {
	if t.Kind() == reflect.Pointer {
		return "*" + typeName(t.Elem())
	}
	return t.PkgPath() + "." + t.Name()
}

func Encode(input any) ([]byte, error) {
	e := encoder{seen: map[any]int{}}
	root, err := e.encode(reflect.ValueOf(input))
	if err != nil {
		return nil, err
	}
	return json.Marshal(graph{Root: root, Nodes: e.nodes})
}

type encoder struct {
	seen  map[any]int
	nodes []value
}

func (e *encoder) encode(v reflect.Value) (value, error) {
	if !v.IsValid() {
		return value{Kind: "nil"}, nil
	}
	if v.Kind() == reflect.Interface {
		if v.IsNil() {
			return value{Kind: "nil"}, nil
		}
		out, err := e.encode(v.Elem())
		if err == nil {
			out.Type = typeName(v.Elem().Type())
		}
		return out, err
	}
	if v.Type() == spanType {
		sp := v.Interface().(source.Span)
		if sp.File == nil {
			return value{Kind: "span"}, nil
		}
		if sp.Start < 0 || sp.End < sp.Start || sp.End > len(sp.File.Content) {
			return value{}, fmt.Errorf("invalid span %s[%d:%d]", sp.File.Name, sp.Start, sp.End)
		}
		before := max(0, sp.Start-32)
		after := min(len(sp.File.Content), sp.End+32)
		return value{Kind: "span", Source: sp.File.Name, Start: sp.Start, End: sp.End,
			Text: string(sp.File.Content[sp.Start:sp.End]), Before: string(sp.File.Content[before:sp.Start]), After: string(sp.File.Content[sp.End:after])}, nil
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return value{Kind: "nil"}, nil
		}
		key := v.Interface()
		if id, ok := e.seen[key]; ok {
			return value{Kind: "ref", Ref: id}, nil
		}
		if len(e.nodes) >= maxNodes {
			return value{}, fmt.Errorf("object graph exceeds %d nodes", maxNodes)
		}
		id := len(e.nodes) + 1
		e.seen[key] = id
		e.nodes = append(e.nodes, value{Kind: "pending"})
		n, err := e.encode(v.Elem())
		if err != nil {
			return value{}, err
		}
		if n.Type == "" {
			n.Type = typeName(v.Type())
		}
		e.nodes[id-1] = n
		return value{Kind: "ref", Ref: id}, nil
	case reflect.Struct:
		out := value{Kind: "struct", Type: typeName(v.Type())}
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if f.Tag.Get("object") == "omit" {
				continue
			}
			if f.PkgPath != "" {
				return value{}, fmt.Errorf("unsupported private field %s.%s", v.Type(), f.Name)
			}
			fv, err := e.encode(v.Field(i))
			if err != nil {
				return value{}, fmt.Errorf("%s.%s: %w", v.Type(), f.Name, err)
			}
			out.Fields = append(out.Fields, field{Name: f.Name, Value: fv})
		}
		return out, nil
	case reflect.Slice:
		if v.IsNil() {
			return value{Kind: "nil"}, nil
		}
		out := value{Kind: "slice", Items: make([]value, v.Len())}
		for i := range out.Items {
			x, err := e.encode(v.Index(i))
			if err != nil {
				return value{}, err
			}
			out.Items[i] = x
		}
		return out, nil
	case reflect.Map:
		if v.IsNil() {
			return value{Kind: "nil"}, nil
		}
		keys := v.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i].Interface()) < fmt.Sprint(keys[j].Interface()) })
		out := value{Kind: "map", Entries: make([]entry, len(keys))}
		for i, k := range keys {
			key, err := e.encode(k)
			if err != nil {
				return value{}, err
			}
			val, err := e.encode(v.MapIndex(k))
			if err != nil {
				return value{}, err
			}
			out.Entries[i] = entry{Key: key, Value: val}
		}
		return out, nil
	case reflect.String:
		return value{Kind: "string", String: v.String()}, nil
	case reflect.Bool:
		return value{Kind: "bool", Boolean: v.Bool()}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value{Kind: "int", Integer: strconv.FormatInt(v.Int(), 10)}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return value{Kind: "uint", Integer: strconv.FormatUint(v.Uint(), 10)}, nil
	case reflect.Float32, reflect.Float64:
		return value{Kind: "float", Bits: fmt.Sprintf("%016x", math.Float64bits(v.Convert(reflect.TypeOf(float64(0))).Float()))}, nil
	default:
		return value{}, fmt.Errorf("unsupported object value %s", v.Type())
	}
}

func Decode(data []byte, output any, context Context) error {
	p := reflect.ValueOf(output)
	if p.Kind() != reflect.Pointer || p.IsNil() {
		return fmt.Errorf("decode target must be a non-nil pointer")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var g graph
	if err := dec.Decode(&g); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("trailing object data")
	}
	if len(g.Nodes) > maxNodes {
		return fmt.Errorf("object graph exceeds %d nodes", maxNodes)
	}
	d := decoder{nodes: g.Nodes, values: make([]reflect.Value, len(g.Nodes)), context: context, filling: make([]bool, len(g.Nodes)), filled: make([]bool, len(g.Nodes))}
	v, err := d.decode(g.Root, p.Elem().Type())
	if err != nil {
		return err
	}
	p.Elem().Set(v)
	return nil
}

type decoder struct {
	nodes           []value
	values          []reflect.Value
	filling, filled []bool
	context         Context
}

func (d *decoder) concrete(tag string) (reflect.Type, error) {
	t := d.context.Types[tag]
	if t == nil {
		return nil, fmt.Errorf("unknown object type %q", tag)
	}
	return t, nil
}

func (d *decoder) decode(in value, target reflect.Type) (reflect.Value, error) {
	if in.Kind == "nil" {
		return reflect.Zero(target), nil
	}
	if target.Kind() == reflect.Interface {
		t, err := d.concrete(in.Type)
		if err != nil {
			return reflect.Value{}, err
		}
		v, err := d.decode(in, t)
		if err != nil {
			return reflect.Value{}, err
		}
		if !v.Type().AssignableTo(target) {
			return reflect.Value{}, fmt.Errorf("%s does not implement %s", v.Type(), target)
		}
		return v, nil
	}
	if target == spanType {
		if in.Kind != "span" {
			return reflect.Value{}, fmt.Errorf("want span, got %s", in.Kind)
		}
		if in.Source == "" {
			return reflect.Zero(target), nil
		}
		f := d.context.Sources[in.Source]
		if f == nil {
			return reflect.Value{}, fmt.Errorf("unknown source provenance %q", in.Source)
		}
		start, end, ok := relocateSpan(in, f.Content)
		if !ok {
			return reflect.Value{}, fmt.Errorf("cannot relocate span %s[%d:%d] in current source", in.Source, in.Start, in.End)
		}
		return reflect.ValueOf(source.Span{File: f, Start: start, End: end}), nil
	}
	if in.Kind == "ref" {
		if target.Kind() != reflect.Pointer || in.Ref < 1 || in.Ref > len(d.nodes) {
			return reflect.Value{}, fmt.Errorf("invalid reference %d for %s", in.Ref, target)
		}
		i := in.Ref - 1
		if d.values[i].IsValid() && d.values[i].Type() != target {
			return reflect.Value{}, fmt.Errorf("reference %d type mismatch: %s and %s", in.Ref, d.values[i].Type(), target)
		}
		if !d.values[i].IsValid() {
			d.values[i] = reflect.New(target.Elem())
		}
		if !d.filled[i] && !d.filling[i] {
			d.filling[i] = true
			v, err := d.decode(d.nodes[i], target.Elem())
			if err != nil {
				return reflect.Value{}, fmt.Errorf("reference %d: %w", in.Ref, err)
			}
			d.values[i].Elem().Set(v)
			d.filling[i], d.filled[i] = false, true
		}
		return d.values[i], nil
	}
	switch target.Kind() {
	case reflect.Struct:
		if in.Kind != "struct" {
			return reflect.Value{}, fmt.Errorf("want struct %s, got %s", target, in.Kind)
		}
		if in.Type != "" && in.Type != typeName(target) {
			return reflect.Value{}, fmt.Errorf("struct type mismatch: %q, want %q", in.Type, typeName(target))
		}
		out := reflect.New(target).Elem()
		fieldIndex := 0
		for i := 0; i < target.NumField(); i++ {
			want := target.Field(i)
			if want.Tag.Get("object") == "omit" {
				continue
			}
			if fieldIndex >= len(in.Fields) || in.Fields[fieldIndex].Name != want.Name || want.PkgPath != "" {
				return reflect.Value{}, fmt.Errorf("missing or unexpected field %q in %s", want.Name, target)
			}
			f := in.Fields[fieldIndex]
			v, err := d.decode(f.Value, want.Type)
			if err != nil {
				return reflect.Value{}, fmt.Errorf("%s.%s: %w", target, want.Name, err)
			}
			out.Field(i).Set(v)
			fieldIndex++
		}
		if fieldIndex != len(in.Fields) {
			return reflect.Value{}, fmt.Errorf("extra fields in %s", target)
		}
		return out, nil
	case reflect.Slice:
		if in.Kind != "slice" {
			return reflect.Value{}, fmt.Errorf("want slice, got %s", in.Kind)
		}
		out := reflect.MakeSlice(target, len(in.Items), len(in.Items))
		for i, item := range in.Items {
			v, err := d.decode(item, target.Elem())
			if err != nil {
				return reflect.Value{}, err
			}
			out.Index(i).Set(v)
		}
		return out, nil
	case reflect.Map:
		if in.Kind != "map" {
			return reflect.Value{}, fmt.Errorf("want map, got %s", in.Kind)
		}
		out := reflect.MakeMapWithSize(target, len(in.Entries))
		for _, item := range in.Entries {
			k, err := d.decode(item.Key, target.Key())
			if err != nil {
				return reflect.Value{}, err
			}
			v, err := d.decode(item.Value, target.Elem())
			if err != nil {
				return reflect.Value{}, err
			}
			if out.MapIndex(k).IsValid() {
				return reflect.Value{}, fmt.Errorf("duplicate map key %v", k.Interface())
			}
			out.SetMapIndex(k, v)
		}
		return out, nil
	case reflect.String:
		if in.Kind != "string" {
			return reflect.Value{}, fmt.Errorf("want string, got %s", in.Kind)
		}
		v := reflect.New(target).Elem()
		v.SetString(in.String)
		return v, nil
	case reflect.Bool:
		if in.Kind != "bool" {
			return reflect.Value{}, fmt.Errorf("want bool, got %s", in.Kind)
		}
		v := reflect.New(target).Elem()
		v.SetBool(in.Boolean)
		return v, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if in.Kind != "int" {
			return reflect.Value{}, fmt.Errorf("want int, got %s", in.Kind)
		}
		n, err := strconv.ParseInt(in.Integer, 10, target.Bits())
		if err != nil {
			return reflect.Value{}, err
		}
		v := reflect.New(target).Elem()
		v.SetInt(n)
		return v, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if in.Kind != "uint" {
			return reflect.Value{}, fmt.Errorf("want uint, got %s", in.Kind)
		}
		n, err := strconv.ParseUint(in.Integer, 10, target.Bits())
		if err != nil {
			return reflect.Value{}, err
		}
		v := reflect.New(target).Elem()
		v.SetUint(n)
		return v, nil
	case reflect.Float32, reflect.Float64:
		if in.Kind != "float" || len(in.Bits) != 16 {
			return reflect.Value{}, fmt.Errorf("invalid float encoding")
		}
		n, err := strconv.ParseUint(in.Bits, 16, 64)
		if err != nil {
			return reflect.Value{}, err
		}
		v := reflect.New(target).Elem()
		v.SetFloat(math.Float64frombits(n))
		return v, nil
	default:
		return reflect.Value{}, fmt.Errorf("unsupported decode target %s", target)
	}
}

func relocateSpan(in value, content []byte) (int, int, bool) {
	if in.Start >= 0 && in.End >= in.Start && in.End <= len(content) && string(content[in.Start:in.End]) == in.Text {
		return in.Start, in.End, true
	}
	text := []byte(in.Text)
	start, found := 0, -1
	for start <= len(content) {
		i := bytes.Index(content[start:], text)
		if i < 0 {
			break
		}
		i += start
		beforeStart := max(0, i-len(in.Before))
		afterEnd := min(len(content), i+len(text)+len(in.After))
		beforeOK := string(content[beforeStart:i]) == in.Before[len(in.Before)-(i-beforeStart):]
		afterOK := string(content[i+len(text):afterEnd]) == in.After[:afterEnd-(i+len(text))]
		if beforeOK && afterOK {
			if found >= 0 {
				return 0, 0, false
			}
			found = i
		}
		start = i + 1
		if len(text) == 0 && start > len(content) {
			break
		}
	}
	return found, found + len(text), found >= 0
}

func Registry(values ...any) map[string]reflect.Type {
	r := map[string]reflect.Type{}
	for _, value := range values {
		t := reflect.TypeOf(value)
		r[typeName(t)] = t
	}
	return r
}
