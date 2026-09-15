package core

import (
	"fmt"

	"github.com/waj/fango/internal/types"
)

// CheckCursorResult verifies the nominal element/result packaging shared by
// semantic Core and the independently checked advancement instruction.
func CheckCursorResult(cursor, result types.Type, adt *types.ADTInfo) error {
	c, ok := cursor.(*types.TCon)
	if !ok || c.Name != types.IteratorTypeName || len(c.Args) != 2 {
		return fmt.Errorf("advancement requires an Iterator cursor")
	}
	r, ok := result.(*types.TCon)
	if !ok || r.Name != "Maybe.Maybe" || len(r.Args) != 1 || !types.Equal(r.Args[0], c.Args[0]) || adt == nil || adt.Con == nil || adt.Con.Unique != r.Unique || len(adt.Params) != 1 || len(adt.Ctors) != 2 {
		return fmt.Errorf("cursor advancement has an invalid Maybe result")
	}
	if adt.Ctors[0] == nil || adt.Ctors[1] == nil || adt.Ctors[0].Name != "Maybe.Nothing" || adt.Ctors[1].Name != "Maybe.Just" || len(adt.Ctors[0].Fields) != 0 || len(adt.Ctors[1].Fields) != 1 || !types.Equal(adt.InstFields(adt.Ctors[1], r.Args)[0], c.Args[0]) {
		return fmt.Errorf("cursor advancement has invalid Maybe constructors")
	}
	return nil
}
