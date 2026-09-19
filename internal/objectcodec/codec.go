// Package objectcodec encodes compiler-owned typed graphs without serializing
// callbacks or implementation structs. Pointer nodes are reference-numbered,
// preserving sharing and cycles across types, declaration metadata, and Core.
//
// The encoding is a string pool, a table of node sizes, and tagged values, so
// a decoder walks bytes straight into typed values without materializing an
// intermediate representation of the graph, and can reach any node without
// reading the ones before it.
package objectcodec

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"reflect"
	"sort"

	"github.com/waj/fango/internal/source"
)

const (
	maxNodes = 1_000_000
	magic    = "FGOB\x01"
)

// Value tags. A value carries a type only where the decoder consults one: a
// struct always names its type, and an interface element or a node body that
// is not a struct is wrapped so its concrete type survives.
const (
	tagNil byte = iota
	tagRef
	tagStruct
	tagSlice
	tagMap
	tagString
	tagFalse
	tagTrue
	tagInt
	tagUint
	tagFloat
	tagSpanNil
	tagSpan
	tagTyped
)

type Context struct {
	Types   map[string]reflect.Type
	Sources map[string]*source.File
}

var spanType = reflect.TypeOf(source.Span{})

func typeName(t reflect.Type) string {
	if t.Kind() == reflect.Pointer {
		return "*" + typeName(t.Elem())
	}
	return t.PkgPath() + "." + t.Name()
}

// Section is one named root of an object. Structure shared between sections is
// stored once, and decoding a section touches only the nodes it reaches, so a
// reader that never needs a section never pays for it.
type Section struct {
	Name  string
	Value any
}

func Encode(input any) ([]byte, error) {
	return EncodeSections(Section{Value: input})
}

func EncodeSections(sections ...Section) ([]byte, error) {
	e := &encoder{index: map[string]int{}, seen: map[any]int{}}
	named := map[string]bool{}
	roots := make([]root, len(sections))
	for i, section := range sections {
		if named[section.Name] {
			return nil, fmt.Errorf("duplicate section %q", section.Name)
		}
		named[section.Name] = true
		body, _, err := e.value(nil, reflect.ValueOf(section.Value))
		if err != nil {
			return nil, err
		}
		roots[i] = root{name: e.str(section.Name), body: body}
	}
	return e.finish(roots), nil
}

type root struct {
	name int
	body []byte
}

type encoder struct {
	strings []string
	index   map[string]int
	nodes   [][]byte
	seen    map[any]int
}

func (e *encoder) str(s string) int {
	if i, ok := e.index[s]; ok {
		return i
	}
	i := len(e.strings)
	e.strings = append(e.strings, s)
	e.index[s] = i
	return i
}

// finish lays the value area out as the nodes in order followed by the roots in
// order, and records every size, so a reader validates the table against the
// payload length before decoding any of it.
func (e *encoder) finish(roots []root) []byte {
	out := make([]byte, 0, len(magic)+16)
	out = append(out, magic...)
	out = binary.AppendUvarint(out, uint64(len(e.strings)))
	for _, s := range e.strings {
		out = binary.AppendUvarint(out, uint64(len(s)))
		out = append(out, s...)
	}
	out = binary.AppendUvarint(out, uint64(len(e.nodes)))
	for _, node := range e.nodes {
		out = binary.AppendUvarint(out, uint64(len(node)))
	}
	out = binary.AppendUvarint(out, uint64(len(roots)))
	for _, r := range roots {
		out = binary.AppendUvarint(out, uint64(r.name))
		out = binary.AppendUvarint(out, uint64(len(r.body)))
	}
	for _, node := range e.nodes {
		out = append(out, node...)
	}
	for _, r := range roots {
		out = append(out, r.body...)
	}
	return out
}

