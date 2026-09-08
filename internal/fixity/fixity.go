// Package fixity resolves operator runs into operator applications.
//
// Fango declares fixity in source (`infixl 6 (+)`), so the parser cannot
// shape an operator run as it reads it: the declarations may live in another
// module of the graph, and the graph is not known until every file is parsed.
// The parser therefore records each run flat as an ast.OpChain, and this
// package rewrites those chains into ast.BinOp trees once the graph-wide
// table is collected.
//
// Resolution runs inside module loading, before name resolution, so no
// ast.OpChain reaches inference, elaboration, or staging — every later phase
// sees only ast.BinOp, exactly as it did when precedence lived in the parser.
//
// A fixity binds an operator's spelling rather than the value it names, and
// the table is graph-global: one fixity per spelling for a whole program.
// That keeps a run's shape independent of which module it is written in,
// which is what lets the flat-then-resolve split work at all. Module-scoped
// fixity, where an import could change how a run parses, is deferred
// (doc/roadmap.md).
package fixity

import (
	"github.com/waj/fango/internal/ast"
	"github.com/waj/fango/internal/diag"
)

// Precedence bounds, as in Haskell: 9 binds tightest, and function
// application binds tighter than every operator.
const (
	MinPrec = 0
	MaxPrec = 9

	// DefaultPrec is the fixity of an operator with no declaration. Left
	// associative at the tightest level, so an undeclared operator behaves
	// like a plain function call chain.
	DefaultPrec  = 9
	DefaultAssoc = ast.AssocLeft
)

// Fixity is one operator's declared precedence and associativity.
type Fixity struct {
	Prec  int
	Assoc ast.Assoc
}

// Table maps an operator spelling to its fixity.
type Table map[string]Fixity

// shortCircuit are the two operators that are surface syntax rather than
// values: they elaborate to an `if` so the right operand stays unevaluated
// when the left one already decides the result. They cannot be declared,
// cannot be given a fixity, and cannot be named — so their fixity is built
// in here rather than read from Basics.
var shortCircuit = Table{
	"&&": {Prec: 3, Assoc: ast.AssocRight},
	"||": {Prec: 2, Assoc: ast.AssocRight},
}

// IsShortCircuit reports whether an operator is one of the two
// short-circuiting forms, which have no value to name.
func IsShortCircuit(op string) bool {
	_, ok := shortCircuit[op]
	return ok
}

// Builtin returns the fixities the language fixes itself. Every other
// operator, including all of Basics', declares its own.
func Builtin() Table {
	t := Table{}
	for op, f := range shortCircuit {
		t[op] = f
	}
	return t
}

// Lookup returns an operator's fixity, falling back to the default for an
// operator that has none declared.
func (t Table) Lookup(op string) Fixity {
	if f, ok := t[op]; ok {
		return f
	}
	return Fixity{Prec: DefaultPrec, Assoc: DefaultAssoc}
}

// Add records one declaration, reporting a conflict with an incompatible
// one already in the table. Re-declaring the same fixity is not an error,
// which keeps a module readable when it re-exports an operator.
func (t Table) Add(d *ast.FixityDecl) []diag.Error {
	if IsShortCircuit(d.Op) {
		return []diag.Error{diag.Errorf(d.OpSpan, "RESERVED OPERATOR",
			"(%s) is short-circuiting syntax rather than a value, so its fixity\nis fixed and cannot be declared.", d.Op)}
	}
	f := Fixity{Prec: d.Prec, Assoc: d.Assoc}
	if old, ok := t[d.Op]; ok && old != f {
		return []diag.Error{diag.Errorf(d.OpSpan, "CONFLICTING FIXITY",
			"(%s) is already declared `%s %d`, and fixity is shared across the\nwhole program. Give it one fixity, or rename one of the operators.",
			d.Op, old.Assoc, old.Prec)}
	}
	t[d.Op] = f
	return nil
}

// Collect adds every fixity declaration among decls to the table.
func (t Table) Collect(decls []ast.Decl) []diag.Error {
	var errs []diag.Error
	for _, d := range decls {
		if fd, ok := d.(*ast.FixityDecl); ok {
			errs = append(errs, t.Add(fd)...)
		}
	}
	return errs
}
