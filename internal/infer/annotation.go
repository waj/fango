package infer

import (
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/types"
)

// ResolveTypeExpr converts a surface type expression into a checker type
// via the session's type-name table. Returns nil (with diagnostics) if any
// part fails to resolve.
func (ck *Checker) ResolveTypeExpr(te ast.TypeExpr) (types.Type, []diag.Error) {
	switch te := te.(type) {
	case *ast.TName:
		if t, ok := ck.TypeNames[te.Name]; ok {
			return t, nil
		}
		return nil, []diag.Error{diag.Errorf(te.Sp, "NAMING ERROR",
			"I don't know a type named `%s`.", te.Name)}
	case *ast.TVarName:
		return nil, []diag.Error{diag.Errorf(te.Sp, "UNSUPPORTED ANNOTATION",
			"Type variables in annotations arrive with polymorphism (S5).\nFor now annotations must be concrete: Int, Float, String, Bool, ().")}
	case *ast.TFunExpr:
		arg, argErrs := ck.ResolveTypeExpr(te.Arg)
		ret, retErrs := ck.ResolveTypeExpr(te.Ret)
		errs := append(argErrs, retErrs...)
		if arg == nil || ret == nil {
			return nil, errs
		}
		return &types.TFun{Arg: arg, Eff: types.Row{}, Ret: ret}, errs
	default:
		panic("infer: unhandled TypeExpr node")
	}
}
