// Package types implements the representation described in doc/design.md,
// "Type inference": the Pred typeclass seam (currently always empty), the
// Eff Row field in TFun, and
// Unique-based TCon identity (load-bearing for REPL redefinition and the
// future module system).
package types

import "sort"

// ResumeID identifies one source handler operation clause. It is compiler-only
// proof data: resumes with different owners may be nested without being
// confused by the source checker or Core linter.
type ResumeID int

// Transport is the concrete execution protocol of one function arrow.  Direct
// arrows return their result normally; Exit arrows return an Outcome carrying
// either that result or a non-local exit.
type Transport uint8

const (
	Direct Transport = iota
	Exit
)

// Control is compiler-only function-arrow metadata. Polymorphic means that the
// defining module emits the bounded Direct/Exit ABI family and a use selects a
// member from its enclosing control context. Transport is the fixed lower bound.
type Control struct {
	Transport   Transport
	Polymorphic bool
}

func (c Control) Valid() bool { return c.Transport <= Exit }

func (c Control) Resolve(context Transport) Transport {
	if c.Polymorphic && context > c.Transport {
		return context
	}
	return c.Transport
}

func JoinControl(cs ...Control) Control {
	var out Control
	for _, c := range cs {
		if c.Transport > out.Transport {
			out.Transport = c.Transport
		}
		out.Polymorphic = out.Polymorphic || c.Polymorphic
	}
	return out
}

// FunctionControl returns the materialized arrow protocol. Source-owned type
// metadata predating Core may still carry only its effect row; an open row or
// custom evidence makes that arrow transport-polymorphic by contract.
func FunctionControl(fn *TFun) Control {
	out := fn.Control
	if fn.Eff.Tail != nil {
		out.Polymorphic = true
	}
	for _, label := range fn.Eff.Labels {
		if label.Abort {
			if out.Transport < Exit {
				out.Transport = Exit
			}
		} else if SurfaceName(label.Name) != "IO" {
			out.Polymorphic = true
		}
	}
	return out
}

type VarKind int

const (
	General VarKind = iota
	RowVar          // effect-row tail variable
)

type Type interface{ isType() }

// TVar is a type variable. Non-rigid TVars are metavariables minted during
// inference; none survive elaboration (a linted Core invariant). Rigid TVars
// (doc/design.md, "Type inference") are annotation skolems and scheme-bound variables: atomic in
// unification, invisible to substitution and defaulting, and legal in Core
// when declared by the enclosing definition's type parameters. Rigid vars
// share the Supply's ID space with metas, so IDs never collide.
type TVar struct {
	ID    int
	Kind  VarKind
	Rigid bool
}

// TCon is a type constructor application: Int, Shape, Maybe a. Identity is
// the Unique, not the name — the environment maps a name to its current
// unique.
type TCon struct {
	Unique int
	Name   string
	Args   []Type
}

// TFun is the shared internal representation for function arrows. Effects
// belong to the individual arrow whose application performs them.
type TFun struct {
	Arg     Type
	Eff     Row
	Ret     Type
	Control Control // omitted by source type printing; materialized for Core
	// OpenRow remembers a residual source row after Core erases row variables.
	// It is independent of transport polymorphism: a closed row containing a
	// resumptive effect also has polymorphic transport.
	OpenRow bool
}

func FunctionOpenRow(fn *TFun) bool { return fn.OpenRow || fn.Eff.Tail != nil }

// Row is a distinct-label effect row, optionally ending in an open tail.
type Row struct {
	Labels []EffLabel
	Tail   Type // nil for a closed row; *TVar{Kind: RowVar} when open
}

// EffLabel identifies an effect by its generation-stable Unique. Name is
// diagnostic syntax; Args instantiate parameterized effects such as Fail e.
type EffLabel struct {
	Scoped  bool // compiler-only generative permission; no runtime evidence
	Binding bool // optional permission available for binding to an activation

	Unique int
	Name   string
	Args   []Type
	Abort  bool // every operation in this (uniform-discipline) effect aborts

}

