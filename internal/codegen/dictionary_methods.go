package codegen

import (
	"fmt"
	goast "go/ast"
	"strings"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// A dictionary's curried method field is what a partial application or a
// first-class use needs, but calling it with all its arguments would build the
// intermediate callables at every step. A pure method taking two or more
// arguments therefore also has an entry field: one plain Go function at its full
// arity, which saturated calls use. A method with an effect keeps its curried
// field alone, since its evidence belongs to its final stage and its transport
// is chosen there.

func dictionaryADT(adt *types.ADTInfo) bool {
	return adt != nil && strings.HasPrefix(types.SurfaceName(adt.Con.Name), "_dictionary_")
}

func methodEntryField(i int) string { return fmt.Sprintf("M%d", i) }

// methodEntryArity is the declared arity of a method field with an entry, or 0.
func methodEntryArity(field types.Type) int {
	arity := 0
	for {
		fn, ok := field.(*types.TFun)
		if !ok {
			break
		}
		if types.FunctionControl(fn) != (types.Control{}) || len(fn.Eff.Labels) != 0 || types.FunctionOpenRow(fn) {
			return 0
		}
		arity++
		field = fn.Ret
	}
	if arity < 2 {
		return 0
	}
	return arity
}

func (g *gen) methodEntryFields(adt *types.ADTInfo, ctor *types.CtorInfo) []*goast.Field {
	if !dictionaryADT(adt) {
		return nil
	}
	var fields []*goast.Field
	for i, field := range ctor.Fields {
		if arity := methodEntryArity(field); arity != 0 {
			fields = append(fields, &goast.Field{Names: []*goast.Ident{ident(methodEntryField(i))}, Type: g.flatCallbackType(field, arity, types.Direct)})
		}
	}
	return fields
}

// dictionaryValue appends the entries to a dictionary's fields. A known lambda
// chain is the entry's own body; anything else is a value whose intermediate
// applications must still run on every call, so its entry adapts it, and it is
// evaluated once, in field order.
func (g *gen) dictionaryValue(ctor *types.CtorInfo, typeArgs []types.Type, result goast.Expr, fieldTypes []types.Type, elts []goast.Expr, sources []core.Expr) goast.Expr {
	entries := make([]goast.Expr, 0, len(ctor.Fields))
	adapted := false
	for i, field := range ctor.Fields {
		arity := methodEntryArity(field)
		if arity == 0 {
			continue
		}
		var entry goast.Expr
		if sources != nil {
			if lam, ok := sources[i].(*core.Lambda); ok {
				if literal, ok := g.saturatedLambda(lam, arity, types.Direct); ok {
					entry = literal
				}
			}
		}
		adapted = adapted || entry == nil
		entries = append(entries, entry)
	}
	if !adapted {
		return g.ctorValue(ctor, typeArgs, append(append([]goast.Expr(nil), elts...), entries...)...)
	}
	params := make([]paramSpec, len(elts))
	fields := make([]goast.Expr, len(elts))
	for i := range elts {
		params[i] = paramSpec{fmt.Sprintf("t_method%d", g.tmp), g.goType(fieldTypes[i])}
		g.tmp++
		fields[i] = ident(params[i].name)
	}
	next := 0
	for i, field := range ctor.Fields {
		arity := methodEntryArity(field)
		if arity == 0 {
			continue
		}
		if entries[next] == nil {
			entries[next] = g.curriedMethodEntry(fields[i], fieldTypes[i], arity)
		}
		next++
	}
	value := g.ctorValue(ctor, typeArgs, append(fields, entries...)...)
	return callExpr(funcLitParams(params, result, []goast.Stmt{returnStmt(value)}), elts...)
}

func (g *gen) curriedMethodEntry(method goast.Expr, t types.Type, arity int) goast.Expr {
	shape := g.flatCallbackType(t, arity, types.Direct)
	call := method
	for i, field := range shape.Params.List {
		parameter := fmt.Sprintf("t_methodArg%d_%d", g.tmp, i)
		field.Names = []*goast.Ident{ident(parameter)}
		call = callExpr(callbackMember(call, types.Direct), ident(parameter))
	}
	g.tmp++
	return &goast.FuncLit{Type: shape, Body: &goast.BlockStmt{List: []goast.Stmt{returnStmt(call)}}}
}

type methodEntryCall struct {
	dictionary goast.Expr
	field      int
}

// methodProjection recognizes the binding elaboration gives a method it calls
// through a dictionary parameter.
func methodProjection(rhs core.Expr) (*core.VarRef, *core.SwitchCtor, int, bool) {
	match, ok := rhs.(*core.Case)
	if !ok {
		return nil, nil, 0, false
	}
	dictionary, ok := match.Scrut.(*core.VarRef)
	if !ok || !dictionary.Local {
		return nil, nil, 0, false
	}
	tree, ok := match.Tree.(*core.SwitchCtor)
	if !ok || len(tree.Cases) != 1 || !dictionaryADT(tree.ADT) {
		return nil, nil, 0, false
	}
	leaf, ok := tree.Cases[0].Tree.(*core.Leaf)
	if !ok {
		return nil, nil, 0, false
	}
	result, ok := leaf.Body.(*core.VarRef)
	if !ok || !result.Local {
		return nil, nil, 0, false
	}
	for i, bind := range tree.Cases[0].Binds {
		if bind == result.Name {
			return dictionary, tree, i, true
		}
	}
	return nil, nil, 0, false
}

// rememberProjectedMethod handles a method bound by a let around its projection.
// Reports whether the binding is unused now.
func (g *gen) rememberProjectedMethod(let *core.Let) bool {
	if g.disableOptimizations || let.Rec {
		return false
	}
	dictionary, tree, index, ok := methodProjection(let.Rhs)
	if !ok {
		return false
	}
	return g.rememberMethodEntries(let.Name, let.Body, g.expr(dictionary, 0), index, tree.Cases[0].Ctor.Fields[index])
}

// rememberMatchedMethod handles a method bound directly by a match on its
// dictionary, the shape inlining leaves. Reports whether the binder is unused now.
func (g *gen) rememberMatchedMethod(t *core.SwitchCtor, index int) bool {
	leaf, ok := t.Cases[0].Tree.(*core.Leaf)
	if g.disableOptimizations || !ok || !dictionaryADT(t.ADT) {
		return false
	}
	return g.rememberMethodEntries(t.Cases[0].Binds[index], leaf.Body, ident(mangleValue(t.Scrut)), index, t.Cases[0].Ctor.Fields[index])
}

// rememberMethodEntries selects the saturated calls of a bound method. They must
// sit in this function: a lambda or handler can be outlined with only its own
// captures, so a use inside one keeps the binding, as does any other use. A later
// stage's argument must already be an atom, since an unknown method's intermediate
// application runs before it would otherwise be evaluated. Reports whether the
// binding is unused now.
func (g *gen) rememberMethodEntries(name string, body core.Expr, dictionary goast.Expr, index int, field types.Type) bool {
	arity := methodEntryArity(field)
	if arity == 0 {
		return false
	}
	used := false
	core.InspectPruned(body, func(e core.Expr) bool {
		switch e := e.(type) {
		case *core.VarRef:
			used = used || e.Name == name
		case *core.Lambda, *core.Handle:
			used = used || core.Mentions(e, name)
			return false
		case *core.App:
			head, stages := core.CallSpine(e)
			ref, ok := head.(*core.VarRef)
			if e.CalleeKind != core.Value || !ok || ref.Name != name || len(stages) != arity || !core.SaturatedCallback(stages) {
				return true
			}
			if g.methodEntries == nil {
				g.methodEntries = map[*core.App]methodEntryCall{}
			}
			g.methodEntries[e] = methodEntryCall{dictionary, index}
			for _, stage := range stages {
				used = used || core.Mentions(stage.Args[0], name)
			}
			return false
		}
		return true
	})
	return !used
}

func (g *gen) methodEntryCall(e *core.App) (goast.Expr, bool) {
	call, ok := g.methodEntries[e]
	if !ok {
		return nil, false
	}
	_, stages := core.CallSpine(e)
	args := make([]goast.Expr, len(stages))
	for i, stage := range stages {
		args[i] = g.expr(stage.Args[0], 0)
	}
	return callExpr(&goast.SelectorExpr{X: call.dictionary, Sel: ident(methodEntryField(call.field))}, args...), true
}
