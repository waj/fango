package infer

import (
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/eval"
	"github.com/waj/fango/internal/types"
)

func (ck *Checker) checkAttributes(td *ast.TypeDecl) []diag.Error {
	con, ok := ck.TypeNames[td.Name].(*types.TCon)
	if !ok {
		return nil
	}
	adt := ck.ADTs[con.Unique]
	if !adt.AttributesPending {
		return nil
	}
	check := func(groups []ast.AttributeGroup) ([]types.AttributeInfo, []diag.Error) {
		var entries []types.AttributeInfo
		for _, group := range groups {
			for _, expression := range group.Exprs {
				sp := expression.Span()
				if ck.moduleCheck != nil {
					if es := ck.moduleCheck.prepareSplice(expression, sp); len(es) > 0 {
						return nil, es
					}
				}
				validator := &stageChecker{binders: map[string]binderStage{}, stage: 1}
				validator.expr(expression)
				if len(validator.errs) > 0 {
					return nil, validator.errs
				}
				x := &stageExpander{ck: ck, stage: 1}
				operand := x.expr(expression)
				if len(x.errs) > 0 {
					return nil, x.errs
				}
				ty, es := ck.ExprWhere(operand, false)
				if len(es) > 0 {
					return nil, es
				}
				if len(ck.PendingPreds) > 0 {
					if es := ck.DefaultPreds(ck.PendingPreds, sp); len(es) > 0 {
						return nil, es
					}
					var obligations []predObligation
					for _, p := range ck.PendingPreds {
						obligations = append(obligations, predObligation{pred: p, span: sp})
					}
					remaining, es := ck.reduceObligations(obligations, nil)
					if len(es) > 0 {
						return nil, es
					}
					ck.PendingPreds = remaining
				}
				ty = ck.Sub.Apply(ty)
				if !ck.reflectionClosed(ty) || len(ck.PendingPreds) > 0 {
					return nil, []diag.Error{diag.Errorf(sp, "ATTRIBUTE TYPE", "An attribute needs a closed, fully applied type without unresolved constraints.")}
				}
				if !ck.attributeDataType(ty, map[int]bool{}) {
					return nil, []diag.Error{diag.Errorf(sp, "ATTRIBUTE TYPE", "Attributes can store data, Code, and TypeRepr, but not functions, native handles, or resources.")}
				}
				if ck.CompileTime == nil {
					return nil, []diag.Error{diag.Errorf(sp, "STAGE ERROR", "Compile-time evaluation is not available here.")}
				}
				value, es := ck.CompileTime(operand)
				if len(es) > 0 {
					return nil, es
				}
				frozen, err := eval.FreezeAttribute(value)
				if err != nil {
					return nil, []diag.Error{diag.Errorf(sp, "ATTRIBUTE VALUE", "%v", err)}
				}
				entries = append(entries, types.AttributeInfo{Type: ty, Value: frozen, Span: sp})
			}
		}
		return entries, nil
	}
	var es []diag.Error
	adt.Attributes, es = check(td.Attributes)
	if len(es) > 0 {
		return es
	}
	for i, field := range td.RecordFields {
		adt.RecordFields[i].Attributes, es = check(field.Attributes)
		if len(es) > 0 {
			return es
		}
	}
	for i, ctor := range td.Ctors {
		info := adt.Ctors[i]
		info.Attributes, es = check(ctor.Attributes)
		if len(es) > 0 {
			return es
		}
		info.FieldAttributes = make([][]types.AttributeInfo, len(ctor.Args))
		for j, groups := range ctor.FieldAttributes {
			info.FieldAttributes[j], es = check(groups)
			if len(es) > 0 {
				return es
			}
		}
	}
	adt.AttributesPending = false
	return nil
}

func (ck *Checker) attributeDataType(t types.Type, seen map[int]bool) bool {
	con, ok := t.(*types.TCon)
	if !ok {
		return false
	}
	if con.Name == "Meta.Code" || con.Name == "Meta.TypeRepr" {
		return true
	}
	if con.Name == "Meta.Attributes" || con.Name == "Meta.Site" {
		return false
	}
	for _, arg := range con.Args {
		if !ck.attributeDataType(arg, seen) {
			return false
		}
	}
	adt := ck.ADTs[con.Unique]
	if adt == nil {
		return true
	}
	if adt.Resource || adt.NativeIndexed || adt.Repr == types.ReprNativeAny {
		return false
	}
	if seen[con.Unique] {
		return true
	}
	seen[con.Unique] = true
	sub := adt.ParamSubst(con.Args)
	for _, c := range adt.Ctors {
		for _, f := range c.Fields {
			if !ck.attributeDataType(types.SubstRigid(f, sub), seen) {
				return false
			}
		}
	}
	return true
}