func (r Row) Empty() bool { return len(r.Labels) == 0 && r.Tail == nil }

// SortedRow returns a deterministic copy ordered by effect identity.
func SortedRow(r Row) Row {
	labels := append([]EffLabel(nil), r.Labels...)
	sort.Slice(labels, func(i, j int) bool { return labels[i].Unique < labels[j].Unique })
	return Row{Labels: labels, Tail: r.Tail}
}

// Scheme is a ∀-quantified type and its compiler-only capture summary.
// Vars holds the quantified rigid variables in first-occurrence order. Row variables are erased before Core; the
// remaining variables become Go type parameters in this same order.
// Instantiation replaces them with fresh metas (SubstRigid); Preds is the
// typeclass seam currently used by compiler-owned native obligations.
// CaptureVars are independently freshened at a value occurrence; they never
// appear in user-facing type printing.
type Scheme struct {
	ScopedRow    *TVar // universally bound row in the final callback parameter
	ScopedArity  int
	CallbackBase *Row // lower bound when invoking a scoped callback parameter

	Vars        []*TVar
	Preds       []Pred
	Body        Type
	CaptureVars []CaptureVar
	Captures    CaptureSet
}

// Pred is a typeclass-shaped obligation. Eq, Ord, and Show are currently
// introduced only by native declarations and discharged statically.
type Pred struct {
	Class string
	Ty    Type
}

// ClassInfo owns a nominal, compiler-internal single-constructor dictionary.
// Its fields are the declared methods, in declaration order.
type ClassInfo struct {
	Name    string
	Param   *TVar
	Dict    *ADTInfo
	Methods []MethodInfo
}

type MethodInfo struct {
	Name  string
	Type  Type
	Class *ClassInfo
	Index int
}

func (c *ClassInfo) DictType(t Type) *TCon {
	return &TCon{Unique: c.Dict.Con.Unique, Name: c.Dict.Con.Name, Args: []Type{t}}
}

// NativeInfo is declaration metadata shared by inference, Core, and both
// backends. Template is nil for a Go-sidecar call.
//
// ParamWrappers, ResultWrapper, and Fallible describe the boundary shapes a
// sidecar call may use beyond plain scalars (doc/design.md, "Go backend and
// runtime"). They are resolved once, after the module's types are declared,
// so both backends read the same constructors instead of re-deriving them.
type NativeInfo struct {
	Storage      NativeStorage
	Name, Module string
	Scheme       Scheme
	Arity        int
	Template     *string
	Effect       *EffectInfo
	// ParamWrappers[i] is the single-boundary-value constructor parameter i
	// is wrapped in, or nil for a plain scalar or Unit. Its length is Arity.
	ParamWrappers []*CtorInfo
	// ResultWrapper wraps the plain result, or the Ok payload when Fallible
	// is set; nil for a plain scalar or Unit.
	ResultWrapper *CtorInfo
	// Fallible is set when the result is `Result IO.Error T` and the Go
	// function returns an error alongside its payload.
	Fallible *FallibleShape
}

// FallibleShape is everything a backend needs to turn a Go error into
// `Err (IO.Error {...})` and a payload into `Ok payload`. Kinds indexes
// IO.Kind's constructors in declaration order, matching fangort's kind codes.
type FallibleShape struct {
	Err, Ok *CtorInfo
	Error   *CtorInfo
	Kinds   []*CtorInfo
	// Classifier selects the runtime mapping from a Go error to this error
	// record. It is "io" for File and "net" for Net.
	Classifier string
	// Field positions inside Error's constructor.
	KindIdx, LocationIdx, MessageIdx int
	// Payload is T, the Ok field's type; Unit when the Go function returns
	// only an error.
	Payload Type
}

