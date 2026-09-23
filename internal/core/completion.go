package core

import "github.com/waj/fango/internal/types"

// Completion is the checked introduction/elimination of detached execution.
// Row names invocation evidence; it is never stored in the resulting value.
type Completion struct {
	Name    string
	Value   Expr
	Row     *RowArgument
	Result  *types.ADTInfo
	Ty      types.Type
	Control types.Control
}

func (*Completion) isExpr()            {}
func (e *Completion) Type() types.Type { return e.Ty }
