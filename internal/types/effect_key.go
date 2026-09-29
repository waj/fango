package types

import (
	"strconv"
	"strings"
)

// EffectKey is the identity of one applied nominal effect. It is internal
// compiler data; source spelling and diagnostic variable names are omitted.
type EffectKey string

func EffectLabelKey(label EffLabel) EffectKey { return AppliedEffectKey(label.Unique, label.Args) }

func AppliedEffectKey(unique int, args []Type) EffectKey {
	var b strings.Builder
	b.WriteString(strconv.Itoa(unique))
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
		b.WriteString(strconv.Itoa(t.ID))
		b.WriteByte('k')
		b.WriteString(strconv.Itoa(int(t.Kind)))
		if t.Rigid {
			b.WriteByte('r')
		}
	case *TCon:
		b.WriteByte('c')
		b.WriteString(strconv.Itoa(t.Unique))
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
