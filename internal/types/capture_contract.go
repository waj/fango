package types

import "github.com/waj/fango/internal/source"

// CaptureContract is a finite, symbolic capture-flow graph. Unlike a result
// capture set it retains higher-order calls and scope obligations, so a caller
// can substitute the actual callback and handler interpretations. It contains
// no scalar computation and is erased before execution.
type CaptureContract struct {
	SourceType Type
	Params     []string
	Effects    []int
	RowParam   CaptureVar
	RowEffects []int
	TypeParams []int
	Body       *CaptureFlow
}

// CaptureFlow describes value flow and access to a lifetime owner. Node IDs
// are local to the defining contract and stable across independent inference.
type CaptureFlow struct {
	Service       bool
	NativeStorage NativeStorage
	SourceType    Type
	Origin        source.Span
	ID            int
	Kind          string
	Name          string
	Type          Type
	TypeArgs      []Type
	Names         []string
	Effects       []int
	RowParam      CaptureVar
	Deferred      []int
	Row           *CaptureRow
	Scope         ScopeID
	Scoped        bool
	Borrow        bool
	Retain        bool
	Access        CursorAccess
	Index         int
	Rec           bool
	Children      []*CaptureFlow
	Clauses       []CaptureClause
}

type CaptureRow struct {
	From    CaptureVar
	Effects []int
}

type CaptureClause struct {
	Suppressed string
	Index      int
	Names      []string
	Types      []Type
	Body       *CaptureFlow
}
