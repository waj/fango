package core

import (
	"encoding/json"
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

// CollectControlNeeds subtracts concrete owners before projecting suspension
// and drive obligations to source labels. Abstract callback destinations stay
// outward; they can never be equated with a locally allocated owner.
func CollectControlNeeds(p *Prog, context []Def, b *types.Builtins) []ControlNeed {
	a := newCaptureAnalyzer(p, b)
	for i := range context {
		if a.defs[context[i].Name] == nil {
			a.defs[context[i].Name] = &context[i]
		}
	}
	var out []ControlNeed
	seen := map[string]bool{}
	for _, d := range p.Defs {
		f := &flowChecker{shape: a, defs: a.defs, objects: []*flowObject{nil}, objectIDs: map[string]int{}, owners: []*flowOwner{nil}, ownerIDs: map[string]int{}, contexts: map[string]*flowContext{}, errors: map[string]error{}, root: d.Name, active: map[int]int{}}
		f.collectControlNeed = func(n ControlNeed) {
			data, _ := json.Marshal(n.Row)
			key := n.Operation + string(data)
			if !seen[key] {
				seen[key] = true
				out = append(out, n)
			}
		}
		env := emptyFlowEnv()
		env.rows[0] = flowRow{unknown: true}
		args := make([]flowValue, len(d.Params))
		cur := d.Type
		for i := range args {
			args[i] = flowValue{unknown: true}
			if fn, ok := cur.(*types.TFun); ok {
				args[i] = f.symbolicControl(fn.Arg, fmt.Sprintf("arg%d", i), map[int]bool{})
				cur = fn.Ret
			}
		}
		for {
			f.generation++
			f.changed = false
			f.callDef(d.Name, args, nil, env, rootFlowSite, nil)
			if !f.changed {
				break
			}
		}
	}
	return out
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
