package infer

import (
	"fmt"

	"github.com/waj/fango/internal/types"
)

// The bundled list type, recognized by canonical symbol exactly once, at its
// declaration. Everything downstream compares identities: the discriminator
// rides on the ADT and constructor rows both backends already hold, so neither
// has to look a name up again (doc/roadmap-list.md).
//
// Bracket syntax already names these constructors — the parser lowers `[a | b]`
// to `List.Cons` and the loader adds the module edge — so this adds no new
// coupling to a spelling, only a second use of an existing one.
const (
	ListTypeName = "List.List"
	ListNilName  = "List.Nil"
	ListConsName = "List.Cons"
)

// markListRepr gives the bundled List its runtime representation once its
// constructors are resolved. A user type named `List` in some other module has
// a different canonical symbol and is never marked, so it keeps the ordinary
// cons lowering.
//
// The shape is verified rather than assumed. The backends emit head/tail
// projections against a fixed constructor layout, so a stdlib that drifted from
// the compiler would miscompile silently; failing here instead is the
// detectable form of the lockstep invariant that unembedding the bundled
// sources will have to preserve (doc/roadmap.md).
func markListRepr(adt *types.ADTInfo) {
	if adt.Con.Name != ListTypeName {
		return
	}
	if err := checkListShape(adt); err != "" {
		panic("infer: the bundled " + ListTypeName + " does not have the shape the backends compile: " + err)
	}
	adt.Repr = types.ReprList
	for _, c := range adt.Ctors {
		c.Repr = types.ReprList
	}
}

// checkListShape returns why adt is not `type List a = Nil | Cons a (List a)`,
// or "" when it is.
func checkListShape(adt *types.ADTInfo) string {
	if len(adt.Params) != 1 {
		return fmt.Sprintf("it declares %d type parameters, want 1", len(adt.Params))
	}
	param := adt.Params[0]
	if len(adt.Ctors) != 2 {
		return fmt.Sprintf("it declares %d constructors, want 2", len(adt.Ctors))
	}
	nil_, cons := adt.Ctors[0], adt.Ctors[1]
	if nil_.Name != ListNilName || cons.Name != ListConsName {
		return fmt.Sprintf("its constructors are `%s` and `%s`, want `%s` then `%s`",
			nil_.Name, cons.Name, ListNilName, ListConsName)
	}
	if len(nil_.Fields) != 0 {
		return fmt.Sprintf("`%s` has %d fields, want 0", ListNilName, len(nil_.Fields))
	}
	if len(cons.Fields) != 2 {
		return fmt.Sprintf("`%s` has %d fields, want 2", ListConsName, len(cons.Fields))
	}
	if head, ok := cons.Fields[0].(*types.TVar); !ok || head != param {
		return fmt.Sprintf("`%s`'s first field is `%s`, want the declaration's type parameter",
			ListConsName, types.Show(cons.Fields[0]))
	}
	tail, ok := cons.Fields[1].(*types.TCon)
	if !ok || tail.Unique != adt.Con.Unique || len(tail.Args) != 1 {
		return fmt.Sprintf("`%s`'s second field is `%s`, want the type applied to its own parameter",
			ListConsName, types.Show(cons.Fields[1]))
	}
	if arg, ok := tail.Args[0].(*types.TVar); !ok || arg != param {
		return fmt.Sprintf("`%s`'s second field is `%s`, want the type applied to its own parameter",
			ListConsName, types.Show(cons.Fields[1]))
	}
	return ""
}
