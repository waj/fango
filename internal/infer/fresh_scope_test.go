package infer

import (
	"testing"

	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/types"
)

type scopeFixture struct {
	sup    *types.Supply
	b      *types.Builtins
	adts   map[int]*types.ADTInfo
	reader *types.TCon
}

func newScopeFixture() *scopeFixture {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	e := sup.FreshRigid(types.RowVar)
	reader := &types.TCon{Unique: sup.NextUnique(), Name: "Reader"}
	field := &types.TFun{Arg: b.Unit, Eff: types.Row{Tail: e}, Ret: b.Int}
	adt := &types.ADTInfo{Con: reader, Params: []*types.TVar{e},
		Ctors: []*types.CtorInfo{{Name: "Reader", Fields: []types.Type{field}}}}
	return &scopeFixture{sup: sup, b: b, reader: reader, adts: map[int]*types.ADTInfo{reader.Unique: adt}}
}

func (f *scopeFixture) read(row types.Row) types.Type {
	return &types.TCon{Unique: f.reader.Unique, Name: f.reader.Name, Args: []types.Type{row}}
}

func (f *scopeFixture) solve(cs ...Constraint) (Subst, []diag.Error) {
	sub, _, errs := Solve(cs, nil, Subst{}, f.b, f.sup)
	return sub, errs
}

func expectScopeEscape(t *testing.T, errs []diag.Error) {
	t.Helper()
	for _, err := range errs {
		if err.Title == "SCOPE ESCAPE" {
			return
		}
	}
	t.Fatalf("want SCOPE ESCAPE, got %v", errs)
}

// Two allocations of the same effect template must coexist in one row. A
// shared-row parser widens both reader arguments without merging identities.
func TestFreshScopesComposeThroughReaderVariance(t *testing.T) {
	f := newScopeFixture()
	first, second := NewFreshEffect(f.sup, "Cursor"), NewFreshEffect(f.sup, "Cursor")
	e := f.sup.FreshVar(types.RowVar)
	combined := types.Row{Tail: e}
	sub, errs := f.solve(
		Constraint{Left: f.read(first.Within(types.Row{})), Right: f.read(combined), Subsume: true, ADTs: f.adts},
		Constraint{Left: f.read(second.Within(types.Row{})), Right: f.read(combined), Subsume: true, ADTs: f.adts},
		Constraint{Left: combined, Right: second.Within(first.Within(types.Row{})), Include: true},
		Constraint{Scope: &ScopeBoundary{Effect: second, Result: f.b.Int, Residual: first.Within(types.Row{})}},
		Constraint{Scope: &ScopeBoundary{Effect: first, Result: f.b.Int}},
	)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	row := sub.Apply(combined).(types.Row)
	if len(row.Labels) != 2 || row.Labels[0].Unique == row.Labels[1].Unique {
		t.Fatalf("independent reader scopes collapsed: %+v", row)
	}
}

func TestFreshScopeCannotBeChosenByCaller(t *testing.T) {
	f := newScopeFixture()
	caller, runner := NewFreshEffect(f.sup, "Cursor"), NewFreshEffect(f.sup, "Cursor")
	_, errs := f.solve(Constraint{
		Left: f.read(runner.Within(types.Row{})), Right: f.read(caller.Within(types.Row{})),
	})
	if len(errs) == 0 {
		t.Fatal("caller-selected identity unified with fresh runner identity")
	}
}

// Check all outward type positions, including latent arrows and opaque type
// arguments; the scope check must not depend on a constructor being public.
func TestFreshScopeRejectsEscapingValues(t *testing.T) {
	for _, shape := range []string{"reader", "callback", "callback argument", "wrapped callback", "effect argument", "row tail"} {
		t.Run(shape, func(t *testing.T) {
			f := newScopeFixture()
			scope := NewFreshEffect(f.sup, "Cursor")
			row := scope.Within(types.Row{})
			var result types.Type = f.read(row)
			switch shape {
			case "callback":
				result = &types.TFun{Arg: f.b.Unit, Eff: row, Ret: f.b.Int}
			case "callback argument":
				result = &types.TFun{Arg: result, Ret: f.b.Int}
			case "wrapped callback":
				callback := &types.TFun{Arg: f.b.Unit, Eff: row, Ret: f.b.Int}
				result = &types.TCon{Unique: f.sup.NextUnique(), Name: "AbstractBox", Args: []types.Type{callback}}
			case "effect argument":
				result = &types.TFun{Arg: f.b.Unit, Ret: f.b.Int, Eff: types.Row{
					Labels: []types.EffLabel{{Unique: f.sup.NextUnique(), Name: "Store", Args: []types.Type{result}}},
				}}
			case "row tail":
				result = f.read(types.Row{Tail: f.sup.FreshVar(types.RowVar)})
			}
			cs := []Constraint{{Scope: &ScopeBoundary{Effect: scope, Result: result}}}
			if shape == "row tail" {
				cs = append(cs, Constraint{Left: result, Right: f.read(row)})
			}
			_, errs := f.solve(cs...)
			expectScopeEscape(t, errs)
		})
	}
}