// value appends v and reports whether the appended value names its own type.
func (e *encoder) value(dst []byte, v reflect.Value) ([]byte, bool, error) {
	if !v.IsValid() {
		return append(dst, tagNil), false, nil
	}
	if v.Kind() == reflect.Interface {
		if v.IsNil() {
			return append(dst, tagNil), false, nil
		}
		elem := v.Elem()
		if elem.Kind() == reflect.Struct && elem.Type() != spanType {
			return e.value(dst, elem) // the struct names the same type itself
		}
		dst = append(dst, tagTyped)
		dst = binary.AppendUvarint(dst, uint64(e.str(typeName(elem.Type()))))
		dst, _, err := e.value(dst, elem)
		return dst, true, err
	}
	if v.Type() == spanType {
		out, err := e.span(dst, v.Interface().(source.Span))
		return out, false, err
	}
	switch v.Kind() {
	case reflect.Pointer:
		return e.pointer(dst, v)
	case reflect.Struct:
		// The field count is a property of the type, so it is known before any
		// field is encoded and needs no placeholder to patch afterwards.
		fields := 0
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).Tag.Get("object") != "omit" {
				fields++
			}
		}
		dst = append(dst, tagStruct)
		dst = binary.AppendUvarint(dst, uint64(e.str(typeName(v.Type()))))
		dst = binary.AppendUvarint(dst, uint64(fields))
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if f.Tag.Get("object") == "omit" {
				continue
			}
			if f.PkgPath != "" {
				return nil, false, fmt.Errorf("unsupported private field %s.%s", v.Type(), f.Name)
			}
			dst = binary.AppendUvarint(dst, uint64(e.str(f.Name)))
			next, _, err := e.value(dst, v.Field(i))
			if err != nil {
				return nil, false, fmt.Errorf("%s.%s: %w", v.Type(), f.Name, err)
			}
			dst = next
		}
		return dst, true, nil
	case reflect.Slice:
		if v.IsNil() {
			return append(dst, tagNil), false, nil
		}
		dst = append(dst, tagSlice)
		dst = binary.AppendUvarint(dst, uint64(v.Len()))
		for i := 0; i < v.Len(); i++ {
			next, _, err := e.value(dst, v.Index(i))
			if err != nil {
				return nil, false, err
			}
			dst = next
		}
		return dst, false, nil
	case reflect.Map:
		if v.IsNil() {
			return append(dst, tagNil), false, nil
		}
		keys := v.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i].Interface()) < fmt.Sprint(keys[j].Interface()) })
		dst = append(dst, tagMap)
		dst = binary.AppendUvarint(dst, uint64(len(keys)))
		for _, k := range keys {
			next, _, err := e.value(dst, k)
			if err != nil {
				return nil, false, err
			}
			if dst, _, err = e.value(next, v.MapIndex(k)); err != nil {
				return nil, false, err
			}
		}
		return dst, false, nil
	case reflect.String:
		dst = append(dst, tagString)
		return binary.AppendUvarint(dst, uint64(e.str(v.String()))), false, nil
	case reflect.Bool:
		if v.Bool() {
			return append(dst, tagTrue), false, nil
		}
		return append(dst, tagFalse), false, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		dst = append(dst, tagInt)
		n := v.Int()
		return binary.AppendUvarint(dst, uint64(n<<1)^uint64(n>>63)), false, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		dst = append(dst, tagUint)
		return binary.AppendUvarint(dst, v.Uint()), false, nil
	case reflect.Float32, reflect.Float64:
		dst = append(dst, tagFloat)
		bits := math.Float64bits(v.Convert(reflect.TypeOf(float64(0))).Float())
		return binary.LittleEndian.AppendUint64(dst, bits), false, nil
	default:
		return nil, false, fmt.Errorf("unsupported object value %s", v.Type())
	}
}

func (e *encoder) pointer(dst []byte, v reflect.Value) ([]byte, bool, error) {
	if v.IsNil() {
		return append(dst, tagNil), false, nil
	}
	key := v.Interface()
	if id, ok := e.seen[key]; ok {
		dst = append(dst, tagRef)
		return binary.AppendUvarint(dst, uint64(id)), false, nil
	}
	if len(e.nodes) >= maxNodes {
		return nil, false, fmt.Errorf("object graph exceeds %d nodes", maxNodes)
	}
	id := len(e.nodes)
	e.seen[key] = id
	e.nodes = append(e.nodes, nil)
	body, typed, err := e.value(nil, v.Elem())
	if err != nil {
		return nil, false, err
	}
	if !typed {
		head := binary.AppendUvarint([]byte{tagTyped}, uint64(e.str(typeName(v.Type()))))
		body = append(head, body...)
	}
	e.nodes[id] = body
	dst = append(dst, tagRef)
	return binary.AppendUvarint(dst, uint64(id)), false, nil
}

