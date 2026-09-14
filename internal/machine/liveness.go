package machine

import (
	"sort"

	"github.com/waj/fango/internal/core"
)

func analyze(w *Worker) {
	known := map[string]bool{}
	localByName := map[string]Local{}
	for _, local := range w.Locals {
		known[local.Name] = true
		localByName[local.Name] = local
	}
	liveIn := make([]map[string]bool, len(w.Blocks))
	liveOut := make([]map[string]bool, len(w.Blocks))
	for i := range w.Blocks {
		liveIn[i] = map[string]bool{}
		liveOut[i] = map[string]bool{}
	}
	for changed := true; changed; {
		changed = false
		for i := len(w.Blocks) - 1; i >= 0; i-- {
			out := map[string]bool{}
			for _, succ := range successors(w.Blocks[i].Term) {
				if int(succ) >= 0 && int(succ) < len(liveIn) {
					unionInto(out, liveIn[succ])
				}
			}
			in := transfer(w.Blocks[i].Term, liveIn, out)
			unionInto(in, termUses(w.Blocks[i].Term, known))
			if !equalSet(out, liveOut[i]) || !equalSet(in, liveIn[i]) {
				liveOut[i], liveIn[i] = out, in
				changed = true
			}
		}
	}

	frame := map[string]bool{}
	for i := range w.Blocks {
		w.Blocks[i].LiveIn = sortedSet(liveIn[i])
		w.Blocks[i].LiveOut = sortedSet(liveOut[i])
		switch term := w.Blocks[i].Term.(type) {
		case *Suspend:
			across := cloneSet(liveOut[i])
			delete(across, term.Bind.Name)
			unionInto(frame, across)
		case *Call:
			if !term.Tail {
				across := cloneSet(liveOut[i])
				delete(across, term.Bind.Name)
				unionInto(frame, across)
			}
		case *Handle:
			across := cloneSet(liveOut[i])
			delete(across, term.Bind.Name)
			delete(across, term.AbortBind.Name)
			delete(across, term.StateResult.Name)
			unionInto(frame, across)
		}
	}
	for _, name := range sortedSet(frame) {
		w.Frame = append(w.Frame, localByName[name])
	}
}

func termUses(term Term, known map[string]bool) map[string]bool {
	out := map[string]bool{}
	add := func(e core.Expr) {
		for name := range freeLocalRefs(e) {
			if known[name] {
				out[name] = true
			}
		}
	}
	switch term := term.(type) {
	case *Eval:
		add(term.Value)
	case *Branch:
		add(term.Cond)
	case *SwitchCtor:
		if known[term.Scrut] {
			out[term.Scrut] = true
		}
	case *SwitchLit:
		if known[term.Scrut] {
			out[term.Scrut] = true
		}
		for _, c := range term.Cases {
			add(c.Lit)
		}
	case *Suspend:
		add(term.Request)
	case *Call:
		add(term.CalleeExpr)
		for _, arg := range term.Args {
			add(arg)
		}
	case *Handle:
		if term.State != nil {
			add(term.State.Initial)
		}
		for _, capture := range term.BodyCaptures {
			out[capture.Name] = true
		}
		for _, clause := range term.Clauses {
			for _, capture := range clause.Captures {
				out[capture.Name] = true
			}
		}
		for _, capture := range term.ReturnCaptures {
			out[capture.Name] = true
		}
	case *StateResume:
		add(term.Value)
		add(term.NextState)
	case *PushCleanup:
		add(term.Acquire)
		// Release is captured when the cleanup is pushed. Its free locals are
		// uses at registration time, not frame liveness across the body.
		add(term.Release)
	case *Return:
		add(term.Value)
	}
	return out
}

func transfer(term Term, liveIn []map[string]bool, ordinaryOut map[string]bool) map[string]bool {
	switch term := term.(type) {
	case *SwitchCtor:
		in := map[string]bool{}
		for _, c := range term.Cases {
			edge := cloneSet(liveIn[c.Next])
			for _, bind := range c.Binds {
				delete(edge, bind.Name)
			}
			unionInto(in, edge)
		}
		if term.Default != nil {
			unionInto(in, liveIn[*term.Default])
		}
		return in
	case *Handle:
		in := cloneSet(ordinaryOut)
		delete(in, term.Bind.Name)
		delete(in, term.AbortBind.Name)
		delete(in, term.StateResult.Name)
		return in
	default:
		in := cloneSet(ordinaryOut)
		if def := defined(term); def != "" {
			delete(in, def)
		}
		return in
	}
}

