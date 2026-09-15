package types

import "testing"

func TestInspectionRejectsMissingNominalDeclarations(t *testing.T) {
	if InspectionShapeSafe(&TCon{Name: "Private.Unknown", Unique: 123}, nil) {
		t.Fatal("missing declaration accepted as immutable data")
	}
}

func TestInspectionChecksEveryConstructorAndNestedTypeArgument(t *testing.T) {
	sup := &Supply{}
	b := NewBuiltins(sup)
	a := sup.FreshRigid(General)
	con := &TCon{Unique: sup.NextUnique(), Name: "Private.Tree", Args: []Type{a}}
	adt := &ADTInfo{Con: con, Params: []*TVar{a}, Ctors: []*CtorInfo{{Name: "Empty", Result: con}, {Name: "Node", Fields: []Type{a, con}, Result: con}}}
	adts := map[int]*ADTInfo{con.Unique: adt}
	tree := &TCon{Unique: con.Unique, Name: con.Name, Args: []Type{b.Int}}
	if !InspectionShapeSafe(tree, adts) {
		t.Fatal("ordinary private recursive data rejected")
	}
	unsafe := &TCon{Unique: sup.NextUnique(), Name: "Private.Handle"}
	adts[unsafe.Unique] = &ADTInfo{Con: unsafe, Resource: true}
	for _, arg := range []Type{unsafe, &TFun{Arg: b.Int, Ret: b.Int}} {
		tree.Args = []Type{arg}
		if InspectionShapeSafe(tree, adts) {
			t.Fatal("unsafe type argument accepted")
		}
	}
	tree.Args = []Type{b.Int}
	adt.Ctors = append(adt.Ctors, &CtorInfo{Name: "Hidden", Fields: []Type{unsafe}, Result: con})
	if InspectionShapeSafe(tree, adts) {
		t.Fatal("unused resource-bearing constructor ignored")
	}
}
