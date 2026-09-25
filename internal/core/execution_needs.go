package core

import (
	"encoding/json"
	"fmt"

	"github.com/waj/fango/internal/types"
)

// CollectExecutionNeeds interprets source capture contracts once per root to
// collect work-budget and coroutine-control constraints before generalization.
// Unknown rows remain intact, and abstract callback destinations stay outward
// until a call supplies a concrete owner.
func CollectExecutionNeeds(p *Prog, context []Def, b *types.Builtins) ([]WorkNeed, []ControlNeed) {
	a := newCaptureAnalyzer(p, b)
	for i := range context {
		if a.defs[context[i].Name] == nil {
			a.defs[context[i].Name] = &context[i]
		}
	}
	var work []WorkNeed
	var control []ControlNeed
	seenWork, seenControl := map[string]bool{}, map[string]bool{}
	for _, d := range p.Defs {
		f := &flowChecker{shape: a, defs: a.defs, objects: []*flowObject{nil}, objectIDs: map[string]int{}, owners: []*flowOwner{nil}, ownerIDs: map[string]int{}, contexts: map[string]*flowContext{}, errors: map[string]error{}, root: d.Name, active: map[int]int{}}
		f.collectWorkNeed = func(n WorkNeed) {
			data, _ := json.Marshal([]types.Type{n.Budget, n.Need})
			key := n.In + "/" + n.Owner + "/" + string(data)
			if !seenWork[key] {
				seenWork[key] = true
				work = append(work, n)
			}
		}
		f.collectControlNeed = func(n ControlNeed) {
			data, _ := json.Marshal(n.Row)
			key := n.Operation + string(data)
			if !seenControl[key] {
				seenControl[key] = true
				control = append(control, n)
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
	return work, control
}
