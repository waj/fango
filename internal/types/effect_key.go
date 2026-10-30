package types

import (
	"strconv"
	"strings"
)

// EffectKey is the identity of one applied nominal effect. It is internal
// compiler data; source spelling and diagnostic variable names are omitted.
// It names an application and carries no order: see EffectOrder.
type EffectKey string

// EffectOrder ranks applied effects within rows and evidence slices, whose
// positions are ABI between a worker and its callers. It is led by canonical
// names because allocation depends on the session: installing a module maps its
// foreign effects onto whatever order that session's imports allocated, and a
// reload retains older identities beside fresh ones, so an order over identities
// would differ between an object and its callers. Identities only separate
// generations of one name, and numbers are length-prefixed so that string order
// agrees with numeric order.
type EffectOrder string

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
		writeTypeKey(&b, arg, false)
	}
	return EffectKey(b.String())
}

func EffectLabelOrder(label EffLabel) EffectOrder {
	return AppliedEffectOrder(label.Name, label.Unique, label.Args)
}

func AppliedEffectOrder(name string, unique int, args []Type) EffectOrder {
	var b strings.Builder
	b.WriteString(name)
	b.WriteByte(0)
	for _, arg := range args {
		writeTypeKey(&b, arg, true)
		b.WriteByte(':')
	}
	writeNumber(&b, unique)
	return EffectOrder(b.String())
}

// writeTypeKey writes a type's identity, or with ordered its rank, which names
// each nominal type before the identity that separates its generations.
func writeTypeKey(b *strings.Builder, t Type, ordered bool) {
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
		if ordered {
			b.WriteString(t.Name)
			b.WriteByte(0)
		}
		writeNumber(b, t.Unique)
		b.WriteByte('[')
		for _, arg := range t.Args {
			writeTypeKey(b, arg, ordered)
			b.WriteByte(';')
		}
		b.WriteByte(']')
	case *TFun:
		b.WriteByte('f')
		writeTypeKey(b, t.Arg, ordered)
		writeTypeKey(b, t.Eff, ordered)
		writeTypeKey(b, t.Ret, ordered)
	case Row:
		b.WriteByte('{')
		for _, label := range SortedRow(t).Labels {
			if ordered {
				b.WriteString(string(EffectLabelOrder(label)))
			} else {
				b.WriteString(string(EffectLabelKey(label)))
			}
			b.WriteByte(';')
		}
		b.WriteByte('|')
		if t.Tail != nil {
			writeTypeKey(b, t.Tail, ordered)
		}
		b.WriteByte('}')
	}
}