func (e *encoder) span(dst []byte, sp source.Span) ([]byte, error) {
	if sp.File == nil {
		return append(dst, tagSpanNil), nil
	}
	if sp.Start < 0 || sp.End < sp.Start || sp.End > len(sp.File.Content) {
		return nil, fmt.Errorf("invalid span %s[%d:%d]", sp.File.Name, sp.Start, sp.End)
	}
	before := max(0, sp.Start-32)
	after := min(len(sp.File.Content), sp.End+32)
	dst = append(dst, tagSpan)
	dst = binary.AppendUvarint(dst, uint64(e.str(sp.File.Name)))
	dst = binary.AppendUvarint(dst, uint64(sp.Start))
	dst = binary.AppendUvarint(dst, uint64(sp.End))
	dst = binary.AppendUvarint(dst, uint64(e.str(string(sp.File.Content[sp.Start:sp.End]))))
	dst = binary.AppendUvarint(dst, uint64(e.str(string(sp.File.Content[before:sp.Start]))))
	return binary.AppendUvarint(dst, uint64(e.str(string(sp.File.Content[sp.End:after])))), nil
}

func Decode(data []byte, output any, context Context) error {
	d, err := NewDecoder(data, context)
	if err != nil {
		return err
	}
	return d.Section("", output)
}

// Decoder reads an object's sections independently, sharing one node pool
// across them. A node decoded for one section is not decoded again for
// another, so the two see the same pointers for the structure they share.
type Decoder struct{ d *decoder }

func NewDecoder(data []byte, context Context) (*Decoder, error) {
	d, err := newDecoder(data, context)
	if err != nil {
		return nil, err
	}
	return &Decoder{d: d}, nil
}

// SectionSize is the encoded byte size of one section, or zero if the object
// has no such section. It reports what a deferred section costs to carry
// without decoding it.
func (dec *Decoder) SectionSize(name string) int {
	return dec.d.roots[name].size
}

func (dec *Decoder) Section(name string, output any) error {
	p := reflect.ValueOf(output)
	if p.Kind() != reflect.Pointer || p.IsNil() {
		return fmt.Errorf("decode target must be a non-nil pointer")
	}
	return dec.d.section(name, p)
}

type span struct{ at, size int }

type decoder struct {
	strings         []string
	area            []byte
	nodes           []span
	roots           map[string]span
	values          []reflect.Value
	filling, filled []bool
	context         Context
}

func newDecoder(data []byte, context Context) (*decoder, error) {
	if !bytes.HasPrefix(data, []byte(magic)) {
		return nil, fmt.Errorf("unsupported object encoding")
	}
	c := &cursor{data: data, at: len(magic)}
	count, err := c.count()
	if err != nil {
		return nil, err
	}
	d := &decoder{context: context, strings: make([]string, count)}
	for i := range d.strings {
		n, err := c.count()
		if err != nil {
			return nil, err
		}
		raw, err := c.take(n)
		if err != nil {
			return nil, err
		}
		d.strings[i] = string(raw)
	}
	nodeCount, err := c.count()
	if err != nil {
		return nil, err
	}
	if nodeCount > maxNodes {
		return nil, fmt.Errorf("object graph exceeds %d nodes", maxNodes)
	}
	d.nodes = make([]span, nodeCount)
	total := 0
	for i := range d.nodes {
		size, err := c.count()
		if err != nil {
			return nil, err
		}
		d.nodes[i] = span{at: total, size: size}
		total += size
	}
	rootCount, err := c.count()
	if err != nil {
		return nil, err
	}
	d.roots = make(map[string]span, rootCount)
	for i := 0; i < rootCount; i++ {
		name, err := c.count()
		if err != nil {
			return nil, err
		}
		size, err := c.count()
		if err != nil {
			return nil, err
		}
		if name >= len(d.strings) {
			return nil, fmt.Errorf("invalid section name")
		}
		if _, repeated := d.roots[d.strings[name]]; repeated {
			return nil, fmt.Errorf("duplicate section %q", d.strings[name])
		}
		d.roots[d.strings[name]] = span{at: total, size: size}
		total += size
	}
	d.area = data[c.at:]
	if total != len(d.area) {
		return nil, fmt.Errorf("object value area is %d bytes, table describes %d", len(d.area), total)
	}
	d.values = make([]reflect.Value, nodeCount)
	d.filling, d.filled = make([]bool, nodeCount), make([]bool, nodeCount)
	return d, nil
}

