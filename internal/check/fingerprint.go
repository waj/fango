package check

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/meta"
	"github.com/waj/fango/internal/modules"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

type typeParamID int

func digest(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write([]byte(strconv.Itoa(len(part))))
		_, _ = h.Write([]byte{':'})
		_, _ = h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func ownFingerprints(object *ModuleObject) (semantic, abi, stage string) {
	semantic = canonicalDigest(struct {
		State    *infer.ModuleState
		Resolver modules.Interface
	}{object.State, object.Resolver}, object)
	headers := append([]core.Def(nil), object.Runtime...)
	for i := range headers {
		headers[i].Body = nil
	}
	abi = canonicalDigest(headers, object)
	stage = canonicalDigest(struct {
		Defs      []core.Def
		Templates []*meta.Template
	}{object.Stage, object.Templates}, object)
	return
}

func combinedFingerprint(kind, own string, dependencies []string) string {
	parts := []string{kind, own}
	parts = append(parts, dependencies...)
	return digest(parts...)
}

func canonicalDigest(value any, object *ModuleObject) string {
	w := &canonicalWriter{
		seen: map[visit]int{}, ids: map[reflect.Type]map[int64]int{},
		nominals: object.Nominals, effects: object.EffectNames,
		templateBase: object.TemplateBase,
	}
	w.write(reflect.ValueOf(value), "")
	h := sha256.Sum256(w.buf.Bytes())
	return hex.EncodeToString(h[:])
}

type visit struct {
	t reflect.Type
	p uintptr
}

type canonicalWriter struct {
	buf          bytes.Buffer
	seen         map[visit]int
	ids          map[reflect.Type]map[int64]int
	nominals     map[int]string
	effects      map[int]string
	templateBase int
}

var (
	spanReflectType       = reflect.TypeOf(source.Span{})
	tvarReflectType       = reflect.TypeOf(types.TVar{})
	tconReflectType       = reflect.TypeOf(types.TCon{})
	effectReflectType     = reflect.TypeOf(types.EffectInfo{})
	effLabelReflectType   = reflect.TypeOf(types.EffLabel{})
	effectInstReflectType = reflect.TypeOf(core.EffectInstance{})
	instanceReflectType   = reflect.TypeOf(infer.InstanceInfo{})
	deriverReflectType    = reflect.TypeOf(infer.DeriverInfo{})
	quoteReflectType      = reflect.TypeOf(core.Quote{})
	codeReflectType       = reflect.TypeOf(meta.Code{})
	captureReflectType    = reflect.TypeOf(types.CaptureVar(0))
	scopeReflectType      = reflect.TypeOf(types.ScopeID(0))
	resumeReflectType     = reflect.TypeOf(types.ResumeID(0))
	typeParamReflectType  = reflect.TypeOf(typeParamID(0))
)

func (w *canonicalWriter) token(s string) {
	w.buf.WriteString(strconv.Itoa(len(s)))
	w.buf.WriteByte(':')
	w.buf.WriteString(s)
}

func (w *canonicalWriter) logicalID(t reflect.Type, n int64) int {
	if n == 0 {
		return 0
	}
	m := w.ids[t]
	if m == nil {
		m = map[int64]int{}
		w.ids[t] = m
	}
	if id := m[n]; id != 0 {
		return id
	}
	id := len(m) + 1
	m[n] = id
	return id
}

func (w *canonicalWriter) write(v reflect.Value, field string) {
	if !v.IsValid() {
		w.token("nil")
		return
	}
	if v.Type() == spanReflectType {
		w.token("span")
		return
	}
	if v.Type() == captureReflectType || v.Type() == scopeReflectType || v.Type() == resumeReflectType {
		w.token(v.Type().String())
		w.token(strconv.Itoa(w.logicalID(v.Type(), v.Int())))
		return
	}
	if v.Kind() == reflect.Interface {
		if v.IsNil() {
			w.token("nil")
			return
		}
		w.token(v.Elem().Type().String())
		w.write(v.Elem(), field)
		return
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			w.token("nil")
			return
		}
		key := visit{v.Type(), v.Pointer()}
		if id := w.seen[key]; id != 0 {
			w.token("ref")
			w.token(strconv.Itoa(id))
			return
		}
		id := len(w.seen) + 1
		w.seen[key] = id
		w.token("ptr")
		w.token(strconv.Itoa(id))
		w.write(v.Elem(), field)
		return
	}
	if v.Kind() == reflect.Struct {
		w.token(v.Type().String())
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if f.PkgPath != "" || f.Tag.Get("object") == "omit" ||
				(v.Type() == tconReflectType && f.Name == "Unique") ||
				(v.Type() == effectReflectType && f.Name == "Unique") ||
				((v.Type() == effLabelReflectType || v.Type() == effectInstReflectType) && f.Name == "Unique") ||
				(v.Type() == instanceReflectType && (f.Name == "Limit" || f.Name == "Span")) ||
				(v.Type() == deriverReflectType && f.Name == "Span") {
				continue
			}
			w.token(f.Name)
			if v.Type() == tvarReflectType && f.Name == "ID" {
				w.token(strconv.Itoa(w.logicalID(typeParamReflectType, v.Field(i).Int())))
				continue
			}
			if (v.Type() == quoteReflectType || v.Type() == codeReflectType) && f.Name == "Template" {
				w.token(strconv.FormatInt(v.Field(i).Int()-int64(w.templateBase), 10))
				continue
			}
			w.write(v.Field(i), f.Name)
		}
		return
	}
	switch v.Kind() {
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			w.token("nil-slice")
			return
		}
		w.token("sequence")
		w.token(strconv.Itoa(v.Len()))
		for i := 0; i < v.Len(); i++ {
			w.write(v.Index(i), field)
		}
	case reflect.Map:
		if v.IsNil() {
			w.token("nil-map")
			return
		}
		keys := v.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return stableMapKey(keys[i]) < stableMapKey(keys[j]) })
		w.token("map")
		w.token(strconv.Itoa(len(keys)))
		for _, key := range keys {
			if field == "Visible" && key.Kind() == reflect.Int {
				w.token(w.nominals[int(key.Int())])
			} else {
				w.write(key, "")
			}
			w.write(v.MapIndex(key), "")
		}
	case reflect.String:
		w.token(v.String())
	case reflect.Bool:
		w.token(strconv.FormatBool(v.Bool()))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if field == "Effects" || field == "RowEffects" {
			w.token(w.effects[int(v.Int())])
			return
		}
		if field == "TypeParams" {
			w.token(strconv.Itoa(w.logicalID(typeParamReflectType, v.Int())))
			return
		}
		w.token(strconv.FormatInt(v.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		w.token(strconv.FormatUint(v.Uint(), 10))
	case reflect.Float32, reflect.Float64:
		w.token(fmt.Sprintf("%016x", math.Float64bits(v.Convert(reflect.TypeOf(float64(0))).Float())))
	default:
		w.token(v.Type().String())
	}
}

func stableMapKey(v reflect.Value) string {
	switch v.Kind() {
	case reflect.String:
		return "s:" + v.String()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return "i:" + fmt.Sprintf("%020d", v.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "u:" + fmt.Sprintf("%020d", v.Uint())
	case reflect.Bool:
		return "b:" + strconv.FormatBool(v.Bool())
	default:
		return fmt.Sprint(v.Interface())
	}
}
