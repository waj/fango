package meta

import (
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

// Data is a portable, immutable stage value, never an evaluator heap object.
type Data struct {
	Kind      string
	Integer   int64
	Float     float64
	Text      string
	Boolean   bool
	Char      rune
	Code      *Code
	Reflected *TypeRepr
	Ctor      *types.CtorInfo
	Fields    []*Data
}
type Attributes struct{ Entries []types.AttributeInfo }
type Site struct{ Span source.Span }
type Failure struct {
	Site    *Site
	Message string
}

func (e *Failure) Error() string { return e.Message }
