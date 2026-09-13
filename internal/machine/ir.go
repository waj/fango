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
	Workers []Worker
}

type Local struct {
	Name string
	Ty   types.Type
}

type Worker struct {
	Name   string
	Owner  string
	Params []Local
	Result types.Type
	Entry  BlockID
	Blocks []Block

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
	Request core.Expr
	Bind    Local
	Next    BlockID
}

// Call transfers to another machine worker. The caller frame remains below
// the callee unless Tail is true. Resumption/return defines Bind at Next.
type Call struct {
	Callee       string
	Args         []core.Expr
	EvidenceArgs []core.EffectInstance
	Bind         Local
	Next         BlockID
	Tail         bool
}

type PushCleanup struct {
	Acquire  core.Expr
	Resource Local
	Release  core.Expr
	Next     BlockID
}

type PopCleanup struct {
	Next BlockID
}

type Return struct {
	Value core.Expr
}

func (*Eval) isTerm()        {}
func (*Branch) isTerm()      {}
func (*SwitchCtor) isTerm()  {}
func (*SwitchLit) isTerm()   {}
func (*Suspend) isTerm()     {}
func (*Call) isTerm()        {}
func (*PushCleanup) isTerm() {}
func (*PopCleanup) isTerm()  {}
func (*Return) isTerm()      {}
