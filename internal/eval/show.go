package eval

import (
	"fmt"

	"github.com/waj/fango/internal/types"
	"github.com/waj/fango/runtime/fangort"
)

// Show renders a value for the REPL, dispatching on its solved type. It
// calls into fangort — the single shared formatting implementation for both
// backends (DESIGN.md §9.6 hard rule).
func Show(v Value, ty types.Type, b *types.Builtins) string {
	if con, ok := ty.(*types.TCon); ok && con.Unique == b.Int.Unique {
		return fangort.ShowInt(v.(int64))
	}
	return fmt.Sprintf("<unshowable value of type %s>", types.Show(ty))
}
