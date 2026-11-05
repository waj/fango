package core

import "github.com/waj/fango/internal/types"

// CallSpine returns an indirect application's stages in execution order.
func CallSpine(app *App) (Expr, []*App) {
	var reverse []*App
	var head Expr = app
	for {
		stage, ok := head.(*App)
		if !ok || stage.CalleeKind != Value {
			break
		}
		reverse = append(reverse, stage)
		head = stage.Callee
	}
	stages := make([]*App, len(reverse))
	for i, stage := range reverse {
		stages[len(reverse)-1-i] = stage
	}
	return head, stages
}

// SaturatedCallback checks the execution boundary as well as the type. Pure
// arrows can still compute or diverge: later arguments must already be atoms
// before an unknown intermediate application may be moved into an adapter.
func SaturatedCallback(stages []*App) bool {
	for i, stage := range stages {
		if len(stage.Args) != 1 {
			return false
		}
		if i > 0 && !callbackAtom(stage.Args[0]) {
			return false
		}
		if i+1 < len(stages) {
			c := stage.Control
			if c.Transport != types.Direct || c.Polymorphic || stage.Row != nil || len(stage.EvidenceArgs) != 0 {
				return false
			}
		}
	}
	return len(stages) > 0
}

func callbackAtom(e Expr) bool {
	switch e.(type) {
	case *IntLit, *FloatLit, *RegexLit, *StringLit, *CharLit, *BoolLit, *UnitLit, *VarRef:
		return true
	}
	return false
}

func callbackArity(t types.Type) int {
	arity := 0
	for {
		fn, ok := t.(*types.TFun)
		if !ok {
			return arity
		}
		arity++
		c := types.FunctionControl(fn)
		if c.Transport != types.Direct || c.Polymorphic || len(fn.Eff.Labels) > 0 || types.FunctionOpenRow(fn) {
			return arity
		}
		t = fn.Ret
	}
}

// Solve a descending fixed point. Initially each function parameter permits
// its full pure-prefix arity. An escaping/mixed use removes the optimization;
// forwarding propagates that decision through recursive groups. Dependency
// bodies are never consulted.
func summarizeCallbacks(defs, context []Def) {
	all := map[string]*Def{}
	for i := range context {
		all[context[i].Name] = &context[i]
	}
	for i := range defs {
		d := &defs[i]
		all[d.Name] = d
		args, _ := PeelFun(d.Type, len(d.Params))
		d.ABI.Callbacks = make([]CallbackABI, len(d.Params))
		for j, arg := range args {
			d.ABI.Callbacks[j] = CallbackABI{Arity: callbackArity(arg), DirectUses: 1, ExitUses: 2, ForwardsRow: true}
		}
	}
	for changed := true; changed; {
		changed = false
		for i := range defs {
			d := &defs[i]
			for j, name := range d.Params {
				old := d.ABI.Callbacks[j]
				if old.Arity == 0 {
					continue
				}
				next := callbackUses(d.Body, name, old.Arity, d.RowParam, all)
				if next != old {
					d.ABI.Callbacks[j] = next
					changed = true
				}
			}
		}
	}
}