// section decodes one named root into p, a non-nil pointer to the target.
func (d *decoder) section(name string, p reflect.Value) error {
	at, ok := d.roots[name]
	if !ok {
		return fmt.Errorf("object has no section %q", name)
	}
	c := &cursor{data: d.area, at: at.at}
	v, err := d.decode(c, p.Elem().Type())
	if err != nil {
		return err
	}
	if c.at != at.at+at.size {
		return fmt.Errorf("section %q ends at %d, want %d", name, c.at, at.at+at.size)
	}
	p.Elem().Set(v)
	return nil
}

type cursor struct {
	data []byte
	at   int
}

func (c *cursor) tag() (byte, error) {
	if c.at >= len(c.data) {
		return 0, fmt.Errorf("object value ends early")
	}
	b := c.data[c.at]
	c.at++
	return b, nil
}

func (c *cursor) uvarint() (uint64, error) {
	n, size := binary.Uvarint(c.data[c.at:])
	if size <= 0 {
		return 0, fmt.Errorf("malformed object varint")
	}
	c.at += size
	return n, nil
}

// count reads a length that must be representable and cannot exceed the bytes
// left to read, so a damaged table cannot ask for an enormous allocation.
func (c *cursor) count() (int, error) {
	n, err := c.uvarint()
	if err != nil {
		return 0, err
	}
	if n > uint64(len(c.data)-c.at) {
		return 0, fmt.Errorf("object length %d exceeds remaining %d bytes", n, len(c.data)-c.at)
	}
	return int(n), nil
}

func (c *cursor) take(n int) ([]byte, error) {
	if n < 0 || c.at+n > len(c.data) {
		return nil, fmt.Errorf("object value ends early")
	}
	out := c.data[c.at : c.at+n]
	c.at += n
	return out, nil
}

func (d *decoder) str(i uint64) (string, error) {
	if i >= uint64(len(d.strings)) {
		return "", fmt.Errorf("invalid object string %d", i)
	}
	return d.strings[i], nil
}

func (d *decoder) concrete(tag string) (reflect.Type, error) {
	t := d.context.Types[tag]
	if t == nil {
		return nil, fmt.Errorf("unknown object type %q", tag)
	}
	return t, nil
}

