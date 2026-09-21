package infer

import (
	"fmt"

	"github.com/waj/fango/internal/diag"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// The bundled Bytes type, recognized by canonical symbol exactly once, at its
// declaration, the same way the bundled List is (see list.go and
// doc/design/backend.md). A user type named `Bytes` in some other module has a
// different canonical symbol and keeps the ordinary ADT lowering.
//
// `type Bytes = Bytes` declares a type whose one constructor carries nothing,
// and the backends give it a `[]byte` representation. The constructor is the
// empty sequence, which is the only value the declaration could name; it stays
// out of the module's exposing list, and because a nullary constructor binds
// nothing, a match on it inside the module can neither discriminate nor
// observe anything the representation hides.
const (
	BytesTypeName = "Bytes.Bytes"
	BytesCtorName = "Bytes.Bytes"
)

// markBytesRepr gives the bundled Bytes its runtime representation once its
// constructor is resolved. The shape is verified rather than assumed, for the
// reason markListRepr verifies List's: the backends generate code against a
// fixed layout, and the library is a tree on disk a user can edit, so drift
// reports as a diagnostic against the declaration.
func markBytesRepr(adt *types.ADTInfo, at source.Span) []diag.Error {
	if adt.Con.Name != BytesTypeName {
		return nil
	}
	if err := checkBytesShape(adt); err != "" {
		return []diag.Error{diag.Errorf(at, "INVALID BUNDLED BYTES",
			"The bundled `%s` does not have the shape the compiler generates code against: %s. Restore it, or point FANGO_ROOT at a library matching this compiler.", BytesTypeName, err)}
	}
	adt.Repr = types.ReprBytes
	for _, c := range adt.Ctors {
		c.Repr = types.ReprBytes
	}
	return nil
}

// checkBytesShape returns why adt is not `type Bytes = Bytes`, or "" when it is.
func checkBytesShape(adt *types.ADTInfo) string {
	if len(adt.Params) != 0 {
		return fmt.Sprintf("it declares %d type parameters, want 0", len(adt.Params))
	}
	if len(adt.Ctors) != 1 {
		return fmt.Sprintf("it declares %d constructors, want 1", len(adt.Ctors))
	}
	only := adt.Ctors[0]
	if only.Name != BytesCtorName {
		return fmt.Sprintf("its constructor is `%s`, want `%s`", only.Name, BytesCtorName)
	}
	if len(only.Fields) != 0 {
		return fmt.Sprintf("`%s` has %d fields, want 0", BytesCtorName, len(only.Fields))
	}
	return ""
}
