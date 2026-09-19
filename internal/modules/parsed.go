package modules

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/source"
)

const parsedSchema = 1

// ParsedCache is the opaque content-addressed storage seam. The module layer
// owns the AST codec; cache implementations never import parser or AST types.
type ParsedCache interface {
	LoadParsed(sourceHash string) ([]byte, bool)
	StoreParsed(sourceHash string, data []byte)
}

// ParsedUnit is one parser result plus discovery metadata derived by that same
// parse. Its fields stay private; every cache use receives a decoded copy that
// later resolution may mutate without changing the stored representation.
type ParsedUnit struct {
	mod       *ast.Module
	header    string
	headerSp  source.Span
	hasHeader bool
	imports   []parsedImport
	noPrelude bool
	staging   bool
	lists     bool
	tuples    bool
	deriving  bool
}

type parsedImport struct {
	module string
	sp     source.Span
}

type parsedEnvelope struct {
	Schema        int           `json:"schema"`
	Kind          string        `json:"kind"`
	SourceSHA256  string        `json:"source_sha256"`
	PayloadSHA256 string        `json:"payload_sha256"`
	Payload       parsedPayload `json:"payload"`
}

type parsedPayload struct {
	Tree      treeDTO      `json:"tree"`
	Discovery discoveryDTO `json:"discovery"`
}

type discoveryDTO struct {
	Header    *nameSpanDTO  `json:"header"`
	Imports   []nameSpanDTO `json:"imports"`
	NoPrelude bool          `json:"no_prelude"`
	Staging   bool          `json:"staging"`
	Lists     bool          `json:"lists"`
	Tuples    bool          `json:"tuples"`
	Deriving  bool          `json:"deriving"`
}

type nameSpanDTO struct {
	Name string  `json:"name"`
	Span spanDTO `json:"span"`
}

// treeDTO is a closed tagged representation. Struct tags are explicitly
// registered below, fields are checked against the current Go AST definition,
// and every span is rebound to the current source.File during decoding.
type treeDTO struct {
	Kind    string        `json:"kind"`
	String  string        `json:"string,omitempty"`
	Integer int64         `json:"integer,omitempty"`
	Bits    string        `json:"bits,omitempty"`
	Boolean bool          `json:"boolean,omitempty"`
	Nil     bool          `json:"nil,omitempty"`
	Span    *spanDTO      `json:"span,omitempty"`
	Elem    *treeDTO      `json:"elem,omitempty"`
	Fields  []fieldDTO    `json:"fields,omitempty"`
	Items   []treeDTO     `json:"items,omitempty"`
	Entries []mapEntryDTO `json:"entries,omitempty"`
}

type fieldDTO struct {
	Name  string  `json:"name"`
	Value treeDTO `json:"value"`
}

type mapEntryDTO struct {
	Key   string  `json:"key"`
	Value treeDTO `json:"value"`
}

type spanDTO struct {
	Present bool `json:"present"`
	Start   int  `json:"start"`
	End     int  `json:"end"`
}

var spanType = reflect.TypeOf(source.Span{})

var parsedTypes = []any{
	ast.Module{}, ast.ModuleHeader{}, ast.Exposing{}, ast.ExposeItem{}, ast.Import{},
	ast.ValueDecl{}, ast.PatternDecl{}, ast.TypeDecl{}, ast.EffectDecl{}, ast.ClassDecl{}, ast.InstanceDecl{}, ast.DeriverDecl{}, ast.FixityDecl{},
	ast.RecordFieldDef{}, ast.CtorDef{}, ast.OpSig{}, ast.NativeBody{}, ast.Param{}, ast.TypeAnn{}, ast.PredExpr{}, ast.Equation{},
	ast.IntLit{}, ast.FloatLit{}, ast.StringLit{}, ast.CharLit{}, ast.UnitLit{}, ast.Var{}, ast.Ctor{}, ast.RecordLit{}, ast.RecordExprField{}, ast.RecordGet{}, ast.RecordUpdate{}, ast.App{}, ast.Neg{}, ast.If{}, ast.Block{}, ast.BlockItem{}, ast.LocalBind{}, ast.Lambda{}, ast.BinOp{}, ast.OpChain{}, ast.OpRef{}, ast.Case{}, ast.CaseBranch{}, ast.Handle{}, ast.HandlerState{}, ast.HandleClause{}, ast.ReturnClause{}, ast.Resume{}, ast.Quote{}, ast.Splice{}, ast.TypeOf{},
	ast.TName{}, ast.TVarName{}, ast.TFunExpr{}, ast.EffRow{}, ast.EffLabelExpr{}, ast.TApp{},
	ast.PVar{}, ast.PWildcard{}, ast.PUnit{}, ast.PInt{}, ast.PFloat{}, ast.PString{}, ast.PChar{}, ast.PPin{}, ast.PRecord{}, ast.RecordPatternField{}, ast.PCtor{},
}

