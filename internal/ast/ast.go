// Package ast is the surface syntax tree: sealed interfaces with marker
// methods, every node carrying a Span.
package ast

import (
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

type Expr interface {
	isExpr()
	Span() source.Span
}

type IntLit struct {
	Value int64
	Sp    source.Span
	Raw   bool // compiler-internal Int payload for Num.fromInt
}

type FloatLit struct {
	Value float64
	Sp    source.Span
}

type StringLit struct {
	Value string
	Sp    source.Span
}

type CharLit struct {
	Value rune
	Sp    source.Span
}

// UnitLit is the sole value of the Unit type, written ().
type UnitLit struct{ Sp source.Span }

type Var struct {
	Name string
	Sp   source.Span
}

// Ctor is a constructor reference, including builtin True and False.
type Ctor struct {
	Name       string
	Sp         source.Span
	ListSyntax bool // parser-generated List.Nil/List.Cons; bypasses import lookup
}

// RecordLit is keyed construction of a nominal record, `Counts { lines = 1 }`.
type RecordLit struct {
	Name     string
	NameSpan source.Span
	Fields   []RecordExprField
	Sp       source.Span
}

type RecordExprField struct {
	Name     string
	NameSpan source.Span
	Value    Expr
	Records  []string // resolver-visible nominal record candidates for this label
}

// RecordGet is tight-binding field projection, `value.field`.
type RecordGet struct {
	Record    Expr
	Field     string
	FieldSpan source.Span
	Records   []string // resolver-visible nominal record candidates
}

// RecordUpdate copies a nominal record while replacing named fields.
type RecordUpdate struct {
	Record Expr
	Fields []RecordExprField
	Sp     source.Span
}

// App is curried application: `f x y` is App(App(f, x), y).
type App struct {
	Fn, Arg Expr
}

// Neg is unary minus — a distinct node (not BinOp with a zero) so spans and
// error messages stay clean.
type Neg struct {
	Operand Expr
	Sp      source.Span // minus sign through the operand
}

type If struct {
	Cond, Then, Else Expr
	Sp               source.Span // the `if` keyword
}

// Block is a statement-style body (doc/design.md, "Language semantics"): binding lines followed by one
// result expression. The parser collapses zero-binding blocks to the plain
// result expression, so a Block always has at least one binding.
type Block struct {
	Binds  []LocalBind
	Items  []BlockItem // ordered; nil for legacy binding-only blocks
	Result Expr
}

type BlockItem struct {
	BindIndex int
	Expr      Expr
}

// Param is one function parameter. Worker arity is the syntactic parameter
// count (doc/design.md, "Go backend and runtime"), which is why parameters stay explicit rather than
// desugaring to lambdas.
type Param struct {
	Name string // "_" discards; "()" is a Unit pattern in handler clauses
	Sp   source.Span
}

type LocalBind struct {
	Name      string
	NameSpan  source.Span
	Params    []Pattern  // non-empty: a local function equation
	Equations []Equation // non-nil only for a grouped local function
	Pattern   Pattern    // non-nil for a destructuring value binding
	Ann       *TypeAnn
	Body      Expr
}

// Lambda is `\x -> e` / `\x y -> e` — multi-param in the AST for clean
// spans and dumps; typing and elaboration treat it as curried.
type Lambda struct {
	Params []Pattern
	Body   Expr
	Sp     source.Span // the backslash
}

// TypeAnn is a `name : Type` annotation line attached to the definition
// directly below it.
type TypeAnn struct {
	Type  TypeExpr
	Preds []PredExpr
	Sp    source.Span // colon through the end of the type
}

type PredExpr struct {
	Class string
	Ty    TypeExpr
	Sp    source.Span
}

type ClassDecl struct {
	Name     string
	NameSpan source.Span
	Param    Param
	Methods  []OpSig
}

type InstanceDecl struct {
	Head    PredExpr
	Preds   []PredExpr
	Methods []*ValueDecl
	Owner   string
}

// DeriverDecl supplies compile-time generators for every method of one class.
// It is visible by dependency, like an instance, and is never emitted.
type DeriverDecl struct {
	Class     string
	ClassSpan source.Span
	Methods   []*ValueDecl
	Owner     string
}

func (*ClassDecl) isDecl()    {}
func (*InstanceDecl) isDecl() {}
func (*DeriverDecl) isDecl()  {}

// TypeExpr is the surface type grammar: ground names, `()`, `->` arrows,
// effect rows attached to arrows, and type variables.
type TypeExpr interface {
	isTypeExpr()
	Span() source.Span
}

type TName struct {
	Name string // "Int", "Bool", … — "()" for unit
	Sp   source.Span
}

type TVarName struct {
	Name string // lowercase: a type variable
	Sp   source.Span
}

// TFunExpr is a function arrow. Eff is nil for a plain `->` — pure as
// written — and non-nil only for `->{…}` (doc/design.md, "Functions and effects"). The dumper omits a nil
// row, keeping pure-arrow dumps compact and stable.
type TFunExpr struct {
	Arg, Ret TypeExpr
	Eff      *EffRow
}

// EffRow is the surface effect row on an arrow: `->{Console}`,
// `->{Db, Fail String}`, or `->{Console | e}` with an explicit open tail.
type EffRow struct {
	Labels []EffLabelExpr
	Tail   string      // "" when closed; a lowercase row variable otherwise
	TailSp source.Span // zero when Tail is ""
	Sp     source.Span // the `{` through the `}`
}

// EffLabelExpr is one label in a surface row: an effect name plus type
// arguments, `Fail String`. Args are type atoms, as in constructor payloads.
type EffLabelExpr struct {
	Name   string
	NameSp source.Span
	Args   []TypeExpr
}

func (r *EffRow) Span() source.Span { return r.Sp }

// TApp is type application, `Maybe Int`. The head is always an uppercase
// name — type variables cannot head applications (no higher kinds, doc/design.md, "Go backend and runtime").
type TApp struct {
	Name   string
	NameSp source.Span
	Args   []TypeExpr // non-empty
}

func (*TName) isTypeExpr()    {}
func (*TVarName) isTypeExpr() {}
func (*TFunExpr) isTypeExpr() {}
func (*TApp) isTypeExpr()     {}

func (t *TName) Span() source.Span    { return t.Sp }
func (t *TVarName) Span() source.Span { return t.Sp }
func (t *TFunExpr) Span() source.Span { return t.Arg.Span().Merge(t.Ret.Span()) }
func (t *TApp) Span() source.Span     { return t.NameSp.Merge(t.Args[len(t.Args)-1].Span()) }

// BinOp stays a distinct node rather than desugaring to App: errors should
// point at the operator, and `&&`/`||` are surface syntax with no value.
// Op holds the operator's name — a spelling before name resolution, a
// canonical symbol after it.
type BinOp struct {
	Op     string
	OpSpan source.Span
	L, R   Expr
}

// OpChain is the parser's shape for a run of infix operators. Fixity is
// declared in source and is not known until the whole module graph is
// loaded, so the parser records the run flat and internal/fixity rebuilds it
// into BinOp trees before name resolution. No OpChain survives module
// loading, so later phases only ever see BinOp.
type OpChain struct {
	Operands []Expr // len(Operands) == len(Ops)+1
	Ops      []OpRef
}

// OpRef is one operator occurrence inside an OpChain.
type OpRef struct {
	Op string
	Sp source.Span
}

// Case is `case scrutinee of` followed by branches aligned at the column of
// the first pattern token (layout rule 2, doc/reference.md, "Modules, imports, and source layout"). Branch bodies are statement
// blocks (doc/design.md, "Language semantics") or inline expressions.
type Case struct {
	Scrutinee Expr
	Branches  []CaseBranch
	Sp        source.Span // the `case` keyword
}

type CaseBranch struct {
	Pattern Pattern
	Body    Expr
}

// Handle is `handle <expr> of` followed by operation clauses aligned at the
// column of the first clause token — layout rule 2, shared with `case`
// (see doc/design.md, "Functions and effects").
type Handle struct {
	Body    Expr
	State   *HandlerState
	Clauses []HandleClause
	Return  *ReturnClause // optional `return x -> …`
	Sp      source.Span   // the `handle` keyword
}

// HandlerState is the optional compiler-owned cell introduced by
// `handle body with name = initial of`. Name denotes an immutable snapshot in
// operation and return clauses; it is deliberately not in scope in Body.
type HandlerState struct {
	Name     string
	NameSpan source.Span
	Initial  Expr
}

// HandleClause is one operation clause, `print s -> …`. Params bind the
// operation's arguments; `resume` is in scope in the body.
type HandleClause struct {
	Op        string
	OpSpan    source.Span
	Params    []Pattern
	Equations []Equation
	Body      Expr
}

// ReturnClause is the optional `return x -> …` clause wrapping the handled
// body's normal result. `return` is a *contextual* keyword — it is not
// reserved, so it stays usable as an ordinary name everywhere else.
type ReturnClause struct {
	Param     Pattern
	Equations []Equation
	Body      Expr
	Sp        source.Span // the `return` token
}

// Resume is the one-shot continuation bound inside a handler clause. It
// parses as an expression head, so `resume ()` is ordinary application.
type Resume struct {
	Sp        source.Span
	NextState Expr // non-nil for `resume value with nextState`
}

// Quote goes up a stage: it does not evaluate Body, it describes it. Body is
// resolved in the quoting module's scope but never checked where it is
// written — its holes have no type yet (doc/design.md, "Compile-time
// metaprogramming").
type Quote struct {
	Body Expr
	Sp   source.Span // the `quote` keyword through the quoted atom
}

// Splice goes down a stage. At quote depth 0 it evaluates Operand during
// compilation and pastes the resulting code in its place; inside a quote it
// marks a hole.
type Splice struct {
	Operand Expr
	Sp      source.Span // `$(` through `)`
}

// TypeOf reflects one closed, fully-applied type at compile time. Visible is
// filled by module resolution and records which nominal schemas the writing
// module may inspect through local or qualified access.
type TypeOf struct {
	Ty      TypeExpr
	Value   types.Type
	Visible map[string]bool
	Sp      source.Span
}

// MetaValue is an internal expression fragment used to invoke a deriver. It
// never appears in parsed source and is rejected if it survives staging.
type MetaValue struct {
	Value any
	Ty    types.Type
	Sp    source.Span
}

// Pattern is the surface pattern grammar (doc/reference.md, "Algebraic data types and matching"): variables, wildcard,
// literals, and constructor patterns with nested argument patterns.
type Pattern interface {
	isPattern()
	Span() source.Span
}

// Equation is one source row of a named function, method, or handler clause.
// Single-row definitions continue to use the owning node's Params and Body
// fields so their stable AST dump stays unchanged; Equations is populated
// when adjacent rows have been grouped.
type Equation struct {
	Params   []Pattern
	Body     Expr
	NameSpan source.Span
}

type PVar struct {
	Name string
	Sp   source.Span
}

type PWildcard struct {
	Sp source.Span
}

// PUnit is the proper Unit pattern. It is distinct from a discarded binder:
// Unit has one inhabitant, so this pattern is exhaustive for Unit values.
type PUnit struct {
	Sp source.Span
}

type PInt struct {
	Value int64
	Sp    source.Span
}

type PFloat struct {
	Value float64
	Sp    source.Span
}

type PString struct {
	Value string
	Sp    source.Span
}

type PChar struct {
	Value rune
	Sp    source.Span
}

// PPin compares the matched value with an existing lexical or global value.
type PPin struct {
	Name         string
	NameSpan, Sp source.Span
}

type PRecord struct {
	Name     string
	NameSpan source.Span
	Fields   []RecordPatternField
	Sp       source.Span
}

type RecordPatternField struct {
	Name     string
	NameSpan source.Span
	Pattern  Pattern
}

type PCtor struct {
	Name       string
	NameSpan   source.Span
	Args       []Pattern
	ListSyntax bool // parser-generated List.Nil/List.Cons; bypasses import lookup
}

func (*PVar) isPattern()      {}
func (*PWildcard) isPattern() {}
func (*PUnit) isPattern()     {}
func (*PInt) isPattern()      {}
func (*PFloat) isPattern()    {}
func (*PString) isPattern()   {}
func (*PChar) isPattern()     {}
func (*PPin) isPattern()      {}
func (*PRecord) isPattern()   {}
func (*PCtor) isPattern()     {}

func (p *PVar) Span() source.Span      { return p.Sp }
func (p *PWildcard) Span() source.Span { return p.Sp }
func (p *PUnit) Span() source.Span     { return p.Sp }
func (p *PInt) Span() source.Span      { return p.Sp }
func (p *PFloat) Span() source.Span    { return p.Sp }
func (p *PString) Span() source.Span   { return p.Sp }
func (p *PChar) Span() source.Span     { return p.Sp }
func (p *PPin) Span() source.Span      { return p.Sp }
func (p *PRecord) Span() source.Span   { return p.Sp }
func (p *PCtor) Span() source.Span {
	if len(p.Args) == 0 {
		return p.NameSpan
	}
	return p.NameSpan.Merge(p.Args[len(p.Args)-1].Span())
}

func (*IntLit) isExpr()       {}
func (*FloatLit) isExpr()     {}
func (*StringLit) isExpr()    {}
func (*CharLit) isExpr()      {}
func (*UnitLit) isExpr()      {}
func (*Var) isExpr()          {}
func (*Ctor) isExpr()         {}
func (*RecordLit) isExpr()    {}
func (*RecordGet) isExpr()    {}
func (*RecordUpdate) isExpr() {}
func (*App) isExpr()          {}
func (*Neg) isExpr()          {}
func (*BinOp) isExpr()        {}
func (*OpChain) isExpr()      {}
func (*If) isExpr()           {}
func (*Block) isExpr()        {}
func (*Lambda) isExpr()       {}
func (*Case) isExpr()         {}
func (*Handle) isExpr()       {}
func (*Resume) isExpr()       {}
func (*Quote) isExpr()        {}
func (*Splice) isExpr()       {}
func (*TypeOf) isExpr()       {}
func (*MetaValue) isExpr()    {}

func (e *IntLit) Span() source.Span       { return e.Sp }
func (e *FloatLit) Span() source.Span     { return e.Sp }
func (e *StringLit) Span() source.Span    { return e.Sp }
func (e *CharLit) Span() source.Span      { return e.Sp }
func (e *UnitLit) Span() source.Span      { return e.Sp }
func (e *Var) Span() source.Span          { return e.Sp }
func (e *Ctor) Span() source.Span         { return e.Sp }
func (e *RecordLit) Span() source.Span    { return e.Sp }
func (e *RecordGet) Span() source.Span    { return e.Record.Span().Merge(e.FieldSpan) }
func (e *RecordUpdate) Span() source.Span { return e.Sp }
func (e *App) Span() source.Span          { return e.Fn.Span().Merge(e.Arg.Span()) }
func (e *Neg) Span() source.Span          { return e.Sp }
func (e *BinOp) Span() source.Span        { return e.L.Span().Merge(e.R.Span()) }
func (e *OpChain) Span() source.Span {
	return e.Operands[0].Span().Merge(e.Operands[len(e.Operands)-1].Span())
}
func (e *If) Span() source.Span { return e.Sp.Merge(e.Else.Span()) }
func (e *Block) Span() source.Span {
	if len(e.Items) > 0 && e.Items[0].Expr != nil {
		return e.Items[0].Expr.Span().Merge(e.Result.Span())
	}
	return e.Binds[0].NameSpan.Merge(e.Result.Span())
}
func (e *Lambda) Span() source.Span { return e.Sp.Merge(e.Body.Span()) }
func (e *Case) Span() source.Span {
	return e.Sp.Merge(e.Branches[len(e.Branches)-1].Body.Span())
}
func (e *Handle) Span() source.Span {
	if e.Return != nil {
		return e.Sp.Merge(e.Return.Body.Span())
	}
	return e.Sp.Merge(e.Clauses[len(e.Clauses)-1].Body.Span())
}
func (e *Resume) Span() source.Span    { return e.Sp }
func (e *Quote) Span() source.Span     { return e.Sp }
func (e *Splice) Span() source.Span    { return e.Sp }
func (e *TypeOf) Span() source.Span    { return e.Sp }
func (e *MetaValue) Span() source.Span { return e.Sp }

type Decl interface{ isDecl() }

type ValueDecl struct {
	Name      string
	NameSpan  source.Span
	Params    []Pattern  // non-empty: a function definition (worker; see doc/design.md, "Go backend and runtime")
	Equations []Equation // non-nil for adjacent same-name equations
	Ann       *TypeAnn   // nil when unannotated
	Body      Expr
	Native    *NativeBody
}

func (*ValueDecl) isDecl() {}

// PatternDecl is a strict, monomorphic top-level destructuring binding. The
// RHS is checked and evaluated once before all names in Pattern become
// visible.
type PatternDecl struct {
	Pattern Pattern
	Body    Expr
}

func (*PatternDecl) isDecl() {}

// TypeDecl is a nominal type declaration (doc/reference.md, "Algebraic data
// types and matching"). Its RHS is either constructor alternatives or a
// standalone record schema. Params declare polymorphic types.
type TypeDecl struct {
	Name         string
	NameSpan     source.Span
	Params       []Param // type parameters (lowercase)
	Ctors        []CtorDef
	RecordFields []RecordFieldDef // non-nil for `type T = { field : Type }`
	Deriving     []TName
	// ReflectionVisible is a resolver snapshot of nominal schemas accessible
	// where this declaration was written. Derived metadata inherits it.
	ReflectionVisible map[string]bool
}

type RecordFieldDef struct {
	Name     string
	NameSpan source.Span
	Type     TypeExpr
}

// CtorDef is one constructor alternative. Args are type atoms: named types
// or parenthesized type expressions.
type CtorDef struct {
	Name     string
	NameSpan source.Span
	Args     []TypeExpr
}

func (*TypeDecl) isDecl() {}

// EffectDecl declares an effect and its operations (doc/design.md, "Functions and effects"):
//
//	effect Console
//	    print    : String -> ()
//	    readLine : () -> String
//
// Params are the effect's type parameters (`effect Fail e`), mirroring
// TypeDecl.Params. Operation signatures are ordinary type expressions; the
// checker attaches the effect's own label to the operation's arrow.
type EffectDecl struct {
	Name     string
	NameSpan source.Span
	Params   []Param
	Ops      []OpSig
}

// OpSig is one operation signature line inside an `effect` declaration.
type OpSig struct {
	Name     string
	NameSpan source.Span
	Type     TypeExpr
	Native   *NativeBody
	Abort    bool // `abort name : ...`; the operation never resumes normally
}

// NativeBody marks a declaration implemented outside ordinary fango source.
// Template is nil for sidecar call form and non-nil for bundled inline form.
type NativeBody struct {
	Template *string
	Module   string // sidecar link module, filled by module resolution
	Sp       source.Span
}

// Assoc is an operator's associativity, declared by `infixl`, `infixr`, or
// bare `infix`.
type Assoc int

const (
	AssocLeft Assoc = iota
	AssocRight
	AssocNone
)

func (a Assoc) String() string {
	switch a {
	case AssocLeft:
		return "infixl"
	case AssocRight:
		return "infixr"
	default:
		return "infix"
	}
}

// FixityDecl declares one operator's precedence and associativity, as in
// `infixl 6 (+)`. Fixity is a property of the operator's spelling rather
// than of the value it names, so Op is never canonicalized.
type FixityDecl struct {
	Op     string
	Assoc  Assoc
	Prec   int
	OpSpan source.Span
	Sp     source.Span
}

func (*FixityDecl) isDecl() {}

func (*EffectDecl) isDecl() {}

type ModuleHeader struct {
	Name     string
	NameSpan source.Span
	Exposing Exposing
}

type Exposing struct {
	All   bool
	Items []ExposeItem
}

type ExposeItem struct {
	Name string
	All  bool
	Sp   source.Span
}

type Import struct {
	Module     string
	ModuleSpan source.Span
	Alias      string
	AliasSpan  source.Span
	Exposing   *Exposing
}

type Module struct {
	InstanceImports map[string]map[string]bool
	Header          *ModuleHeader
	Imports         []Import
	Decls           []Decl

	// UsesStaging records whether the parser built a Quote or a Splice. The
	// module loader adds the bundled `Meta` dependency only for files that
	// need it, so an ordinary program's graph is unchanged.
	UsesStaging bool

	// UsesLists records bracket list syntax. The module loader adds List as a
	// syntax dependency without exposing its ordinary names.
	UsesLists bool
}

// Spelling renders a declaration name the way it is written in source: an
// operator wears the parentheses that name it.
func Spelling(name string) string {
	if name == "" || isIdentStart(name[0]) {
		return name
	}
	return "(" + name + ")"
}

func isIdentStart(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
}
