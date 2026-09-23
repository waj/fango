// Package machine defines the checked execution IR used only for computations
// whose solved control transport is Machine. Semantic Core remains the
// language/backend contract; this package makes suspension storage, program
// counters, and inter-frame transfers explicit before either machine backend
// consumes them.
package machine

import (
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

type BlockID int

type Prog struct {
	Workers  []Worker
	Closures []Closure
	// Declared are the Machine families this unit calls but does not own: the
	// imported calling contract without blocks. Whole-program lowering owns
	// every family it calls and leaves this empty.
	Declared []Worker
}

// Closure connects one semantic Core lambda to its defunctionalized machine
// worker. Captures are supplied when the value is created; CallEvidence and
// the lambda argument are supplied when it is invoked.
type Closure struct {
	Expr             *core.Lambda
	Worker           string
	Captures         []Local
	CapturedEvidence []core.EffectInstance
	CallEvidence     []core.EffectInstance
	CapturedRows     []types.CaptureVar
	CallRow          types.CaptureVar
}

type Local struct {
	Name string
	Ty   types.Type
}

type Worker struct {
	Name     string
	Owner    string
	TyParams []*types.TVar
	Params   []Local
	// EffectParams are explicit lexical evidence inputs. Unlike term locals,
	// they keep nominal effect identity and capture metadata through lowering.
	EffectParams []core.EffectInstance
	RowEffects   []core.EffectInstance
	RowParam     types.CaptureVar
	Rows         []types.CaptureVar
	Result       types.Type
	Entry        BlockID
	Blocks       []Block
	Def          *core.Def
	StateToken   bool
	// SynchronousParams select the Exit representation for the checked
	// acquisition and release callbacks of the Scope intrinsic.
	SynchronousParams []int

	// Locals is the complete typed local namespace. Frame is the subset live
	// across at least one suspension or non-tail machine call.
	Locals []Local
	Frame  []Local
}

type Block struct {
	ID   BlockID
	Term Term

	// LiveIn and LiveOut are materialized proof data. Lint independently
	// recomputes them, so a later transform cannot silently retain too little
	// or too much state.
	LiveIn  []string
	LiveOut []string
}

type Term interface{ isTerm() }

// Eval evaluates a non-machine Core expression, binds its result, and moves
// to Next. Exit-producing expressions are allowed; the machine backend must
// turn an Exit result into the same completion/unwind protocol as other exits.
type Eval struct {
	Bind  Local
	Value core.Expr
	Next  BlockID
}

type Branch struct {
	Cond       core.Expr
	Then, Else BlockID
}

type SwitchCtor struct {
	Scrut   string
	ADT     *types.ADTInfo
	Cases   []CtorCase
	Default *BlockID
}

type CtorCase struct {
	Ctor  *types.CtorInfo
	Binds []Local // an empty Name means the constructor field is ignored
	Next  BlockID
}

type SwitchLit struct {
	Scrut   string
	Cases   []LitCase
	Default BlockID
}

type LitCase struct {
	Lit  core.Expr
	Next BlockID
}

// Suspend evaluates Request and yields it to the private machine driver.
// Resumption defines Bind and continues at Next. No continuation value is
// represented in this IR.
type Suspend struct {
	// Owner is preserved from semantic Core, never inferred from the active
	// dispatcher or cursor. A source yield must name a worker evidence slot.
	Owner   core.EffectInstance
	Request core.Expr
	Bind    Local
	Next    BlockID
}

type CursorAdvance struct {
	Reply  core.Expr
	Close  bool
	Row    *core.RowArgument
	Cursor core.Expr
	Result *types.ADTInfo
	Access types.CursorAccess
	Bind   Local
	Next   BlockID
}

// Call transfers to another machine worker. The caller frame remains below
// the callee unless Tail is true. Resumption/return defines Bind at Next.
type Call struct {
	Capture         bool
	Row             *core.RowArgument
	Callee          string // non-empty for a statically known worker
	CalleeExpr      core.Expr
	Operation       *types.EffectOp
	Effect          core.EffectInstance
	TyArgs          []types.Type
	Args            []core.Expr
	EvidenceArgs    []core.EffectInstance
	Bind            Local
	Next            BlockID
	Tail            bool
	SynchronousArgs []int
}

type HandlerClause struct {
	Op        *types.EffectOp
	Worker    string
	Captures  []Local
	StateName string
	StateTy   types.Type
}

// Handle installs Machine evidence, calls the defunctionalized handled body,
// then optionally calls a return-transform worker under outer evidence.
// Abort handlers and state cells extend this term below without changing the
// ordinary Call protocol.
type Handle struct {
	Node           *core.Handle
	BodyWorker     string
	BodyCaptures   []Local
	Clauses        []HandlerClause
	ReturnWorker   string
	ReturnCaptures []Local
	Bind           Local
	Next           BlockID
	// Ordinary marks an activation the body reaches at Direct: its clauses
	// neither exit nor suspend, so they stay ordinary closures over the
	// handler's state rather than frame workers, and Clauses is empty.
	// OrdinaryCaptures are the locals those clause bodies read, which have to
	// be live where the activation is installed and are snapshotted there.
	Ordinary         bool
	OrdinaryCaptures []Local
	// Abort selects the non-resumptive exit-routing protocol. AbortNext is
	// reached after an abort clause returns; ordinary completion uses Next.
	Abort       bool
	AbortNext   BlockID
	AbortBind   Local
	State       *core.HandlerState
	StateResult Local
}

type StateResume struct {
	Value     core.Expr
	NextState core.Expr
	Bind      Local
	Next      BlockID
}

type PushCleanup struct {
	Acquire  core.Expr
	Resource Local
	Release  core.Expr
	Next     BlockID
}

// CursorOpen allocates a lazy producer and registers its synchronous closure.
// Yield is fresh evidence supplied only when constructing the producer frame.
type CursorOpen struct {
	Row      *core.RowArgument
	Scope    types.ScopeID
	Yield    core.EffectInstance
	Producer core.Expr
	Cursor   Local
	Next     BlockID
}

// CursorClose discharges the matching registered owner before continuing.
type CursorClose struct {
	Scope types.ScopeID
	Next  BlockID
}

type PopCleanup struct {
	Next BlockID
}

type Return struct {
	Value core.Expr
}

func (*Eval) isTerm()          {}
func (*Branch) isTerm()        {}
func (*SwitchCtor) isTerm()    {}
func (*SwitchLit) isTerm()     {}
func (*Suspend) isTerm()       {}
func (*CursorAdvance) isTerm() {}
func (*Call) isTerm()          {}
func (*Handle) isTerm()        {}
func (*StateResume) isTerm()   {}
func (*PushCleanup) isTerm()   {}
func (*CursorOpen) isTerm()    {}
func (*CursorClose) isTerm()   {}
func (*PopCleanup) isTerm()    {}
func (*Return) isTerm()        {}