var parsedTypeByTag, parsedTagByType = parsedTypeRegistry()

func parsedTypeRegistry() (map[string]reflect.Type, map[reflect.Type]string) {
	byTag := make(map[string]reflect.Type, len(parsedTypes))
	byType := make(map[reflect.Type]string, len(parsedTypes))
	for _, value := range parsedTypes {
		t := reflect.TypeOf(value)
		byTag[t.Name()] = t
		byType[t] = t.Name()
	}
	return byTag, byType
}

func newParsedUnit(m *ast.Module) *ParsedUnit {
	u := &ParsedUnit{mod: m, noPrelude: m.NoPrelude, staging: m.UsesStaging, lists: m.UsesLists, tuples: m.UsesTuples, deriving: usesDeriving(m)}
	if m.Header != nil {
		u.hasHeader, u.header, u.headerSp = true, m.Header.Name, m.Header.NameSpan
	}
	for _, im := range m.Imports {
		u.imports = append(u.imports, parsedImport{module: im.Module, sp: im.ModuleSpan})
	}
	return u
}

func encodeParsed(u *ParsedUnit, sourceHash string) ([]byte, error) {
	tree, err := encodeTree(reflect.ValueOf(u.mod))
	if err != nil {
		return nil, err
	}
	d := discoveryDTO{NoPrelude: u.noPrelude, Staging: u.staging, Lists: u.lists, Tuples: u.tuples, Deriving: u.deriving}
	if u.hasHeader {
		sp := encodeSpan(u.headerSp)
		d.Header = &nameSpanDTO{Name: u.header, Span: sp}
	}
	d.Imports = make([]nameSpanDTO, len(u.imports))
	for i, im := range u.imports {
		d.Imports[i] = nameSpanDTO{Name: im.module, Span: encodeSpan(im.sp)}
	}
	p := parsedPayload{Tree: tree, Discovery: d}
	payload, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(payload)
	return json.Marshal(parsedEnvelope{Schema: parsedSchema, Kind: "parsed-unit", SourceSHA256: sourceHash, PayloadSHA256: hex.EncodeToString(h[:]), Payload: p})
}

func decodeParsed(data []byte, sourceHash string, f *source.File) (*ParsedUnit, error) {
	var envelope parsedEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.Schema != parsedSchema || envelope.Kind != "parsed-unit" || envelope.SourceSHA256 != sourceHash {
		return nil, fmt.Errorf("invalid parsed-unit envelope")
	}
	payload, err := json.Marshal(envelope.Payload)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(payload)
	if hex.EncodeToString(h[:]) != envelope.PayloadSHA256 {
		return nil, fmt.Errorf("parsed-unit payload hash mismatch")
	}
	v, err := decodeTree(envelope.Payload.Tree, reflect.TypeOf((*ast.Module)(nil)), f)
	if err != nil {
		return nil, err
	}
	m, ok := v.Interface().(*ast.Module)
	if !ok || m == nil {
		return nil, fmt.Errorf("parsed-unit root is not a module")
	}
	d := envelope.Payload.Discovery
	u := &ParsedUnit{mod: m, noPrelude: d.NoPrelude, staging: d.Staging, lists: d.Lists, tuples: d.Tuples, deriving: d.Deriving}
	if d.Header != nil {
		sp, err := decodeSpan(d.Header.Span, f)
		if err != nil {
			return nil, err
		}
		u.hasHeader, u.header, u.headerSp = true, d.Header.Name, sp
	}
	for _, im := range d.Imports {
		sp, err := decodeSpan(im.Span, f)
		if err != nil {
			return nil, err
		}
		u.imports = append(u.imports, parsedImport{module: im.Name, sp: sp})
	}
	if !validDiscovery(u) {
		return nil, fmt.Errorf("parsed-unit discovery metadata mismatch")
	}
	return u, nil
}

func validDiscovery(u *ParsedUnit) bool {
	m := u.mod
	if (m.Header != nil) != u.hasHeader || m.NoPrelude != u.noPrelude || m.UsesStaging != u.staging || m.UsesLists != u.lists || m.UsesTuples != u.tuples || usesDeriving(m) != u.deriving || len(m.Imports) != len(u.imports) {
		return false
	}
	if u.hasHeader && (m.Header.Name != u.header || m.Header.NameSpan.Start != u.headerSp.Start || m.Header.NameSpan.End != u.headerSp.End) {
		return false
	}
	for i, im := range m.Imports {
		if im.Module != u.imports[i].module || im.ModuleSpan.Start != u.imports[i].sp.Start || im.ModuleSpan.End != u.imports[i].sp.End {
			return false
		}
	}
	return true
}

