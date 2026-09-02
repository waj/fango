// Package types is the type representation from DESIGN.md §7.1, final in
// shape from S0: the Pred typeclass seam (always empty until typeclasses),
// the Eff Row field in TFun (always the empty closed row until S7), and
// Unique-based TCon identity (load-bearing for REPL redefinition and the
// future module system).
package types

type VarKind int

const (
	General VarKind = iota
	Number          // Elm-style `number` kind flag: Int or Float
	RowVar          // reserved for effect rows (S7)
)

type Type interface{ isType() }

// TVar is a metavariable minted during inference. None survive elaboration
// (a linted Core invariant).
type TVar struct {
	ID   int
	Kind VarKind
}

// TCon is a type constructor application: Int, Shape, Maybe a. Identity is
// the Unique, not the name — the environment maps a name to its current
// unique.
type TCon struct {
	Unique int
	Name   string
	Args   []Type
}

// TFun is a function arrow. Eff is always the empty closed row until S7:
// the field, not the machinery.
type TFun struct {
	Arg Type
	Eff Row
	Ret Type
}

// Row is an effect row. Declared from S0 so TFun's shape never changes; no
// row logic exists before S7.
type Row struct {
	Labels []string
	Tail   Type // nil for a closed row; *TVar{Kind: RowVar} when open (S7)
}

func (r Row) Empty() bool { return len(r.Labels) == 0 && r.Tail == nil }

// Scheme is a ∀-quantified type. NumVars counts the quantified variables
// (referenced in Body as bound indices via TVar IDs 0..NumVars-1 after
// generalization); Preds is the typeclass seam, empty until typeclasses.
type Scheme struct {
	NumVars int
	Preds   []Pred
	Body    Type
}

// Pred is a typeclass predicate — the reserved seam. Always empty in the MVP.
type Pred struct {
	Class string
	Ty    Type
}

func (*TVar) isType() {}
func (*TCon) isType() {}
func (*TFun) isType() {}

// Supply mints metavariable IDs and TCon uniques. It is session-scoped and
// passed in explicitly (never a global): the REPL needs one supply across
// many interactive inputs.
type Supply struct {
	nextVar    int
	nextUnique int
}

func (s *Supply) FreshVar(kind VarKind) *TVar {
	v := &TVar{ID: s.nextVar, Kind: kind}
	s.nextVar++
	return v
}

func (s *Supply) NextUnique() int {
	u := s.nextUnique
	s.nextUnique++
	return u
}

// Builtins holds the predefined type constructors, uniques minted from the
// session supply. Only Int is reachable from S0 surface syntax; the rest
// exist so the registry's shape is final.
type Builtins struct {
	Int, Float, String, Bool, Unit *TCon
}

func NewBuiltins(s *Supply) *Builtins {
	mk := func(name string) *TCon {
		return &TCon{Unique: s.NextUnique(), Name: name}
	}
	return &Builtins{
		Int:    mk("Int"),
		Float:  mk("Float"),
		String: mk("String"),
		Bool:   mk("Bool"),
		Unit:   mk("Unit"),
	}
}
