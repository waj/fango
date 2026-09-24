package codegen

import (
	goast "go/ast"
	gotoken "go/token"

	"github.com/waj/fango/internal/core"
	machineir "github.com/waj/fango/internal/machine"
	"github.com/waj/fango/internal/types"
)

func (g *gen) machineStartRef(name string) goast.Expr {
	owner := symbolOwner(name)
	if d := g.defs[name]; d != nil {
		owner = d.Owner
	}
	return g.qualified(owner, "MachineStart_"+linkName(name))
}

// A start factory stays lazy. Only a bounded sequence of atomic aliases ending
// in a tail call to a tagged pause may bypass its frame. The fallback never
// invokes an opaque factory recursively: it allocates the ordinary worker.
func (g *gen) machineStartDecl(w *machineir.Worker, params []paramSpec) goast.Decl {
	var body []goast.Stmt
	if callee, request := forwardedPause(w); callee != nil {
		owner := &goast.SelectorExpr{X: g.machineExpr(callee), Sel: ident("PauseOwner")}
		body = append(body, &goast.IfStmt{
			Cond: &goast.BinaryExpr{X: owner, Op: gotoken.NEQ, Y: ident("nil")},
			Body: &goast.BlockStmt{List: []goast.Stmt{returnStmt(callExpr(selector("fangort", "PauseStart"), owner, g.machineBoxedValue(request)))}},
		})
	}
	var args []goast.Expr
	for _, p := range params {
		args = append(args, ident(p.name))
	}
	frame := callExpr(indexExpr(g.machineConstructorRef(w.Name), machineTypeParamIdents(w.TyParams)), args...)
	body = append(body, returnStmt(callExpr(selector("fangort", "FrameStart"), frame)))
	d := workerDecl("MachineStart_"+linkName(w.Name), params, selector("fangort", "MachineStart"), body).(*goast.FuncDecl)
	d.Type.TypeParams = g.typeParamFields(w.TyParams)
	return d
}

func forwardedPause(w *machineir.Worker) (core.Expr, core.Expr) {
	if !w.Optimized || w.StateToken {
		return nil, nil
	}
	values := map[string]core.Expr{}
	for _, p := range w.Params {
		values[p.Name] = &core.VarRef{Name: p.Name, Local: true, Ty: p.Ty}
	}
	atom := func(e core.Expr) core.Expr {
		switch e := e.(type) {
		case *core.VarRef:
			return values[e.Name]
		case *core.UnitLit, *core.IntLit, *core.FloatLit, *core.BoolLit, *core.CharLit, *core.StringLit:
			return e
		}
		return nil
	}
	next := w.Entry
	for range 48 {
		if int(next) < 0 || int(next) >= len(w.Blocks) {
			return nil, nil
		}
		switch t := w.Blocks[next].Term.(type) {
		case *machineir.Eval:
			v := atom(t.Value)
			if v == nil {
				return nil, nil
			}
			values[t.Bind.Name], next = v, t.Next
		case *machineir.Call:
			if !t.Tail || t.Capture || t.Callee != "" || t.Operation != nil || len(t.EvidenceArgs) != 0 || len(t.Args) != 1 || (t.Row != nil && len(t.Row.Effects) != 0) {
				return nil, nil
			}
			callee, request := atom(t.CalleeExpr), atom(t.Args[0])
			if _, ok := callee.(*core.VarRef); !ok || request == nil {
				return nil, nil
			}
			fn, ok := callee.Type().(*types.TFun)
			if !ok || !types.Equal(fn.Ret, w.Result) {
				return nil, nil
			}
			return callee, request
		default:
			return nil, nil
		}
	}
	return nil, nil
}
