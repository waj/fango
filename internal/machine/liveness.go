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
			if h, ok := w.Blocks[i].Term.(*Handle); ok {
				for _, clause := range h.Clauses {
					for _, capture := range clause.Captures {
						out[capture.Name] = true
					}
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
		case *CursorAdvance:
			across := cloneSet(liveOut[i])
			delete(across, term.Bind.Name)
			unionInto(frame, across)
		case *Call:
			if !term.Tail {
				across := cloneSet(liveOut[i])
				delete(across, term.Bind.Name)
				unionInto(frame, across)
			}
		case *PopCleanup, *CursorClose:
			unionInto(frame, liveOut[i])
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
	case *CursorAdvance:
		add(term.Cursor)
		add(term.Reply)
	case *CursorOpen:
		add(term.Producer)
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
		for _, capture := range term.OrdinaryCaptures {
			out[capture.Name] = true
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

// freeLocalRefs walks binders by lexical scope. Generated equation parameters
// can reuse a spelling in a nested lambda; subtracting every binder in the
// entire expression would hide a reference to the enclosing parameter.
func freeLocalRefs(e core.Expr) map[string]bool {
	refs := map[string]bool{}
	bound := map[string]int{}
	with := func(names []string, body func()) {
		for _, name := range names {
			if name != "" && name != "_" && name != "()" {
				bound[name]++
			}
		}
		body()
		for _, name := range names {
			if name != "" && name != "_" && name != "()" {
				bound[name]--
			}
		}
	}
	var walk func(core.Expr)
	var walkTree func(core.Tree)
	walkTree = func(tree core.Tree) {
		switch tree := tree.(type) {
		case nil, *core.Unreachable:
		case *core.Leaf:
			walk(tree.Body)
		case *core.Guard:
			walk(tree.Cond)
			walkTree(tree.Then)
			walkTree(tree.Else)
		case *core.SwitchCtor:
			for _, c := range tree.Cases {
				with(c.Binds, func() { walkTree(c.Tree) })
			}
			walkTree(tree.Default)
		case *core.SwitchLit:
			for _, c := range tree.Cases {
				walk(c.Lit)
				walkTree(c.Tree)
			}
			walkTree(tree.Default)
		}
	}
	walk = func(x core.Expr) {
		if x == nil {
			return
		}
		switch x := x.(type) {
		case *core.VarRef:
			if x.Local && bound[x.Name] == 0 {
				refs[x.Name] = true
			}
		case *core.Let:
			if x.Rec {
				with([]string{x.Name}, func() { walk(x.Rhs); walk(x.Body) })
			} else {
				walk(x.Rhs)
				with([]string{x.Name}, func() { walk(x.Body) })
			}
		case *core.Lambda:
			with([]string{x.Param}, func() { walk(x.Body) })
		case *core.Case:
			walk(x.Scrut)
			with([]string{x.Bind}, func() { walkTree(x.Tree) })
		case *core.Handle:
			state := ""
			if x.State != nil {
				walk(x.State.Initial)
				state = x.State.Name
			}
			with([]string{state}, func() {
				walk(x.Body)
				for _, clause := range x.Clauses {
					with(clause.Params, func() { walk(clause.Body) })
				}
				if x.Return != nil {
					with([]string{x.Return.Param}, func() { walk(x.Return.Body) })
				}
			})
		case *core.Bracket:
			walk(x.Acquire)
			with([]string{x.Resource}, func() { walk(x.Body); walk(x.Release) })
		case *core.If:
			walk(x.Cond)
			walk(x.Then)
			walk(x.Else)
		case *core.Seq:
			walk(x.First)
			walk(x.Then)
		case *core.App:
			walk(x.Callee)
			for _, arg := range x.Args {
				walk(arg)
			}
		case *core.Perform:
			for _, arg := range x.Args {
				walk(arg)
			}
		case *core.NativeCall:
			for _, arg := range x.Args {
				walk(arg)
			}
		case *core.Work:
			for _, arg := range x.Args {
				walk(arg)
			}
		case *core.FailureInspect:
			for _, arg := range x.Args {
				walk(arg)
			}
		case *core.ControlExit:
			for _, arg := range x.Payload {
				walk(arg)
			}
		case *core.Quote:
			for _, hole := range x.Holes {
				walk(hole)
			}
		case *core.Completion:
			walk(x.Value)
		case *core.Neg:
			walk(x.Operand)
		case *core.Suspend:
			walk(x.Request)
		case *core.CoroutineAdvance:
			walk(x.Cursor)
			walk(x.Reply)
		case *core.CoroutineScope:
			walk(x.Producer)
			walk(x.Consumer)
		case *core.ResumeTail:
			walk(x.Value)
			walk(x.NextState)
		}
	}
	walk(e)
	return refs
}

func defined(term Term) string {
	switch term := term.(type) {
	case *Eval:
		return term.Bind.Name
	case *Suspend:
		return term.Bind.Name
	case *CursorAdvance:
		return term.Bind.Name
	case *CursorOpen:
		return term.Cursor.Name
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
	case *CursorOpen:
		return []BlockID{term.Next}
	case *CursorClose:
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
	case *CursorAdvance:
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