// The boundary comes first deliberately. Constraint generation/row solving
// order must not let an unknown result hide the reader.
func TestFreshScopeEscapeWaitsForDeferredRowBounds(t *testing.T) {
	f := newScopeFixture()
	scope := NewFreshEffect(f.sup, "Cursor")
	result := f.sup.FreshVar(types.General)
	external := f.sup.FreshRigid(types.RowVar)
	callback := &types.TFun{Arg: f.b.Unit, Eff: scope.Within(types.Row{Tail: external}), Ret: f.b.Int}
	_, errs := f.solve(
		Constraint{Scope: &ScopeBoundary{Effect: scope, Result: result, Residual: types.Row{Tail: external}}},
		Constraint{Left: callback, Right: result, Subsume: true},
	)
	expectScopeEscape(t, errs)
}

func TestFreshScopeRejectsOuterStorageEscape(t *testing.T) {
	f := newScopeFixture()
	stored := f.sup.FreshVar(types.General)
	outerCell := &types.TCon{Unique: f.sup.NextUnique(), Name: "Cell", Args: []types.Type{stored}}
	scope := NewFreshEffect(f.sup, "Cursor")
	_, errs := f.solve(
		Constraint{Scope: &ScopeBoundary{Effect: scope, Result: f.b.Unit, Outer: []types.Type{outerCell}}},
		Constraint{Left: stored, Right: f.read(scope.Within(types.Row{}))},
	)
	expectScopeEscape(t, errs)
}

func TestFreshScopeRejectsResidualEscape(t *testing.T) {
	f := newScopeFixture()
	scope := NewFreshEffect(f.sup, "Cursor")
	residual := types.Row{Tail: f.sup.FreshVar(types.RowVar)}
	_, errs := f.solve(
		Constraint{Scope: &ScopeBoundary{Effect: scope, Result: f.b.Unit, Residual: residual}},
		Constraint{Left: scope.Within(types.Row{}), Right: residual, Include: true},
	)
	expectScopeEscape(t, errs)
}

func TestFreshScopeCannotEraseEffectsByAdaptation(t *testing.T) {
	for _, knownSchema := range []bool{false, true} {
		f := newScopeFixture()
		scope := NewFreshEffect(f.sup, "Cursor")
		var adts map[int]*types.ADTInfo
		if knownSchema {
			adts = f.adts
		}
		_, errs := f.solve(Constraint{
			Left: f.read(scope.Within(types.Row{})), Right: f.read(types.Row{}),
			Subsume: true, ADTs: adts,
		})
		if len(errs) == 0 {
			t.Fatalf("adaptation erased scope (schema known: %v)", knownSchema)
		}
	}
}

// Returning an outer reader from an inner scope is valid. Returning the
// inner reader into outer storage is not; lifetime nesting has a direction.
func TestFreshScopeNesting(t *testing.T) {
	f := newScopeFixture()
	outer := NewFreshEffect(f.sup, "Cursor")
	inner := NewFreshEffect(f.sup, "Cursor")
	_, errs := f.solve(Constraint{Scope: &ScopeBoundary{
		Effect: inner, Result: f.read(outer.Within(types.Row{})), Residual: outer.Within(types.Row{}),
	}})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	slot := f.sup.FreshVar(types.General)
	_, errs = f.solve(
		Constraint{Scope: &ScopeBoundary{Effect: inner, Result: f.b.Unit, Outer: []types.Type{slot}}},
		Constraint{Left: slot, Right: f.read(inner.Within(types.Row{}))},
	)
	expectScopeEscape(t, errs)
}

func TestFreshScopePreservesDomainEffects(t *testing.T) {
	f := newScopeFixture()
	scope := NewFreshEffect(f.sup, "Cursor")
	database := types.Row{Labels: []types.EffLabel{{Unique: f.sup.NextUnique(), Name: "Database"}}}
	_, errs := f.solve(
		Constraint{Left: database, Right: scope.Within(database), Include: true},
		Constraint{Scope: &ScopeBoundary{Effect: scope, Result: f.b.Int, Residual: database}},
	)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	_, errs = f.solve(Constraint{Left: database, Right: scope.Within(types.Row{}), Include: true})
	if len(errs) == 0 {
		t.Fatal("local scope authorized an unrelated domain effect")
	}
}

func TestFreshScopeObligationSurvivesIncrementalSolving(t *testing.T) {
	f := newScopeFixture()
	scope := NewFreshEffect(f.sup, "Cursor")
	result := f.sup.FreshVar(types.General)
	boundary := Constraint{Scope: &ScopeBoundary{Effect: scope, Result: result}}
	sub, errs := f.solve(boundary)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	_, _, errs = Solve([]Constraint{boundary, {Left: result, Right: f.read(scope.Within(types.Row{}))}},
		nil, sub, f.b, f.sup)
	expectScopeEscape(t, errs)
}
