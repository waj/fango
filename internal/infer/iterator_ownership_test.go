package infer

import (
	"testing"

	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

func TestIteratorOwnershipDiagnosticsUseCursorSourceSpans(t *testing.T) {
	file := source.NewFile("iterator.fango", []byte("012345678901234567890123456789"))
	sp := func(start int) source.Span { return source.Span{File: file, Start: start, End: start + 1} }
	variable := func(name string, at int) *ast.Var { return &ast.Var{Name: name, Sp: sp(at)} }
	apply := func(fn ast.Expr, args ...ast.Expr) ast.Expr {
		for _, arg := range args {
			fn = &ast.App{Fn: fn, Arg: arg}
		}
		return fn
	}
	consume := func(name string, cursor *ast.Var) ast.Expr {
		return apply(variable(name, 1), &ast.UnitLit{Sp: sp(2)}, cursor)
	}
	withIterator := func(consumer ast.Expr) *ast.App {
		producer := &ast.Lambda{Params: []ast.Pattern{&ast.PWildcard{Sp: sp(3)}}, Body: &ast.UnitLit{Sp: sp(4)}, Sp: sp(3)}
		return apply(variable(types.GeneratorWithIteratorName, 0), producer, consumer).(*ast.App)
	}
	consumer := func(body ast.Expr) *ast.Lambda {
		return &ast.Lambda{Params: []ast.Pattern{&ast.PVar{Name: "cursor", Sp: sp(5)}}, Body: body, Sp: sp(5)}
	}
	sup := &types.Supply{}
	checker := NewChecker(sup, types.NewBuiltins(sup), NewEnv())
	checker.Intrinsics[types.GeneratorWithIteratorName] = types.Scheme{}
	g := &generator{ck: checker}

	tests := []struct {
		name      string
		consumer  ast.Expr
		wantTitle string
		wantStart int
	}{
		{name: "consumed once", consumer: consumer(consume("Iterator.forEach", variable("cursor", 8)))},
		{name: "alias", consumer: consumer(variable("cursor", 9)), wantTitle: "ITERATOR CURSOR ESCAPES", wantStart: 9},
		{name: "captured", consumer: consumer(&ast.Lambda{Params: []ast.Pattern{&ast.PWildcard{Sp: sp(10)}}, Body: variable("cursor", 11), Sp: sp(10)}), wantTitle: "ITERATOR CURSOR ESCAPES", wantStart: 11},
		{name: "duplicate", consumer: consumer(&ast.Block{Items: []ast.BlockItem{{Expr: consume("Iterator.forEach", variable("cursor", 12))}}, Result: consume("Iterator.fold", variable("cursor", 19))}), wantTitle: "ITERATOR CURSOR REUSED", wantStart: 19},
		{name: "non lexical", consumer: variable("consume", 20), wantTitle: "ITERATOR CONSUMER", wantStart: 20},
		{name: "destructuring parameter", consumer: &ast.Lambda{Params: []ast.Pattern{&ast.PWildcard{Sp: sp(21)}}, Body: &ast.UnitLit{Sp: sp(22)}, Sp: sp(21)}, wantTitle: "ITERATOR CONSUMER", wantStart: 21},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := g.iteratorOwnership(withIterator(tt.consumer))
			if tt.wantTitle == "" {
				if len(errs) != 0 {
					t.Fatalf("unexpected diagnostics: %+v", errs)
				}
				return
			}
			if len(errs) != 1 || errs[0].Title != tt.wantTitle || errs[0].Span.Start != tt.wantStart {
				t.Fatalf("diagnostics = %+v, want %s at %d", errs, tt.wantTitle, tt.wantStart)
			}
		})
	}
}

func TestExprRunsIteratorOwnershipCheck(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	env := NewEnv()
	cursorTy := &types.TCon{Unique: sup.NextUnique(), Name: "Iterator.Iterator", Args: []types.Type{b.Int}}
	consumerTy := &types.TFun{Arg: cursorTy, Eff: types.Row{}, Ret: b.Unit}
	producerTy := &types.TFun{Arg: b.Unit, Eff: types.Row{}, Ret: b.Unit}
	withIteratorTy := &types.TFun{Arg: producerTy, Eff: types.Row{}, Ret: &types.TFun{Arg: consumerTy, Eff: types.Row{}, Ret: b.Unit}}
	env.Bind(types.GeneratorWithIteratorName, types.Scheme{Body: withIteratorTy})
	checker := NewChecker(sup, b, env)
	checker.Intrinsics[types.GeneratorWithIteratorName] = types.Scheme{Body: withIteratorTy}
	file := source.NewFile("iterator.fango", []byte("cursor"))
	sp := source.Span{File: file, Start: 0, End: 6}
	producer := &ast.Lambda{Params: []ast.Pattern{&ast.PWildcard{Sp: sp}}, Body: &ast.UnitLit{Sp: sp}, Sp: sp}
	consumer := &ast.Lambda{Params: []ast.Pattern{&ast.PVar{Name: "cursor", Sp: sp}}, Body: &ast.Var{Name: "cursor", Sp: sp}, Sp: sp}
	call := &ast.App{Fn: &ast.App{Fn: &ast.Var{Name: types.GeneratorWithIteratorName, Sp: sp}, Arg: producer}, Arg: consumer}
	_, errs := checker.Expr(call)
	found := false
	for _, err := range errs {
		if err.Title == "ITERATOR CURSOR ESCAPES" && err.Span == sp {
			found = true
		}
	}
	if !found {
		t.Fatalf("diagnostics = %+v, want source-positioned iterator escape", errs)
	}
}

func TestIteratorOwnershipRecognizesPreludeAliases(t *testing.T) {
	sup := &types.Supply{}
	checker := NewChecker(sup, types.NewBuiltins(sup), NewEnv())
	checker.Aliases["withIterator"] = types.GeneratorWithIteratorName
	checker.Aliases["forEach"] = "Iterator.forEach"
	checker.Intrinsics[types.GeneratorWithIteratorName] = types.Scheme{}
	g := &generator{ck: checker}
	sp := source.Span{}
	cursor := &ast.Var{Name: "cursor", Sp: sp}
	body := &ast.App{Fn: &ast.Var{Name: "forEach", Sp: sp}, Arg: cursor}
	consumer := &ast.Lambda{Params: []ast.Pattern{&ast.PVar{Name: "cursor", Sp: sp}}, Body: body, Sp: sp}
	call := &ast.App{Fn: &ast.App{Fn: &ast.Var{Name: "withIterator", Sp: sp}, Arg: &ast.UnitLit{Sp: sp}}, Arg: consumer}
	if errs := g.iteratorOwnership(call); len(errs) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", errs)
	}
}

func TestIteratorOwnershipIgnoresUndeclaredCanonicalSpelling(t *testing.T) {
	sup := &types.Supply{}
	checker := NewChecker(sup, types.NewBuiltins(sup), NewEnv())
	g := &generator{ck: checker}
	sp := source.Span{}
	consumer := &ast.Lambda{Params: []ast.Pattern{&ast.PVar{Name: "cursor", Sp: sp}}, Body: &ast.Var{Name: "cursor", Sp: sp}, Sp: sp}
	call := &ast.App{Fn: &ast.App{Fn: &ast.Var{Name: types.GeneratorWithIteratorName, Sp: sp}, Arg: &ast.UnitLit{Sp: sp}}, Arg: consumer}
	if errs := g.iteratorOwnership(call); len(errs) != 0 {
		t.Fatalf("ordinary user declaration with future intrinsic spelling was checked: %+v", errs)
	}
}