func encodeTree(v reflect.Value) (treeDTO, error) {
	if !v.IsValid() {
		return treeDTO{Kind: "nil", Nil: true}, nil
	}
	if v.Kind() == reflect.Interface {
		if v.IsNil() {
			return treeDTO{Kind: "nil", Nil: true}, nil
		}
		return encodeTree(v.Elem())
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return treeDTO{Kind: "pointer", Nil: true}, nil
		}
		e, err := encodeTree(v.Elem())
		return treeDTO{Kind: "pointer", Elem: &e}, err
	}
	if v.Type() == spanType {
		sp := v.Interface().(source.Span)
		d := encodeSpan(sp)
		return treeDTO{Kind: "span", Span: &d}, nil
	}
	switch v.Kind() {
	case reflect.Struct:
		tag, ok := parsedTagByType[v.Type()]
		if !ok {
			return treeDTO{}, fmt.Errorf("unsupported parsed AST struct %s", v.Type())
		}
		d := treeDTO{Kind: tag, Fields: make([]fieldDTO, 0, v.NumField())}
		for i := 0; i < v.NumField(); i++ {
			field := v.Type().Field(i)
			if !field.IsExported() {
				continue
			}
			value, err := encodeTree(v.Field(i))
			if err != nil {
				return treeDTO{}, fmt.Errorf("%s.%s: %w", tag, field.Name, err)
			}
			d.Fields = append(d.Fields, fieldDTO{Name: field.Name, Value: value})
		}
		return d, nil
	case reflect.Slice:
		d := treeDTO{Kind: "slice", Nil: v.IsNil(), Items: make([]treeDTO, v.Len())}
		for i := 0; i < v.Len(); i++ {
			item, err := encodeTree(v.Index(i))
			if err != nil {
				return treeDTO{}, err
			}
			d.Items[i] = item
		}
		return d, nil
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return treeDTO{}, fmt.Errorf("unsupported parsed AST map %s", v.Type())
		}
		keys := v.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
		d := treeDTO{Kind: "map", Nil: v.IsNil(), Entries: make([]mapEntryDTO, len(keys))}
		for i, key := range keys {
			value, err := encodeTree(v.MapIndex(key))
			if err != nil {
				return treeDTO{}, err
			}
			d.Entries[i] = mapEntryDTO{Key: key.String(), Value: value}
		}
		return d, nil
	case reflect.String:
		return treeDTO{Kind: "string", String: v.String()}, nil
	case reflect.Bool:
		return treeDTO{Kind: "bool", Boolean: v.Bool()}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return treeDTO{Kind: "int", Integer: v.Int()}, nil
	case reflect.Float32, reflect.Float64:
		return treeDTO{Kind: "float", Bits: fmt.Sprintf("%016x", math.Float64bits(v.Convert(reflect.TypeOf(float64(0))).Float()))}, nil
	default:
		return treeDTO{}, fmt.Errorf("unsupported parsed AST value %s", v.Type())
	}
}

