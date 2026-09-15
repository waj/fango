package types

import "github.com/waj/fango/internal/source"

// CaptureContract is a finite, symbolic capture-flow graph. Unlike a result
// capture set it retains higher-order calls and scope obligations, so a caller
// can substitute the actual callback and handler interpretations. It contains
// no scalar computation and is erased before execution.
type CaptureContract struct {
	Params     []string
	Effects    []int
	TypeParams []int
	Body       *CaptureFlow
}

// CaptureFlow describes value flow and access to a lifetime owner. Node IDs
// are local to the defining contract and stable across independent inference.
type CaptureFlow struct {
	Origin   source.Span
	ID       int
	Kind     string
	Name     string
	Type     Type
	TypeArgs []Type
	Names    []string
	Effects  []int
	Scope    ScopeID
	Scoped   bool
	Borrow   bool
	Retain   bool
	Index    int
	Rec      bool
	Children []*CaptureFlow
	Clauses  []CaptureClause
}

type CaptureClause struct {
	Index int
	Names []string
	Types []Type
	Body  *CaptureFlow
}