func (*TVar) isType() {}
func (*TCon) isType() {}
func (*TFun) isType() {}
func (Row) isType()   {}

// SubstRigid replaces rigid variables according to m (ID → replacement) —
// scheme instantiation and constructor-field instantiation. Metas and rigid
// vars outside m are left alone.
func SubstRigid(t Type, m map[int]Type) Type {
	switch t := t.(type) {
	case *TVar:
		if t.Rigid {
			if r, ok := m[t.ID]; ok {
				return r
			}
		}
		return t
	case *TCon:
		if len(t.Args) == 0 {
			return t
		}
		args := make([]Type, len(t.Args))
		for i, a := range t.Args {
			args[i] = SubstRigid(a, m)
		}
		return &TCon{Unique: t.Unique, Name: t.Name, Args: args}
	case *TFun:
		return &TFun{Arg: SubstRigid(t.Arg, m), Eff: substRigidRow(t.Eff, m), Ret: SubstRigid(t.Ret, m), Control: t.Control, OpenRow: t.OpenRow}
	case Row:
		return substRigidRow(t, m)
	default:
		return t
	}
}

func substRigidRow(r Row, m map[int]Type) Row {
	labels := make([]EffLabel, len(r.Labels))
	for i, l := range r.Labels {
		args := make([]Type, len(l.Args))
		for j, a := range l.Args {
			args[j] = SubstRigid(a, m)
		}
		labels[i] = EffLabel{Unique: l.Unique, Name: l.Name, Args: args, Abort: l.Abort, Scoped: l.Scoped, Binding: l.Binding}
	}
	var tail Type
	if r.Tail != nil {
		tail = SubstRigid(r.Tail, m)
	}
	return Row{Labels: labels, Tail: tail}
}

// RigidVarsIn collects the rigid variables free in t, deduplicated, in
// first-occurrence order — a definition's type parameters, in the order
// codegen emits them and call sites instantiate them.
func RigidVarsIn(t Type) []*TVar {
	var vars []*TVar
	seen := map[int]bool{}
	var walk func(Type)
	walk = func(t Type) {
		switch t := t.(type) {
		case *TVar:
			if t.Rigid && !seen[t.ID] {
				seen[t.ID] = true
				vars = append(vars, t)
			}
		case *TCon:
			for _, a := range t.Args {
				walk(a)
			}
		case *TFun:
			walk(t.Arg)
			walk(t.Eff)
			walk(t.Ret)
		case Row:
			for _, l := range t.Labels {
				for _, a := range l.Args {
					walk(a)
				}
			}
			if t.Tail != nil {
				walk(t.Tail)
			}
		}
	}
	walk(t)
	return vars
}

// Equal is structural type equality. String comparison via Show is not a
// substitute: the printer normalizes variables per printer instance, so two
// different types can print alike (and vice versa) once TVars are legal in
// Core.
func Equal(a, b Type) bool {
	switch a := a.(type) {
	case *TVar:
		bv, ok := b.(*TVar)
		return ok && a.ID == bv.ID && a.Kind == bv.Kind && a.Rigid == bv.Rigid
	case *TCon:
		bc, ok := b.(*TCon)
		if !ok || a.Unique != bc.Unique || len(a.Args) != len(bc.Args) {
			return false
		}
		for i := range a.Args {
			if !Equal(a.Args[i], bc.Args[i]) {
				return false
			}
		}
		return true
	case *TFun:
		bf, ok := b.(*TFun)
		return ok && Equal(a.Arg, bf.Arg) && Equal(a.Eff, bf.Eff) && Equal(a.Ret, bf.Ret)
	case Row:
		br, ok := b.(Row)
		if !ok || len(a.Labels) != len(br.Labels) || (a.Tail == nil) != (br.Tail == nil) {
			return false
		}
		as, bs := SortedRow(a), SortedRow(br)
		for i := range as.Labels {
			al, bl := as.Labels[i], bs.Labels[i]
			if al.Unique != bl.Unique || len(al.Args) != len(bl.Args) {
				return false
			}
			for j := range al.Args {
				if !Equal(al.Args[j], bl.Args[j]) {
					return false
				}
			}
		}
		return as.Tail == nil || Equal(as.Tail, bs.Tail)
	default:
		return false
	}
}

