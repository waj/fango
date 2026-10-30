package check

import (
	"fmt"
	"reflect"
	"sort"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/infer"
	"github.com/waj/fango/internal/meta"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// CopyGraph copies a compiler value graph, preserving sharing and cycles.
// Source files are immutable and retain their identity. post visits each new
// value, allowing session-only remapping without modifying cached objects.
func CopyGraph[T any](value T, post func(reflect.Value)) T {
	seen := map[visit]reflect.Value{}
	var copyValue func(reflect.Value) reflect.Value
	copyValue = func(v reflect.Value) reflect.Value {
		if !v.IsValid() {
			return v
		}
		if v.Type() == reflect.TypeFor[*source.File]() {
			return v
		}
		out := reflect.New(v.Type()).Elem()
		switch v.Kind() {
		case reflect.Pointer:
			if v.IsNil() {
				return out
			}
			key := visit{v.Type(), v.Pointer()}
			if old, ok := seen[key]; ok {
				return old
			}
			out.Set(reflect.New(v.Type().Elem()))
			seen[key] = out
			out.Elem().Set(copyValue(v.Elem()))
		case reflect.Interface:
			if !v.IsNil() {
				out.Set(copyValue(v.Elem()))
			}
		case reflect.Map:
			if v.IsNil() {
				return out
			}
			out.Set(reflect.MakeMapWithSize(v.Type(), v.Len()))
			iter := v.MapRange()
			for iter.Next() {
				out.SetMapIndex(copyValue(iter.Key()), copyValue(iter.Value()))
			}
		case reflect.Slice:
			if v.IsNil() {
				return out
			}
			out.Set(reflect.MakeSlice(v.Type(), v.Len(), v.Len()))
			for i := 0; i < v.Len(); i++ {
				out.Index(i).Set(copyValue(v.Index(i)))
			}
		case reflect.Struct:
			out.Set(v)
			for i := 0; i < v.NumField(); i++ {
				if v.Type().Field(i).IsExported() && v.Type().Field(i).Tag.Get("object") != "omit" {
					out.Field(i).Set(copyValue(v.Field(i)))
				}
			}
		case reflect.Array:
			for i := 0; i < v.Len(); i++ {
				out.Index(i).Set(copyValue(v.Index(i)))
			}
		default:
			out.Set(v)
		}
		if post != nil {
			post(out)
		}
		return out
	}
	return copyValue(reflect.ValueOf(value)).Interface().(T)
}

// NominalReuse compares checked declaration contracts, ignoring positions and
// allocation numbers. A matching declaration can retain identity only if all
// nominal declarations in its contract also retain theirs.
func NominalReuse(next, old *infer.ModuleState, nextTemplates, oldTemplates *meta.Table) (map[int]int, map[int]int) {
	type declaration struct {
		id     int
		value  any
		effect bool
	}
	declarations := func(s *infer.ModuleState) (map[string]declaration, *ModuleObject) {
		ds := map[string]declaration{}
		o := &ModuleObject{Nominals: map[int]string{}, EffectNames: map[int]string{}}
		for _, a := range s.ADTs {
			ds[a.Con.Name] = declaration{a.Con.Unique, a, false}
			o.Nominals[a.Con.Unique] = a.Con.Name
		}
		for _, e := range s.Effects {
			ds[e.Name] = declaration{e.Unique, e, true}
			o.EffectNames[e.Unique] = e.Name
		}
		for _, t := range s.Types {
			if t, ok := t.(*types.TCon); ok {
				o.Nominals[t.Unique] = t.Name
			}
		}
		return ds, o
	}
	nd, no := declarations(next)
	od, oo := declarations(old)
	contract := func(value any, object *ModuleObject, templates *meta.Table) string {
		// Attribute Code is part of a declaration's contract, but its table
		// slot is session allocation. Compare the quoted syntax it denotes.
		normalized := CopyGraph(value, func(v reflect.Value) {
			if v.Type() == reflect.TypeFor[meta.Code]() {
				code := v.Addr().Interface().(*meta.Code)
				if expr := templates.Expand(code); expr != nil {
					code.Direct, code.Template, code.Holes = expr, 0, nil
				}
			}
		})
		return canonicalDigest(normalized, object)
	}
	ids, effects := map[int]int{}, map[int]int{}
	for name, n := range nd {
		if o, ok := od[name]; ok && n.effect == o.effect && contract(n.value, no, nextTemplates) == contract(o.value, oo, oldTemplates) {
			if n.effect {
				effects[n.id] = o.id
			} else {
				ids[n.id] = o.id
			}
		}
	}
	for name, class := range next.Classes {
		previous := old.Classes[name]
		if previous == nil || contract(class, no, nextTemplates) != contract(previous, oo, oldTemplates) {
			delete(ids, class.Dict.Con.Unique)
		}
	}
	// Builtin scalar identities are supplied by the session and never change.
	for _, t := range next.Types {
		if t, ok := t.(*types.TCon); ok {
			if o, ok := old.Types[t.Name].(*types.TCon); ok && t.Unique == o.Unique {
				ids[t.Unique] = t.Unique
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, n := range nd {
			reused := ids
			if n.effect {
				reused = effects
			}
			if _, ok := reused[n.id]; !ok {
				continue
			}
			valid := true
			CopyGraph(n.value, func(v reflect.Value) {
				switch v.Type() {
				case reflect.TypeFor[types.TCon]():
					t := v.Interface().(types.TCon)
					if _, declared := nd[t.Name]; declared {
						if _, ok := ids[t.Unique]; !ok {
							valid = false
						}
					}
				case reflect.TypeFor[types.EffLabel]():
					e := v.Interface().(types.EffLabel)
					if _, ok := effects[e.Unique]; !ok && !e.Scoped {
						valid = false
					}
				}
			})
			if !valid {
				delete(reused, n.id)
				changed = true
			}
		}
	}
	return ids, effects
}

// RemapNominal rewrites the identity-bearing compiler nodes in a copied graph.
func RemapNominal(v reflect.Value, ids, effects map[int]int) {
	if v.Type() == reflect.TypeFor[meta.TypeRepr]() {
		r := v.Addr().Interface().(*meta.TypeRepr)
		if r.Visible != nil {
			visible := map[int]bool{}
			for id, yes := range r.Visible {
				if reused, ok := ids[id]; ok {
					id = reused
				} else if reused, ok := effects[id]; ok {
					id = reused
				}
				visible[id] = yes
			}
			r.Visible = visible
		}
		return
	}
	var mapping map[int]int
	switch v.Type() {
	case reflect.TypeFor[types.TCon]():
		mapping = ids
	case reflect.TypeFor[types.EffectInfo](), reflect.TypeFor[types.EffLabel](), reflect.TypeFor[core.EffectInstance]():
		mapping = effects
	default:
		return
	}
	f := v.FieldByName("Unique")
	if id, ok := mapping[int(f.Int())]; ok {
		f.SetInt(int64(id))
	}
}

// NativeContractsEqual normalizes type variables but retains nominal identity:
// an old worker must not interpret a new wrapper as an old opaque value. The
// raw identities compare across generations because a is reconciled first: a
// nominal NominalReuse kept already carries b's identity, and one it retired
// differs even when the printed scheme does not.
func NativeContractsEqual(a, b *infer.Checker) bool {
	fingerprint := func(n *types.NativeInfo) string {
		nominals, effects := map[int]bool{}, map[int]bool{}
		CopyGraph(n, func(v reflect.Value) {
			switch v.Type() {
			case reflect.TypeFor[types.TCon]():
				nominals[v.Interface().(types.TCon).Unique] = true
			case reflect.TypeFor[types.EffLabel]():
				e := v.Interface().(types.EffLabel)
				if !e.Scoped {
					effects[e.Unique] = true
				}
			}
		})
		var ns, es []int
		for id := range nominals {
			ns = append(ns, id)
		}
		for id := range effects {
			es = append(es, id)
		}
		sort.Ints(ns)
		sort.Ints(es)
		template := ""
		if n.Template != nil {
			template = *n.Template
		}
		return fmt.Sprintf("%s|%s|%v|%d|%s|%s|%v|%v", n.Name, n.Module, n.Storage, n.Arity, template, types.ShowScheme(n.Scheme), ns, es)
	}
	for name, native := range a.Natives {
		old := b.Natives[name]
		if old == nil || fingerprint(native) != fingerprint(old) {
			return false
		}
	}
	return true
}