func decodeTree(d treeDTO, target reflect.Type, f *source.File) (reflect.Value, error) {
	if target.Kind() == reflect.Interface {
		if d.Kind == "nil" && d.Nil {
			return reflect.Zero(target), nil
		}
		if d.Kind != "pointer" || d.Elem == nil {
			return reflect.Value{}, fmt.Errorf("interface %s requires tagged pointer", target)
		}
		t, ok := parsedTypeByTag[d.Elem.Kind]
		if !ok {
			return reflect.Value{}, fmt.Errorf("unknown parsed AST tag %q", d.Elem.Kind)
		}
		v, err := decodeTree(d, reflect.PointerTo(t), f)
		if err != nil || !v.Type().Implements(target) {
			if err == nil {
				err = fmt.Errorf("%s does not implement %s", v.Type(), target)
			}
			return reflect.Value{}, err
		}
		return v, nil
	}
	if target.Kind() == reflect.Pointer {
		if d.Kind != "pointer" {
			return reflect.Value{}, fmt.Errorf("expected pointer, got %q", d.Kind)
		}
		if d.Nil {
			return reflect.Zero(target), nil
		}
		if d.Elem == nil {
			return reflect.Value{}, fmt.Errorf("missing pointer element")
		}
		v, err := decodeTree(*d.Elem, target.Elem(), f)
		if err != nil {
			return reflect.Value{}, err
		}
		p := reflect.New(target.Elem())
		p.Elem().Set(v)
		return p, nil
	}
	if target == spanType {
		if d.Kind != "span" || d.Span == nil {
			return reflect.Value{}, fmt.Errorf("invalid span")
		}
		sp, err := decodeSpan(*d.Span, f)
		return reflect.ValueOf(sp), err
	}
	switch target.Kind() {
	case reflect.Struct:
		if parsedTagByType[target] != d.Kind {
			return reflect.Value{}, fmt.Errorf("expected %s, got %q", target.Name(), d.Kind)
		}
		v := reflect.New(target).Elem()
		exported := make([]reflect.StructField, 0, target.NumField())
		for i := 0; i < target.NumField(); i++ {
			if target.Field(i).IsExported() {
				exported = append(exported, target.Field(i))
			}
		}
		if len(d.Fields) != len(exported) {
			return reflect.Value{}, fmt.Errorf("%s field count mismatch", target.Name())
		}
		for i, field := range exported {
			if d.Fields[i].Name != field.Name {
				return reflect.Value{}, fmt.Errorf("%s field %d is %q, want %q", target.Name(), i, d.Fields[i].Name, field.Name)
			}
			value, err := decodeTree(d.Fields[i].Value, field.Type, f)
			if err != nil {
				return reflect.Value{}, fmt.Errorf("%s.%s: %w", target.Name(), field.Name, err)
			}
			v.FieldByIndex(field.Index).Set(value)
		}
		return v, nil
	case reflect.Slice:
		if d.Kind != "slice" {
			return reflect.Value{}, fmt.Errorf("expected slice, got %q", d.Kind)
		}
		if d.Nil {
			return reflect.Zero(target), nil
		}
		v := reflect.MakeSlice(target, len(d.Items), len(d.Items))
		for i := range d.Items {
			item, err := decodeTree(d.Items[i], target.Elem(), f)
			if err != nil {
				return reflect.Value{}, err
			}
			v.Index(i).Set(item)
		}
		return v, nil
	case reflect.Map:
		if d.Kind != "map" || target.Key().Kind() != reflect.String {
			return reflect.Value{}, fmt.Errorf("invalid map encoding")
		}
		if d.Nil {
			return reflect.Zero(target), nil
		}
		v := reflect.MakeMapWithSize(target, len(d.Entries))
		last := ""
		for i, entry := range d.Entries {
			if i > 0 && entry.Key <= last {
				return reflect.Value{}, fmt.Errorf("map keys are not strictly sorted")
			}
			value, err := decodeTree(entry.Value, target.Elem(), f)
			if err != nil {
				return reflect.Value{}, err
			}
			v.SetMapIndex(reflect.ValueOf(entry.Key).Convert(target.Key()), value)
			last = entry.Key
		}
		return v, nil
	case reflect.String:
		if d.Kind != "string" {
			return reflect.Value{}, fmt.Errorf("expected string")
		}
		return reflect.ValueOf(d.String).Convert(target), nil
	case reflect.Bool:
		if d.Kind != "bool" {
			return reflect.Value{}, fmt.Errorf("expected bool")
		}
		v := reflect.New(target).Elem()
		v.SetBool(d.Boolean)
		return v, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if d.Kind != "int" {
			return reflect.Value{}, fmt.Errorf("expected int")
		}
		v := reflect.New(target).Elem()
		if v.OverflowInt(d.Integer) {
			return reflect.Value{}, fmt.Errorf("integer overflow")
		}
		v.SetInt(d.Integer)
		return v, nil
	case reflect.Float32, reflect.Float64:
		if d.Kind != "float" {
			return reflect.Value{}, fmt.Errorf("expected float")
		}
		var bits uint64
		if _, err := fmt.Sscanf(d.Bits, "%016x", &bits); err != nil {
			return reflect.Value{}, fmt.Errorf("invalid float bits")
		}
		v := reflect.New(target).Elem()
		v.SetFloat(math.Float64frombits(bits))
		return v, nil
	default:
		return reflect.Value{}, fmt.Errorf("unsupported decoded type %s", target)
	}
}

func encodeSpan(sp source.Span) spanDTO {
	return spanDTO{Present: sp.File != nil, Start: sp.Start, End: sp.End}
}

func decodeSpan(d spanDTO, f *source.File) (source.Span, error) {
	if !d.Present {
		if d.Start != 0 || d.End != 0 {
			return source.Span{}, fmt.Errorf("offsets on absent span")
		}
		return source.Span{}, nil
	}
	if d.Start < 0 || d.End < d.Start || d.End > len(f.Content) {
		return source.Span{}, fmt.Errorf("span [%d,%d) outside source", d.Start, d.End)
	}
	return source.Span{File: f, Start: d.Start, End: d.End}, nil
}
