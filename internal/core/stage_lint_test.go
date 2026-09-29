package core

import (
	"strings"
	"testing"

	"github.com/waj/fango/internal/meta"
	"github.com/waj/fango/internal/types"
)

func TestStageCoreLintKeepsEmissionBoundary(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	code := &types.TCon{Unique: sup.NextUnique(), Name: "Meta.Code"}
	repr := &types.TCon{Unique: sup.NextUnique(), Name: "Meta.TypeRepr"}
	for _, body := range []Expr{
		&Quote{Template: 0, Ty: code},
		&TypeOf{Repr: &meta.TypeRepr{Type: b.Int}, Ty: repr},
		&TypeOf{Repr: &meta.Code{Template: 0}, Ty: code},
	} {
		p := &Prog{ADTs: []*types.ADTInfo{{Con: code}, {Con: repr}},
			Defs: []Def{{Name: "stage", Type: body.Type(), Body: body}}}
		if errs := InferCaptures(p, b); len(errs) != 0 {
			t.Fatal(errs)
		}
		if errs := LintStage(p, b); len(errs) != 0 {
			t.Fatalf("stage rejected %T: %v", body, errs)
		}
		if errs := Lint(p, b); len(errs) == 0 {
			t.Fatalf("emission accepted %T", body)
		}
	}
}

func TestStageCoreLintRejectsMalformedConstants(t *testing.T) {
	for _, test := range []struct {
		name string
		body func(*types.Builtins, *types.TCon, *types.TCon) Expr
		want string
	}{
		{"wrong quote type", func(b *types.Builtins, code, repr *types.TCon) Expr { return &Quote{Template: 0, Ty: b.Int} }, "stage value must have declared type Meta.Code"},
		{"negative template", func(b *types.Builtins, code, repr *types.TCon) Expr { return &Quote{Template: -1, Ty: code} }, "invalid template identity"},
		{"wrong hole", func(b *types.Builtins, code, repr *types.TCon) Expr {
			return &Quote{Template: 0, Ty: code, Holes: []Expr{&IntLit{Val: 1, Ty: b.Int}}}
		}, "stage value must have declared type Meta.Code"},
		{"nil reflection", func(b *types.Builtins, code, repr *types.TCon) Expr {
			return &TypeOf{Repr: (*meta.TypeRepr)(nil), Ty: repr}
		}, "no checked type representation"},
		{"arbitrary payload", func(b *types.Builtins, code, repr *types.TCon) Expr { return &TypeOf{Repr: 42, Ty: repr} }, "invalid stage constant"},
		{"retagged code", func(b *types.Builtins, code, repr *types.TCon) Expr { return &TypeOf{Repr: &meta.Code{}, Ty: repr} }, "stage value must have declared type Meta.Code"},
		{"forged nominal", func(b *types.Builtins, code, repr *types.TCon) Expr {
			return &Quote{Template: 0, Ty: &types.TCon{Unique: b.Int.Unique, Name: "Meta.Code"}}
		}, "stage value must have declared type Meta.Code"},
	} {
		t.Run(test.name, func(t *testing.T) {
			sup := &types.Supply{}
			b := types.NewBuiltins(sup)
			code := &types.TCon{Unique: sup.NextUnique(), Name: "Meta.Code"}
			repr := &types.TCon{Unique: sup.NextUnique(), Name: "Meta.TypeRepr"}
			body := test.body(b, code, repr)
			p := &Prog{ADTs: []*types.ADTInfo{{Con: code}, {Con: repr}}, Defs: []Def{{Name: "stage", Type: body.Type(), Body: body}}}
			var messages []string
			for _, err := range LintStage(p, b) {
				messages = append(messages, err.Error())
			}
			if !strings.Contains(strings.Join(messages, "\n"), test.want) {
				t.Fatalf("errors = %v, want %s", messages, test.want)
			}
		})
	}
}

func TestStageAttributeLookupChecksProjectionAndStoredTypes(t *testing.T) {
	sup := &types.Supply{}
	b := types.NewBuiltins(sup)
	nominal := func(name string) *types.TCon { return &types.TCon{Unique: sup.NextUnique(), Name: name} }
	bagTy, siteTy, itemsTy, attachedTy := nominal("Meta.Attributes"), nominal("Meta.Site"), nominal("Meta.Items"), nominal("Meta.Attached")
	itemVar, attachedVar := sup.FreshRigid(types.General), sup.FreshRigid(types.General)
	nilCtor := &types.CtorInfo{Name: "Meta.NoItems", Index: 0, Result: itemsTy}
	itemCtor := &types.CtorInfo{Name: "Meta.Item", Index: 1, Fields: []types.Type{itemVar, &types.TCon{Unique: itemsTy.Unique, Name: itemsTy.Name, Args: []types.Type{itemVar}}}, Result: itemsTy}
	attachedCtor := &types.CtorInfo{Name: "Meta.Attached.__record", Fields: []types.Type{attachedVar, siteTy}, Result: attachedTy}
	adts := []*types.ADTInfo{{Con: bagTy}, {Con: siteTy}, {Con: itemsTy, Params: []*types.TVar{itemVar}, Ctors: []*types.CtorInfo{nilCtor, itemCtor}}, {Con: attachedTy, Params: []*types.TVar{attachedVar}, Ctors: []*types.CtorInfo{attachedCtor}}}
	resultTy := &types.TCon{Unique: itemsTy.Unique, Name: itemsTy.Name, Args: []types.Type{&types.TCon{Unique: attachedTy.Unique, Name: attachedTy.Name, Args: []types.Type{b.Int}}}}
	for _, tc := range []struct {
		name      string
		requested types.Type
		kind      string
		stageOK   bool
	}{
		{"valid", b.Int, "int", true},
		{"retagged projection", b.String, "int", false},
		{"retagged payload", b.Int, "string", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bag := &TypeOf{Ty: bagTy, Repr: &meta.Attributes{Entries: []types.AttributeInfo{{Type: b.Int, Value: &meta.Data{Kind: tc.kind, Integer: 42}}}}}
			body := &AttributeLookup{Bag: bag, Requested: tc.requested, NilCtor: nilCtor, ItemCtor: itemCtor, AttachedCtor: attachedCtor, Ty: resultTy}
			p := &Prog{ADTs: adts, Defs: []Def{{Name: "lookup", Type: resultTy, Body: body}}}
			errors := LintStage(p, b)
			if (len(errors) == 0) != tc.stageOK {
				t.Fatalf("stage errors: %v", errors)
			}
			if len(Lint(p, b)) == 0 {
				t.Fatal("runtime lint accepted attribute operation")
			}
		})
	}
}
