package codegen

import (
	goast "go/ast"
	"reflect"
)

// The exact evidence expression identifies the activation. Invocation rows,
// child replacements and shadowing install different expressions and therefore
// retain dispatch. Only small clauses without captured evidence are expanded.
func (g *gen) operationCallee(evidence goast.Expr, name string) goast.Expr {
	if fn := g.knownOperations[evidence][name]; fn != nil && !g.disableOptimizations {
		return g.cloneOperation(reflect.ValueOf(fn)).Interface().(*goast.FuncLit)
	}
	return &goast.SelectorExpr{X: evidence, Sel: ident(name)}
}

// Every invocation gets its own AST and lexical block. Result-call markings
// are copied as well: the ABI pass must never infer a cloned call's result.
func (g *gen) cloneOperation(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Interface:
		out := reflect.New(v.Type()).Elem()
		if !v.IsNil() {
			out.Set(g.cloneOperation(v.Elem()))
		}
		return out
	case reflect.Pointer:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(g.cloneOperation(v.Elem()))
		if call, ok := v.Interface().(*goast.CallExpr); ok {
			if typ, found := g.outcomeCalls[call]; found {
				cloned := out.Interface().(*goast.CallExpr)
				g.markOutcomeCall(cloned, typ)
				if g.unitOutcomeCalls[call] {
					g.unitOutcomeCalls[cloned] = true
				}
			}
		}
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		for i := range v.NumField() {
			out.Field(i).Set(g.cloneOperation(v.Field(i)))
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := range v.Len() {
			out.Index(i).Set(g.cloneOperation(v.Index(i)))
		}
		return out
	default:
		return v
	}
}