func (d *decoder) decode(c *cursor, target reflect.Type) (reflect.Value, error) {
	tag, err := c.tag()
	if err != nil {
		return reflect.Value{}, err
	}
	if tag == tagNil {
		return reflect.Zero(target), nil
	}
	if tag == tagTyped {
		index, err := c.uvarint()
		if err != nil {
			return reflect.Value{}, err
		}
		name, err := d.str(index)
		if err != nil {
			return reflect.Value{}, err
		}
		if target.Kind() == reflect.Interface {
			concrete, err := d.concrete(name)
			if err != nil {
				return reflect.Value{}, err
			}
			v, err := d.decode(c, concrete)
			if err != nil {
				return reflect.Value{}, err
			}
			if !v.Type().AssignableTo(target) {
				return reflect.Value{}, fmt.Errorf("%s does not implement %s", v.Type(), target)
			}
			return v, nil
		}
		return d.decode(c, target)
	}
	if target.Kind() == reflect.Interface {
		if tag != tagStruct {
			return reflect.Value{}, fmt.Errorf("untyped value cannot satisfy %s", target)
		}
		at := c.at - 1
		index, err := c.uvarint()
		if err != nil {
			return reflect.Value{}, err
		}
		name, err := d.str(index)
		if err != nil {
			return reflect.Value{}, err
		}
		concrete, err := d.concrete(name)
		if err != nil {
			return reflect.Value{}, err
		}
		c.at = at
		v, err := d.decode(c, concrete)
		if err != nil {
			return reflect.Value{}, err
		}
		if !v.Type().AssignableTo(target) {
			return reflect.Value{}, fmt.Errorf("%s does not implement %s", v.Type(), target)
		}
		return v, nil
	}
	if target == spanType {
		return d.span(c, tag)
	}
	if tag == tagRef {
		return d.ref(c, target)
	}
	switch target.Kind() {
	case reflect.Struct:
		return d.structure(c, tag, target)
	case reflect.Slice:
		if tag != tagSlice {
			return reflect.Value{}, fmt.Errorf("want slice, got tag %d", tag)
		}
		n, err := c.count()
		if err != nil {
			return reflect.Value{}, err
		}
		out := reflect.MakeSlice(target, n, n)
		for i := 0; i < n; i++ {
			v, err := d.decode(c, target.Elem())
			if err != nil {
				return reflect.Value{}, err
			}
			out.Index(i).Set(v)
		}
		return out, nil
	case reflect.Map:
		if tag != tagMap {
			return reflect.Value{}, fmt.Errorf("want map, got tag %d", tag)
		}
		n, err := c.count()
		if err != nil {
			return reflect.Value{}, err
		}
		out := reflect.MakeMapWithSize(target, n)
		for i := 0; i < n; i++ {
			k, err := d.decode(c, target.Key())
			if err != nil {
				return reflect.Value{}, err
			}
			v, err := d.decode(c, target.Elem())
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
		if tag != tagString {
			return reflect.Value{}, fmt.Errorf("want string, got tag %d", tag)
		}
		index, err := c.uvarint()
		if err != nil {
			return reflect.Value{}, err
		}
		s, err := d.str(index)
		if err != nil {
			return reflect.Value{}, err
		}
		v := reflect.New(target).Elem()
		v.SetString(s)
		return v, nil
	case reflect.Bool:
		if tag != tagTrue && tag != tagFalse {
			return reflect.Value{}, fmt.Errorf("want bool, got tag %d", tag)
		}
		v := reflect.New(target).Elem()
		v.SetBool(tag == tagTrue)
		return v, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if tag != tagInt {
			return reflect.Value{}, fmt.Errorf("want int, got tag %d", tag)
		}
		raw, err := c.uvarint()
		if err != nil {
			return reflect.Value{}, err
		}
		n := int64(raw>>1) ^ -int64(raw&1)
		v := reflect.New(target).Elem()
		if v.OverflowInt(n) {
			return reflect.Value{}, fmt.Errorf("%d overflows %s", n, target)
		}
		v.SetInt(n)
		return v, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if tag != tagUint {
			return reflect.Value{}, fmt.Errorf("want uint, got tag %d", tag)
		}
		n, err := c.uvarint()
		if err != nil {
			return reflect.Value{}, err
		}
		v := reflect.New(target).Elem()
		if v.OverflowUint(n) {
			return reflect.Value{}, fmt.Errorf("%d overflows %s", n, target)
		}
		v.SetUint(n)
		return v, nil
	case reflect.Float32, reflect.Float64:
		if tag != tagFloat {
			return reflect.Value{}, fmt.Errorf("want float, got tag %d", tag)
		}
		raw, err := c.take(8)
		if err != nil {
			return reflect.Value{}, err
		}
		v := reflect.New(target).Elem()
		v.SetFloat(math.Float64frombits(binary.LittleEndian.Uint64(raw)))
		return v, nil
	default:
		return reflect.Value{}, fmt.Errorf("unsupported decode target %s", target)
	}
}

func (d *decoder) structure(c *cursor, tag byte, target reflect.Type) (reflect.Value, error) {
	if tag != tagStruct {
		return reflect.Value{}, fmt.Errorf("want struct %s, got tag %d", target, tag)
	}
	index, err := c.uvarint()
	if err != nil {
		return reflect.Value{}, err
	}
	name, err := d.str(index)
	if err != nil {
		return reflect.Value{}, err
	}
	if name != typeName(target) {
		return reflect.Value{}, fmt.Errorf("struct type mismatch: %q, want %q", name, typeName(target))
	}
	count, err := c.count()
	if err != nil {
		return reflect.Value{}, err
	}
	out := reflect.New(target).Elem()
	seen := 0
	for i := 0; i < target.NumField(); i++ {
		want := target.Field(i)
		if want.Tag.Get("object") == "omit" {
			continue
		}
		if seen >= count || want.PkgPath != "" {
			return reflect.Value{}, fmt.Errorf("missing or unexpected field %q in %s", want.Name, target)
		}
		index, err := c.uvarint()
		if err != nil {
			return reflect.Value{}, err
		}
		got, err := d.str(index)
		if err != nil {
			return reflect.Value{}, err
		}
		if got != want.Name {
			return reflect.Value{}, fmt.Errorf("missing or unexpected field %q in %s", want.Name, target)
		}
		v, err := d.decode(c, want.Type)
		if err != nil {
			return reflect.Value{}, fmt.Errorf("%s.%s: %w", target, want.Name, err)
		}
		out.Field(i).Set(v)
		seen++
	}
	if seen != count {
		return reflect.Value{}, fmt.Errorf("extra fields in %s", target)
	}
	return out, nil
}

func (d *decoder) ref(c *cursor, target reflect.Type) (reflect.Value, error) {
	raw, err := c.uvarint()
	if err != nil {
		return reflect.Value{}, err
	}
	if target.Kind() != reflect.Pointer || raw >= uint64(len(d.nodes)) {
		return reflect.Value{}, fmt.Errorf("invalid reference %d for %s", raw, target)
	}
	i := int(raw)
	if d.values[i].IsValid() && d.values[i].Type() != target {
		return reflect.Value{}, fmt.Errorf("reference %d type mismatch: %s and %s", i, d.values[i].Type(), target)
	}
	if !d.values[i].IsValid() {
		d.values[i] = reflect.New(target.Elem())
	}
	if !d.filled[i] && !d.filling[i] {
		d.filling[i] = true
		node := &cursor{data: d.area, at: d.nodes[i].at}
		v, err := d.decode(node, target.Elem())
		if err != nil {
			return reflect.Value{}, fmt.Errorf("reference %d: %w", i, err)
		}
		if node.at != d.nodes[i].at+d.nodes[i].size {
			return reflect.Value{}, fmt.Errorf("reference %d ends at %d, want %d", i, node.at, d.nodes[i].at+d.nodes[i].size)
		}
		d.values[i].Elem().Set(v)
		d.filling[i], d.filled[i] = false, true
	}
	return d.values[i], nil
}

func (d *decoder) span(c *cursor, tag byte) (reflect.Value, error) {
	if tag == tagSpanNil {
		return reflect.Zero(spanType), nil
	}
	if tag != tagSpan {
		return reflect.Value{}, fmt.Errorf("want span, got tag %d", tag)
	}
	read := func() (string, error) {
		index, err := c.uvarint()
		if err != nil {
			return "", err
		}
		return d.str(index)
	}
	name, err := read()
	if err != nil {
		return reflect.Value{}, err
	}
	start, err := c.uvarint()
	if err != nil {
		return reflect.Value{}, err
	}
	end, err := c.uvarint()
	if err != nil {
		return reflect.Value{}, err
	}
	text, err := read()
	if err != nil {
		return reflect.Value{}, err
	}
	before, err := read()
	if err != nil {
		return reflect.Value{}, err
	}
	after, err := read()
	if err != nil {
		return reflect.Value{}, err
	}
	f := d.context.Sources[name]
	if f == nil {
		return reflect.Value{}, fmt.Errorf("unknown source provenance %q", name)
	}
	at, to, ok := relocateSpan(int(start), int(end), text, before, after, f.Content)
	if !ok {
		return reflect.Value{}, fmt.Errorf("cannot relocate span %s[%d:%d] in current source", name, start, end)
	}
	return reflect.ValueOf(source.Span{File: f, Start: at, End: to}), nil
}

func relocateSpan(start, end int, text, before, after string, content []byte) (int, int, bool) {
	if start >= 0 && end >= start && end <= len(content) && string(content[start:end]) == text {
		return start, end, true
	}
	needle := []byte(text)
	at, found := 0, -1
	for at <= len(content) {
		i := bytes.Index(content[at:], needle)
		if i < 0 {
			break
		}
		i += at
		beforeStart := max(0, i-len(before))
		afterEnd := min(len(content), i+len(needle)+len(after))
		beforeOK := string(content[beforeStart:i]) == before[len(before)-(i-beforeStart):]
		afterOK := string(content[i+len(needle):afterEnd]) == after[:afterEnd-(i+len(needle))]
		if beforeOK && afterOK {
			if found >= 0 {
				return 0, 0, false
			}
			found = i
		}
		at = i + 1
		if len(needle) == 0 && at > len(content) {
			break
		}
	}
	return found, found + len(needle), found >= 0
}

func Registry(values ...any) map[string]reflect.Type {
	r := map[string]reflect.Type{}
	for _, value := range values {
		t := reflect.TypeOf(value)
		r[typeName(t)] = t
	}
	return r
}
