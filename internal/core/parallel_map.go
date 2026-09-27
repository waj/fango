package core

import "github.com/waj/fango/internal/types"

type ParallelMap struct {
	Function, Input Expr
	Ty              types.Type
}

func (*ParallelMap) isExpr()            {}
func (e *ParallelMap) Type() types.Type { return e.Ty }
func (l *linter) parallelMap(e *ParallelMap, where string) {
	if e.Function == nil || e.Input == nil || !l.intrinsics[types.AsyncParMapName] {
		l.errorf("%s: malformed parallel map", where)
		return
	}
	l.expr(e.Function, where)
	l.expr(e.Input, where)
	fn, ok := e.Function.Type().(*types.TFun)
	if !ok || len(fn.Eff.Labels) != 0 || types.FunctionOpenRow(fn) || types.FunctionControl(fn) != (types.Control{}) {
		l.errorf("%s: parallel map requires a pure callback", where)
		return
	}
	input, iok := e.Input.Type().(*types.TCon)
	output, ook := e.Ty.(*types.TCon)
	if !iok || !ook || len(input.Args) != 1 || len(output.Args) != 1 || input.Unique != output.Unique || l.adts[input.Unique] == nil || l.adts[input.Unique].Repr != types.ReprList || !EqualValueRepresentation(input.Args[0], fn.Arg) || !EqualValueRepresentation(output.Args[0], fn.Ret) {
		l.errorf("%s: parallel map List indices mismatch", where)
	}
}
