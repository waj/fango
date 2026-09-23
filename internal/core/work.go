package core

import "github.com/waj/fango/internal/types"

// Work is a checked edge of a restricted row package. Pack retains a typed
// coroutine; Open checks owner membership and recovers exactly its protocol.
// It never converts an unindexed package into a freely callable closure.
type Work struct {
	// SourceRow is the budget at introduction, or the child's residual row at
	// packaging. It is proof data and is instantiated with SourceType at calls.
	SourceRow types.Type
	Kind      string
	Args      []Expr
	Ty        types.Type
}

func (*Work) isExpr()            {}
func (w *Work) Type() types.Type { return w.Ty }

func (l *linter) work(w *Work, where string) {
	if (w.Kind == "begin" || w.Kind == "pack") && w.SourceRow == nil {
		l.errorf("%s: missing work effect-budget proof", where)
	}
	name := map[string]string{"begin": types.WorkRunName, "end": types.WorkRunName, "facet": types.WorkFacetName, "pack": types.WorkPackName, "open": l.defName}[w.Kind]
	if name == "" || !l.intrinsics[name] || l.defName != name || w.Kind == "open" && name != types.WorkAdvanceName && name != types.WorkCloseName {
		l.errorf("%s: work operation outside its checked intrinsic", where)
	}
	if d := l.workers[l.defName]; d == nil || !types.WorkShape(l.defName, d.SourceType) {
		l.errorf("%s: missing or invalid source work contract", where)
	} else if w.Kind == "begin" || w.Kind == "pack" {
		args, _ := PeelFun(d.SourceType, types.IntrinsicArity(l.defName))
		var want types.Type
		if w.Kind == "begin" {
			want = args[0].(*types.TFun).Arg.(*types.TCon).Args[0]
		} else {
			want = args[1].(*types.TCon).Args[3]
		}
		if !types.Equal(want, w.SourceRow) {
			l.errorf("%s: stale work effect-budget proof", where)
		}
	}
	con := func(t types.Type, name string, arity int) bool {
		c, ok := t.(*types.TCon)
		return ok && c.Name == name && len(c.Args) == arity
	}
	ok := false
	switch w.Kind {
	case "begin":
		ok = len(w.Args) == 0 && con(w.Ty, types.WorkOwnerTypeName, 1)
	case "end":
		ok = len(w.Args) == 1 && con(w.Args[0].Type(), types.WorkOwnerTypeName, 1) && con(w.Ty, "()", 0)
	case "facet":
		ok = len(w.Args) == 1 && con(w.Args[0].Type(), types.WorkOwnerTypeName, 1) && con(w.Ty, types.WorkFacetTypeName, 0)
	case "pack":
		if len(w.Args) == 2 && con(w.Args[0].Type(), types.WorkFacetTypeName, 0) {
			q, r, a, c := types.CoroutineProtocol(w.Args[1].Type())
			x, y, z, p := types.WorkProtocol(w.Ty)
			ok = c && p && EqualValueRepresentation(q, x) && EqualValueRepresentation(r, y) && EqualValueRepresentation(a, z)
		}
	case "open":
		if len(w.Args) == 2 && con(w.Args[0].Type(), types.WorkOwnerTypeName, 1) {
			q, r, a, c := types.WorkProtocol(w.Args[1].Type())
			x, y, z, p := types.CoroutineProtocol(w.Ty)
			ok = c && p && EqualValueRepresentation(q, x) && EqualValueRepresentation(r, y) && EqualValueRepresentation(a, z)
		}
	}
	if !ok {
		l.errorf("%s: invalid work package protocol", where)
	}
	for _, arg := range w.Args {
		l.expr(arg, where)
	}
}