// Repr selects how a nominal type is represented at run time. It is a backend
// concern only: inference, elaboration, Core, the linter, reflection, and the
// deriver treat every ADT identically whatever its Repr.
type Repr uint8

const (
	ReprADT       Repr = iota // marker interface plus one struct per constructor
	ReprList                  // fangort.List, the bundled List type
	ReprBytes                 // fangort.Bytes, the bundled Bytes type
	ReprNativeAny             // Go any, the bundled Runtime.Native.Any type
)

// CtorInfo is one constructor's row in the constructor table (doc/design.md, "Type inference"), shared
// by pattern checking, exhaustiveness checking, and codegen.
type CtorInfo struct {
	Name   string
	Index  int    // declaration position; drives layout and tree ordering
	Fields []Type // solved constructor field types
	Result *TCon  // the ADT this constructor belongs to
	// Repr mirrors the owning ADT's representation, because the interpreter
	// discriminates a construction from the CtorInfo alone.
	Repr Repr
}

// ValueType is the constructor used as a value: fields curried onto the
// result (`Circle : Float -> Shape`).
func (c *CtorInfo) ValueType() Type {
	var t Type = c.Result
	for i := len(c.Fields) - 1; i >= 0; i-- {
		t = &TFun{Arg: c.Fields[i], Ret: t}
	}
	return t
}

// ADTInfo is one declared type's constructor-table entry, constructors in
// declaration order. Params are the declaration's type parameters as rigid
// vars (empty for monomorphic types); constructor Fields and Result are
// expressed over them.
type ADTInfo struct {
	NativeIndexed bool // phantom native handle; its representation cannot be opened by Fango
	// Resource marks opaque native storage regardless of representation.
	Resource bool
	Con      *TCon
	Params   []*TVar
	// ParamKindsKnown distinguishes a header's initially unresolved parameter
	// kinds from kinds established while resolving its constructor fields.
	ParamKindsKnown []bool
	Ctors           []*CtorInfo
	// Repr selects the backends' runtime representation. Every source type is
	// ReprADT; the bundled List and Bytes are recognized at their declarations
	// (doc/design/backend.md).
	Repr Repr
	// RecordFields is non-nil for a standalone nominal record. The sole
	// synthetic constructor remains an internal representation detail.
	RecordFields []RecordFieldInfo
}

type RecordFieldInfo struct {
	Name        string
	Type        Type
	JSONKey     string
	JSONSkip    bool
	JSONDefault any // resolved ast.Expr; kept opaque to avoid a types -> ast cycle
}

func (a *ADTInfo) IsRecord() bool { return a.RecordFields != nil }

func (a *ADTInfo) RecordField(name string) (int, *RecordFieldInfo) {
	for i := range a.RecordFields {
		if a.RecordFields[i].Name == name {
			return i, &a.RecordFields[i]
		}
	}
	return -1, nil
}

// EffectInfo is one declared algebraic effect. Params are rigid variables
// shared by its operation schemes; identity follows the same generational
// Unique discipline as type constructors.
type EffectInfo struct {
	Unique int
	Name   string
	Params []*TVar
	Ops    []*EffectOp
	// Scoped is compiler-owned policy. Source effect declarations are durable
	// by default; State/resource milestones mark the capabilities whose
	// handler activation must not escape.
	Scoped bool
	// Suspension is set only by the bundled-module loader. Its operations
	// elaborate to compiler-owned suspension rather than evidence dispatch.

}

