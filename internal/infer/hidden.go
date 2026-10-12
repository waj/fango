package infer

import (
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// A deferred boundary also covers inferred record patterns: their nominal
// type and fresh abstract binders may become known only after solving.
type hiddenBoundary struct {
	pattern  ast.Pattern
	outer    []types.Type
	result   types.Type
	residual types.Row
}

func rigidHidden(hidden []types.Type) []*types.TVar {
	vars := make([]*types.TVar, len(hidden))
	for i, t := range hidden {
		vars[i] = t.(*types.TVar)
	}
	return vars
}

func (g *generator) closeHiddenBoundaries(from int, result types.Type, residual types.Row) {
	for i := from; i < len(g.hiddenBoundaries); i++ {
		if g.hiddenBoundaries[i].result == nil {
			g.hiddenBoundaries[i].result = result
			g.hiddenBoundaries[i].residual = residual
		}
	}
}

func (ck *Checker) hiddenInPattern(p ast.Pattern) []*types.TVar {
	vars := append([]*types.TVar(nil), ck.PatternHidden[p]...)
	switch p := p.(type) {
	case *ast.PCtor:
		for _, arg := range p.Args {
			vars = append(vars, ck.hiddenInPattern(arg)...)
		}
	case *ast.PRecord:
		for _, field := range p.Fields {
			vars = append(vars, ck.hiddenInPattern(field.Pattern)...)
		}
	}
	return vars
}

func (g *generator) checkHiddenBoundaries() []diag.Error {
	var errs []diag.Error
	for _, boundary := range g.hiddenBoundaries {
		vars := g.ck.hiddenInPattern(boundary.pattern)
		if len(vars) == 0 {
			continue
		}
		roots := append(append([]types.Type(nil), boundary.outer...), boundary.result, boundary.residual)
		for _, root := range roots {
			found := types.RigidVarsIn(g.ck.Sub.Apply(root))
			for _, hidden := range vars {
				for _, used := range found {
					if hidden.ID == used.ID {
						errs = append(errs, diag.Errorf(boundary.pattern.Span(), "HIDDEN TYPE ESCAPES", "A type opened by this pattern escapes through its result, effects, or an outer binding. Consume the hidden value inside this scope, or package it again."))
						goto next
					}
				}
			}
		}
	next:
	}
	return errs
}

func hiddenField(ctor *types.CtorInfo, index int) bool {
	if len(ctor.Hidden) == 0 {
		return false
	}
	for _, free := range types.RigidVarsIn(ctor.Fields[index]) {
		for _, hidden := range ctor.Hidden {
			if free.ID == hidden.ID {
				return true
			}
		}
	}
	return false
}

func hiddenProjection(sp source.Span, name string) diag.Error {
	return diag.Errorf(sp, "HIDDEN RECORD FIELD", "Field `%s` depends on a hidden type. Open the record with a pattern so that its related fields share the same abstract type.", name)
}

// Most patterns cannot open hidden types. Avoid snapshotting their outer
// environments; inferred records must wait until their nominal type is solved.
func (g *generator) patternMayHideTypes(p ast.Pattern) bool {
	switch p := p.(type) {
	case *ast.PRecord:
		return true
	case *ast.PCtor:
		if ctor := g.ck.Ctors[p.Name]; ctor != nil && len(ctor.Hidden) != 0 {
			return true
		}
		for _, arg := range p.Args {
			if g.patternMayHideTypes(arg) {
				return true
			}
		}
	}
	return false
}

// Forward type headers can acquire their parameter kinds after this field
// resolves. Check those positions again once the whole declaration batch exists.
func (ck *Checker) checkHiddenFieldKinds(field types.Type, ctor *types.CtorInfo, sp source.Span) []diag.Error {
	if len(ctor.Hidden) == 0 {
		return nil
	}
	hidden := map[int]bool{}
	for _, v := range ctor.Hidden {
		hidden[v.ID] = true
	}
	var invalid bool
	var visit func(types.Type, types.VarKind)
	visit = func(t types.Type, want types.VarKind) {
		switch t := t.(type) {
		case *types.TVar:
			invalid = invalid || hidden[t.ID] && (want == types.RowVar || t.Kind == types.RowVar)
		case *types.TCon:
			adt := ck.ADTs[t.Unique]
			for i, arg := range t.Args {
				kind := types.General
				if adt != nil && i < len(adt.Params) {
					kind = adt.Params[i].Kind
				}
				visit(arg, kind)
			}
		case *types.TFun:
			visit(t.Arg, types.General)
			visit(t.Ret, types.General)
			visit(t.Eff, types.RowVar)
		case types.Row:
			visit(t.Tail, types.RowVar)
			for _, label := range t.Labels {
				for _, arg := range label.Args {
					visit(arg, types.General)
				}
			}
		}
	}
	visit(field, types.General)
	if invalid {
		return []diag.Error{diag.Errorf(sp, "HIDDEN EFFECT ROW", "An effect row used by this field must be declared in the type's parameters. Only ordinary types can be hidden in a constructor.")}
	}
	return nil
}
