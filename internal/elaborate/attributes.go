package elaborate

import (
	"fmt"
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

func (el *elab) attributeLookup(ty, raw types.Type, args []ast.Expr) core.Expr {
	argTypes, result := core.PeelFun(ty, 2)
	witness := argTypes[0].(*types.TCon)
	requested := witness.Args[0]
	if !closedAttributeType(requested) {
		at := source.Span{}
		if len(args) > 0 {
			at = args[0].Span()
		}
		el.errs = append(el.errs, diag.Errorf(at, "ATTRIBUTE TYPE", "Attribute lookup requires a closed, fully applied requested type."))
		return &core.UnitLit{Ty: el.ck.B.Unit}
	}
	coreArgs := make([]core.Expr, 2)
	for i := range 2 {
		if i < len(args) {
			coreArgs[i] = el.expr(args[i])
		} else {
			name := fmt.Sprintf("_attribute%d", el.tmp)
			el.tmp++
			coreArgs[i] = &core.VarRef{Name: name, Local: true, Ty: argTypes[i]}
		}
	}
	var body core.Expr = &core.AttributeLookup{Bag: coreArgs[1], Requested: requested,
		NilCtor: el.ck.Ctors["Meta.NoItems"], ItemCtor: el.ck.Ctors["Meta.Item"], AttachedCtor: el.ck.Ctors["Meta.Attached.__record"], Ty: result}
	// Keep witness evaluation, even though its singleton value is unnecessary.
	if len(args) > 0 {
		body = &core.Seq{First: coreArgs[0], Then: body, Ty: result}
	}
	for i := 1; i >= len(args); i-- {
		body = &core.Lambda{SourceType: arrowAt(raw, i), Param: coreArgs[i].(*core.VarRef).Name, Body: body, Ty: arrowAt(ty, i), ParamCapture: el.ck.Sup.FreshCapture()}
	}
	if len(args) > 2 {
		for _, arg := range args[2:] {
			body = el.valueApp(body, el.expr(arg))
		}
	}
	return body
}

func closedAttributeType(t types.Type) bool {
	switch t := t.(type) {
	case *types.TCon:
		for _, a := range t.Args {
			if !closedAttributeType(a) {
				return false
			}
		}
		return true
	default:
		return false
	}
}