// freeLocalRefs relies on Core's no-shadowing invariant: binder names are
// unique throughout an expression, so collecting then subtracting every
// nested binder is equivalent to a scope-sensitive free-variable walk.
func freeLocalRefs(e core.Expr) map[string]bool {
	refs := map[string]bool{}
	bound := map[string]bool{}
	var treeBinders func(core.Tree)
	treeBinders = func(tree core.Tree) {
		switch tree := tree.(type) {
		case *core.Guard:
			treeBinders(tree.Then)
			treeBinders(tree.Else)
		case *core.SwitchCtor:
			for _, c := range tree.Cases {
				for _, name := range c.Binds {
					if name != "" {
						bound[name] = true
					}
				}
				treeBinders(c.Tree)
			}
			if tree.Default != nil {
				treeBinders(tree.Default)
			}
		case *core.SwitchLit:
			for _, c := range tree.Cases {
				treeBinders(c.Tree)
			}
			treeBinders(tree.Default)
		}
	}
	core.Rewrite(e, identityType, func(x core.Expr) core.Expr {
		switch x := x.(type) {
		case *core.VarRef:
			if x.Local {
				refs[x.Name] = true
			}
		case *core.Let:
			if x.Name != "_" {
				bound[x.Name] = true
			}
		case *core.Lambda:
			if x.Param != "_" {
				bound[x.Param] = true
			}
		case *core.Case:
			bound[x.Bind] = true
			treeBinders(x.Tree)
		case *core.Handle:
			if x.State != nil {
				bound[x.State.Name] = true
			}
			for _, clause := range x.Clauses {
				for _, name := range clause.Params {
					if name != "_" && name != "()" {
						bound[name] = true
					}
				}
			}
			if x.Return != nil && x.Return.Param != "_" && x.Return.Param != "()" {
				bound[x.Return.Param] = true
			}
		case *core.Bracket:
			bound[x.Resource] = true
		}
		return x
	})
	for name := range bound {
		delete(refs, name)
	}
	return refs
}

func defined(term Term) string {
	switch term := term.(type) {
	case *Eval:
		return term.Bind.Name
	case *Suspend:
		return term.Bind.Name
	case *Call:
		return term.Bind.Name
	case *Handle:
		return term.Bind.Name
	case *StateResume:
		return term.Bind.Name
	case *PushCleanup:
		return term.Resource.Name
	default:
		return ""
	}
}

func successors(term Term) []BlockID {
	switch term := term.(type) {
	case *Eval:
		return []BlockID{term.Next}
	case *Branch:
		return []BlockID{term.Then, term.Else}
	case *SwitchCtor:
		out := make([]BlockID, 0, len(term.Cases)+1)
		for _, c := range term.Cases {
			out = append(out, c.Next)
		}
		if term.Default != nil {
			out = append(out, *term.Default)
		}
		return out
	case *SwitchLit:
		out := make([]BlockID, 0, len(term.Cases)+1)
		for _, c := range term.Cases {
			out = append(out, c.Next)
		}
		return append(out, term.Default)
	case *Suspend:
		return []BlockID{term.Next}
	case *Call:
		return []BlockID{term.Next}
	case *Handle:
		if term.Abort {
			return []BlockID{term.Next, term.AbortNext}
		}
		return []BlockID{term.Next}
	case *StateResume:
		return []BlockID{term.Next}
	case *PushCleanup:
		return []BlockID{term.Next}
	case *PopCleanup:
		return []BlockID{term.Next}
	default:
		return nil
	}
}

func cloneSet(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	unionInto(out, in)
	return out
}

func unionInto(dst, src map[string]bool) {
	for value := range src {
		dst[value] = true
	}
}

func equalSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for value := range a {
		if !b[value] {
			return false
		}
	}
	return true
}

func sortedSet(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