// EffectOp is the runtime-relevant, declaration-ordered description of an
// operation. Scheme includes effect parameters, operation-local variables,
// and row variables; Params/Result describe its fully saturated call.
type EffectOp struct {
	// Invocation is the service's fixed protocol. Its execution capability is
	// passed separately at each invocation, never retained by service evidence.
	Owner      *EffectInfo
	Index      int
	Name       string
	Scheme     Scheme
	Arity      int
	ParamTypes []Type
	ResultType Type
	LocalVars  []*TVar
	Builtin    bool
	Abort      bool
	Native     *NativeInfo
	// BorrowsEvidence says the operation result retains the current scoped
	// evidence activation. It is reserved for compiler-owned resource APIs.
	BorrowsEvidence bool
	// RetainsArguments marks compiler-owned store operations (for example
	// State.put). Passing a value to one transfers that value's captures into
	// the destination evidence scope and is checked for cross-scope escape.
	RetainsArguments bool
}

func SubstPreds(ps []Pred, m map[int]Type) []Pred {
	out := make([]Pred, len(ps))
	for i, p := range ps {
		out[i] = Pred{Class: p.Class, Ty: SubstRigid(p.Ty, m)}
	}
	return out
}

// ParamSubst builds the rigid-var substitution instantiating the type's
// parameters at args (parallel to Params).
func (a *ADTInfo) ParamSubst(args []Type) map[int]Type {
	if len(a.Params) == 0 {
		return nil
	}
	m := make(map[int]Type, len(a.Params))
	for i, p := range a.Params {
		m[p.ID] = args[i]
	}
	return m
}

// InstFields returns c's field types instantiated at args — the type
// arguments of the constructor's result type.
func (a *ADTInfo) InstFields(c *CtorInfo, args []Type) []Type {
	if len(a.Params) == 0 {
		return c.Fields
	}
	m := a.ParamSubst(args)
	fields := make([]Type, len(c.Fields))
	for i, f := range c.Fields {
		fields[i] = SubstRigid(f, m)
	}
	return fields
}

// CtorNamed returns the constructor with the given name, or nil.
func (a *ADTInfo) CtorNamed(name string) *CtorInfo {
	for _, c := range a.Ctors {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// Supply mints metavariable IDs and TCon uniques. It is session-scoped and
// passed in explicitly (never a global): the REPL needs one supply across
// many interactive inputs.
type Supply struct {
	nextVar     int
	nextUnique  int
	nextScope   ScopeID
	nextCapture CaptureVar
}

func (s *Supply) FreshVar(kind VarKind) *TVar {
	v := &TVar{ID: s.nextVar, Kind: kind}
	s.nextVar++
	return v
}

// FreshRigid mints a rigid variable (annotation skolem or scheme-bound var)
// from the same ID space as metas, so IDs never collide across the two.
func (s *Supply) FreshRigid(kind VarKind) *TVar {
	v := &TVar{ID: s.nextVar, Kind: kind, Rigid: true}
	s.nextVar++
	return v
}

func (s *Supply) NextUnique() int {
	u := s.nextUnique
	s.nextUnique++
	return u
}

func (s *Supply) FreshScope() ScopeID {
	s.nextScope++
	return s.nextScope
}

func (s *Supply) FreshCapture() CaptureVar {
	s.nextCapture++
	return s.nextCapture
}

// Builtins holds the predefined type constructors, uniques minted from the
// session supply. The builtins are all reachable from current surface syntax;
// exist so the registry's shape is final.
type Builtins struct {
	Int, Float, String, Char, Bool, Unit *TCon
}

func NewBuiltins(s *Supply) *Builtins {
	mk := func(name string) *TCon {
		return &TCon{Unique: s.NextUnique(), Name: name}
	}
	return &Builtins{
		Int:    mk("Int"),
		Float:  mk("Float"),
		String: mk("String"),
		Char:   mk("Char"),
		Bool:   mk("Bool"),
		Unit:   mk("()"), // displayed Elm-style; identity is the Unique, not the name
	}
}
