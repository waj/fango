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
