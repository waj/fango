package types

import (
	"strconv"
	"strings"
)

// EffectKey is the identity of one applied nominal effect. It is internal
// compiler data; source spelling and diagnostic variable names are omitted.
// Rows and evidence are ordered by comparing keys as strings, so numbers are
// written length-prefixed: string order then agrees with numeric order, which
// is the order installing a cached module preserves when it renumbers
// identities. Plain decimal would let renumbering across a digit boundary
// reorder a cached callee's evidence against a freshly compiled caller.
type EffectKey string

func writeNumber(b *strings.Builder, n int) {
	digits := strconv.Itoa(n)
	b.WriteByte(byte('a' + len(digits)))
	b.WriteString(digits)
}

func EffectLabelKey(label EffLabel) EffectKey { return AppliedEffectKey(label.Unique, label.Args) }

func AppliedEffectKey(unique int, args []Type) EffectKey {
	var b strings.Builder
	writeNumber(&b, unique)
	for _, arg := range args {
		b.WriteByte(':')
		writeTypeKey(&b, arg)
	}
	return EffectKey(b.String())
}

func writeTypeKey(b *strings.Builder, t Type) {
	switch t := t.(type) {
	case *TVar:
		b.WriteByte('v')
		writeNumber(b, t.ID)
		b.WriteByte('k')
		b.WriteString(strconv.Itoa(int(t.Kind)))
		if t.Rigid {
			b.WriteByte('r')
		}
	case *TCon:
		b.WriteByte('c')
		writeNumber(b, t.Unique)
		b.WriteByte('[')
		for _, arg := range t.Args {
			writeTypeKey(b, arg)
			b.WriteByte(';')
		}
		b.WriteByte(']')
	case *TFun:
		b.WriteByte('f')
		writeTypeKey(b, t.Arg)
		writeTypeKey(b, t.Eff)
		writeTypeKey(b, t.Ret)
	case Row:
		b.WriteByte('{')
		for _, label := range SortedRow(t).Labels {
			b.WriteString(string(EffectLabelKey(label)))
			b.WriteByte(';')
		}
		b.WriteByte('|')
		if t.Tail != nil {
			writeTypeKey(b, t.Tail)
		}
		b.WriteByte('}')
	}
}
