package core

import (
	"github.com/waj/fango/internal/meta"
	"github.com/waj/fango/internal/types"
)

func (l *linter) closedAttributeType(t types.Type) bool {
	con, ok := t.(*types.TCon)
	if !ok {
		return false
	}
	if adt := l.adts[con.Unique]; adt != nil && len(con.Args) != len(adt.Params) {
		return false
	}
	for _, arg := range con.Args {
		if !l.closedAttributeType(arg) {
			return false
		}
	}
	return true
}

// Frozen metadata is a typed stage constant, not a way to retag arbitrary values.
func (l *linter) attributeData(t types.Type, d *meta.Data, where string) {
	con, ok := t.(*types.TCon)
	if !ok || d == nil {
		l.errorf("%s: malformed attribute data", where)
		return
	}
	scalar := map[string]*types.TCon{"int": l.b.Int, "float": l.b.Float, "string": l.b.String, "bool": l.b.Bool, "char": l.b.Char, "unit": l.b.Unit}
	if want := scalar[d.Kind]; want != nil {
		if !types.Equal(con, want) {
			l.errorf("%s: attribute scalar differs from stored type", where)
		}
		return
	}
	switch d.Kind {
	case "code":
		l.stageType(t, "Meta.Code", where)
		if d.Code == nil || d.Code.Template < 0 && d.Code.Direct == nil {
			l.errorf("%s: invalid stored attribute code", where)
		}
	case "type":
		l.stageType(t, "Meta.TypeRepr", where)
		if d.Reflected == nil || d.Reflected.Type == nil {
			l.errorf("%s: invalid stored attribute reflection", where)
		}
	case "list":
		adt := l.adts[con.Unique]
		if adt == nil || adt.Repr != types.ReprList || len(con.Args) != 1 {
			l.errorf("%s: invalid attribute list type", where)
			return
		}
		for _, child := range d.Fields {
			l.attributeData(con.Args[0], child, where)
		}
	case "ctor":
		adt := l.adts[con.Unique]
		if adt == nil || d.Ctor == nil || d.Ctor.Result == nil || d.Ctor.Result.Unique != con.Unique ||
			d.Ctor.Index < 0 || d.Ctor.Index >= len(adt.Ctors) || adt.Ctors[d.Ctor.Index] != d.Ctor || len(con.Args) != len(adt.Params) {
			l.errorf("%s: attribute constructor differs from stored type", where)
			return
		}
		fields := adt.InstFields(d.Ctor, con.Args)
		if len(fields) != len(d.Fields) {
			l.errorf("%s: invalid attribute constructor fields", where)
			return
		}
		for i, child := range d.Fields {
			l.attributeData(fields[i], child, where)
		}
	default:
		l.errorf("%s: invalid attribute data kind %q", where, d.Kind)
	}
}
