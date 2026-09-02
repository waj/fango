package eval

import (
	"fmt"

	"github.com/waj/fango/internal/types"
	"github.com/waj/fango/runtime/fangort"
)

// Show renders a value for the REPL, dispatching on its solved type. It
// calls into fangort — the single shared formatting implementation for both
// backends (DESIGN.md §9.6 hard rule). Strings render as source literals
// (quoted, escaped) at the prompt; `print` outputs them raw.
func Show(v Value, ty types.Type, b *types.Builtins) string {
	if _, isFn := ty.(*types.TFun); isFn {
		return "<function>"
	}
	if cv, ok := v.(*CtorVal); ok {
		return showCtorVal(cv, false)
	}
	if con, ok := ty.(*types.TCon); ok && len(con.Args) == 0 {
		switch con.Unique {
		case b.Int.Unique:
			return fangort.ShowInt(v.(int64))
		case b.Float.Unique:
			return fangort.ShowFloat(v.(float64))
		case b.String.Unique:
			return fangort.ShowStringLiteral(v.(string))
		case b.Bool.Unique:
			return fangort.ShowBool(v.(bool))
		case b.Unit.Unique:
			return fangort.ShowUnit()
		}
	}
	return fmt.Sprintf("<unshowable value of type %s>", types.Show(ty))
}

// showCtorVal renders an ADT value exactly as the compiled backend's derived
// showT_X does (§8.6): `Circle 2.5`, nested field-taking constructors
// parenthesized, String fields as source literals. Dispatch is on the
// field's dynamic value type — bijective with its solved static type.
func showCtorVal(v *CtorVal, nested bool) string {
	if len(v.Fields) == 0 {
		return v.Ctor.Name
	}
	s := v.Ctor.Name
	for _, f := range v.Fields {
		s += " " + showFieldValue(f)
	}
	if nested {
		return "(" + s + ")"
	}
	return s
}

func showFieldValue(f Value) string {
	switch f := f.(type) {
	case int64:
		return fangort.ShowInt(f)
	case float64:
		return fangort.ShowFloat(f)
	case string:
		return fangort.ShowStringLiteral(f)
	case bool:
		return fangort.ShowBool(f)
	case struct{}:
		return fangort.ShowUnit()
	case *CtorVal:
		return showCtorVal(f, true)
	default:
		return fmt.Sprintf("<unshowable %T>", f)
	}
}

// ShowForPrint renders a value with `print` semantics — identical to Show
// except Strings are raw, mirroring fangort.PrintString. The differential
// harness uses it to observe a value-typed main exactly as the compiled
// backend's print-main mode does.
func ShowForPrint(v Value, ty types.Type, b *types.Builtins) string {
	if con, ok := ty.(*types.TCon); ok && con.Unique == b.String.Unique {
		return fangort.ShowString(v.(string))
	}
	return Show(v, ty, b)
}
