package core

// Inspect visits the original Core nodes in depth-first order without
// rebuilding them. Use Rewrite for transforms; Inspect exists for analyses
// whose result is keyed by expression pointer identity, such as Machine
// closure registration.
func Inspect(e Expr, visit func(Expr)) {
	InspectPruned(e, func(e Expr) bool { visit(e); return true })
}

// InspectPruned visits an expression before its children, stopping at a node
// whose visitor returns false. Lexical lowering uses this at new worker roots.
func InspectPruned(e Expr, visit func(Expr) bool) {
	if e == nil {
		return
	}
	if !visit(e) {
		return
	}
	walk := func(child Expr) { InspectPruned(child, visit) }
	switch e := e.(type) {
	case *Neg:
		walk(e.Operand)
	case *Completion:
		walk(e.Value)
	case *NativeCall:
		for _, arg := range e.Args {
			walk(arg)
		}
	case *Work:
		for _, arg := range e.Args {
			walk(arg)
		}
	case *FailureInspect:
		for _, arg := range e.Args {
			walk(arg)
		}
	case *Quote:
		for _, hole := range e.Holes {
			walk(hole)
		}
	case *If:
		walk(e.Cond)
		walk(e.Then)
		walk(e.Else)
	case *Let:
		walk(e.Rhs)
		walk(e.Body)
	case *Lambda:
		walk(e.Body)
	case *Seq:
		walk(e.First)
		walk(e.Then)
	case *ResumeTail:
		walk(e.Value)
		walk(e.NextState)
	case *Perform:
		for _, arg := range e.Args {
			walk(arg)
		}
	case *ControlExit:
		for _, payload := range e.Payload {
			walk(payload)
		}
	case *Suspend:
		walk(e.Request)
	case *CoroutineAdvance:
		walk(e.Cursor)
		walk(e.Reply)
	case *CoroutineScope:
		walk(e.Producer)
		walk(e.Consumer)

	case *Bracket:
		walk(e.Acquire)
		walk(e.Release)
		walk(e.Body)
	case *Handle:
		walk(e.Body)
		if e.State != nil {
			walk(e.State.Initial)
		}
		for _, clause := range e.Clauses {
			walk(clause.Body)
		}
		if e.Return != nil {
			walk(e.Return.Body)
		}
	case *App:
		walk(e.Callee)
		for _, arg := range e.Args {
			walk(arg)
		}
	case *Case:
		walk(e.Scrut)
		inspectTree(e.Tree, visit)
	case *IntLit, *FloatLit, *StringLit, *CharLit, *BoolLit, *UnitLit,
		*VarRef, *TypeOf:
		// Leaves.
	}
}

func inspectTree(tree Tree, visit func(Expr) bool) {
	switch tree := tree.(type) {
	case nil, *Unreachable:
	case *Leaf:
		InspectPruned(tree.Body, visit)
	case *Guard:
		InspectPruned(tree.Cond, visit)
		inspectTree(tree.Then, visit)
		inspectTree(tree.Else, visit)
	case *SwitchCtor:
		for _, c := range tree.Cases {
			inspectTree(c.Tree, visit)
		}
		inspectTree(tree.Default, visit)
	case *SwitchLit:
		for _, c := range tree.Cases {
			InspectPruned(c.Lit, visit)
			inspectTree(c.Tree, visit)
		}
		inspectTree(tree.Default, visit)
	}
}