func callbackUses(body Expr, name string, arity int, row types.CaptureVar, defs map[string]*Def) CallbackABI {
	out := CallbackABI{Arity: arity, ForwardsRow: true}
	escapes := false
	add := func(c CallbackABI) {
		if c.Arity != arity {
			escapes = true
		}
		out.ForwardsRow = out.ForwardsRow && c.ForwardsRow
		for mode := range 2 {
			out.AddUses(types.Transport(mode), c.Uses(types.Transport(mode)))
		}
	}
	// context maps each worker transport to the control context an
	// expression is emitted in. Code generation evaluates an abort handler's
	// subject in Exit and a cleanup scope's children in the scope's own
	// transport, so uses there resolve against that context instead.
	var visit func(Expr, [2]types.Transport)
	visit = func(e Expr, context [2]types.Transport) {
		InspectPruned(e, func(e Expr) bool {
			switch e := e.(type) {
			case *Let:
				if ref, ok := e.Rhs.(*VarRef); ok && ref.Local && ref.Name == name && !e.Rec {
					c := callbackUses(e.Body, e.Name, arity, row, defs)
					add(c.inContext(context))
					visit(e.Body, context)
					return false
				}
			case *Bracket:
				inner := [2]types.Transport{e.Control.Resolve(context[0]), e.Control.Resolve(context[1])}
				visit(e.Acquire, inner)
				visit(e.Release, inner)
				visit(e.Body, inner)
				return false
			case *Handle:
				// Clauses become operation functions, potentially invoked in
				// another transport context or task. They are escaping uses.
				for _, clause := range e.Clauses {
					if Mentions(clause.Body, name) {
						escapes = true
					}
				}
				if e.Return != nil && Mentions(e.Return.Body, name) {
					escapes = true
				}
				if e.State != nil {
					visit(e.State.Initial, context)
				}
				if e.HandlesAbort() {
					visit(e.Body, [2]types.Transport{types.Exit, types.Exit})
				} else {
					visit(e.Body, context)
				}
				return false
			case *Lambda:
				if Mentions(e.Body, name) {
					escapes = true
				}
				return false
			case *VarRef:
				if e.Local && e.Name == name {
					escapes = true
				}
			case *App:
				if e.CalleeKind == Value {
					head, stages := CallSpine(e)
					if ref, ok := head.(*VarRef); ok && ref.Local && ref.Name == name {
						if len(stages) != arity || !SaturatedCallback(stages) {
							escapes = true
						}
						last := stages[len(stages)-1]
						c := CallbackABI{Arity: len(stages), ForwardsRow: forwardsRow(last.Row, row)}
						for mode := range 2 {
							c.AddUses(types.Transport(mode), 1<<last.Control.Resolve(context[mode]))
						}
						add(c)
						for _, stage := range stages {
							visit(stage.Args[0], context)
						}
						return false
					}
				}
				if e.CalleeKind == Worker {
					ref := e.Callee.(*VarRef)
					target := defs[ref.Name]
					for j, arg := range e.Args {
						v, ok := arg.(*VarRef)
						if !ok || !v.Local || v.Name != name {
							visit(arg, context)
							continue
						}
						if target == nil || !target.ABI.Valid || j >= len(target.ABI.Callbacks) {
							escapes = true
							continue
						}
						want := target.ABI.Callbacks[j]
						c := CallbackABI{Arity: want.Arity, ForwardsRow: want.ForwardsRow && forwardsRow(e.Row, row)}
						for mode := range 2 {
							c.AddUses(types.Transport(mode), want.Uses(e.Control.Resolve(context[mode])))
						}
						add(c)
					}
					return false
				}
			}
			return true
		})
	}
	visit(body, [2]types.Transport{types.Direct, types.Exit})
	if escapes || out.DirectUses == 3 || out.ExitUses == 3 || out.DirectUses|out.ExitUses == 0 {
		return CallbackABI{}
	}
	return out
}

// forwardsRow reports whether an argument hands on the row bound by row as it
// is. A call without a row argument passes none for a callback to observe.
func forwardsRow(arg *RowArgument, row types.CaptureVar) bool {
	return arg == nil || (row != 0 && arg.From == row && len(arg.Effects) == 0 && len(arg.Hidden) == 0)
}

// LocalCallbackABI applies the same conservative use contract to a local
// binding. It is not serialized: only the enclosing module can use it.
func LocalCallbackABI(body Expr, name string, t types.Type, defs map[string]*Def) CallbackABI {
	return callbackUses(body, name, callbackArity(t), 0, defs)
}

// inContext reads uses summarized for worker transports as uses emitted in
// the given control context.
func (c CallbackABI) inContext(context [2]types.Transport) CallbackABI {
	out := CallbackABI{Arity: c.Arity, ForwardsRow: c.ForwardsRow}
	for mode := range 2 {
		out.AddUses(types.Transport(mode), c.Uses(context[mode]))
	}
	return out
}
