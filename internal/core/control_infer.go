package core

import (
	"fmt"
	"github.com/waj/fango/internal/source"
	"github.com/waj/fango/internal/types"
)

type ControlNeed struct {
	Row       types.Type
	Operation string
	Span      source.Span
	In        string
}

func executionRow(t types.Type, arity int) types.Type {
	var row types.Type
	for range arity {
		fn, ok := t.(*types.TFun)
		if !ok {
			return nil
		}
		row = fn.Eff
		t = fn.Ret
	}
	return row
}

func (f *flowChecker) symbolicControl(t types.Type, site string, seen map[int]bool) flowValue {
	if fn, ok := t.(*types.TFun); ok {
		for _, l := range fn.Eff.Labels {
			if l.Name == types.CoroutineSuspensionName || l.Name == types.CoroutineDriveName {
				id := f.alloc(site, flowObject{kind: "abstract-control", code: &types.CaptureFlow{Type: fn}})
				return flowValue{refs: []int{id}}
			}
		}
	}
	if con, ok := t.(*types.TCon); ok {
		if con.Name == types.CoroutineTypeName {
			owner := f.owner(&types.CaptureFlow{ID: len(f.owners), Kind: "coroutine", Scoped: true}, emptyFlowEnv(), "", nil)
			id := f.alloc(site, flowObject{kind: "coroutine", owner: owner, fields: []flowValue{{unknown: true}}})
			return flowValue{refs: []int{id}, caps: []int{owner}}
		}
		if seen[con.Unique] {
			return flowValue{unknown: true}
		}
		seen[con.Unique] = true
		defer delete(seen, con.Unique)
		if adt := f.shape.adts[con.Unique]; adt != nil {
			var out flowValue
			for _, ctor := range adt.Ctors {
				fields := make([]flowValue, len(ctor.Fields))
				for i, field := range ctor.Fields {
					fields[i] = f.symbolicControl(types.SubstRigid(field, adt.ParamSubst(con.Args)), fmt.Sprintf("%s/%d/%d", site, ctor.Index, i), seen)
				}
				id := f.alloc(fmt.Sprintf("%s/%d", site, ctor.Index), flowObject{kind: "ctor", ctor: ctor.Index, fields: fields})
				out.refs = append(out.refs, id)
			}
			return out
		}
	}
	return flowValue{unknown: true}
}
