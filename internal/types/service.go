package types

import "fmt"

// Service invocation authority has a fixed request/reply protocol. The runtime
// operation gains one hidden callback carrying the invoking producer's slot.
func (op *EffectOp) RuntimeParamTypes() []Type {
	if op.Invocation == nil {
		return op.ParamTypes
	}
	return append(append([]Type(nil), op.ParamTypes...), InvocationCallback(*op.Invocation))
}

func InvocationCallback(label EffLabel) Type {
	if len(label.Args) != 2 {
		return nil
	}
	return &TFun{Arg: label.Args[0], Ret: label.Args[1], Control: Control{Transport: Machine}}
}

func ServiceRunShape(ty Type) bool {
	f, ok := ty.(*TFun)
	if !ok || !f.Eff.Empty() {
		return false
	}
	pause, ok := f.Arg.(*TFun)
	if !ok || len(pause.Eff.Labels) != 1 || pause.Eff.Labels[0].Name != CoroutineSuspensionName || pause.Eff.Tail != nil {
		return false
	}
	next, ok := f.Ret.(*TFun)
	if !ok {
		return false
	}
	action, ok := next.Arg.(*TFun)
	if !ok {
		return false
	}
	unit, ok := action.Arg.(*TCon)
	if !ok || unit.Name != "()" || len(action.Eff.Labels) != 1 {
		return false
	}
	inv := action.Eff.Labels[0]
	return inv.Name == ServiceInvocationName && len(inv.Args) == 2 && Equal(inv.Args[0], pause.Arg) && Equal(inv.Args[1], pause.Ret) && Equal(next.Ret, action.Ret) && Equal(Row{Tail: action.Eff.Tail}, Row{Tail: next.Eff.Tail}) && len(next.Eff.Labels) == 1 && next.Eff.Labels[0].Name == CoroutineSuspensionName
}

// CheckServiceEffect reconstructs the split-evidence protocol from the public
// operation schemes; serialized flags alone never authorize an adapter.
func CheckServiceEffect(effect *EffectInfo, effects map[int]*EffectInfo) error {
	if effect.Invocation {
		if effect.Name != ServiceInvocationName || effect.Service || !effect.Scoped || len(effect.Params) != 2 || len(effect.Ops) != 1 {
			return fmt.Errorf("invalid compiler-owned invocation effect")
		}
		op := effect.Ops[0]
		if op.Name != "Service.invoke" || op.Arity != 1 || len(op.ParamTypes) != 1 || op.Abort || op.Native != nil || !Equal(op.ParamTypes[0], effect.Params[0]) || !Equal(op.ResultType, effect.Params[1]) {
			return fmt.Errorf("invalid compiler-owned invocation protocol")
		}
	} else if effect.Name == ServiceInvocationName {
		return fmt.Errorf("missing compiler-owned invocation contract")
	}
	var protocol *EffLabel
	for _, op := range effect.Ops {
		if (op.Invocation != nil) != effect.Service {
			return fmt.Errorf("service operation metadata disagrees with its effect")
		}
		if !effect.Service {
			continue
		}
		inv := op.Invocation
		owner := effects[inv.Unique]
		if owner == nil || !owner.Invocation || owner.Name != ServiceInvocationName || len(inv.Args) != 2 || op.Abort || op.Native != nil {
			return fmt.Errorf("service operation requires a fixed invocation protocol")
		}
		for _, local := range op.LocalVars {
			for _, arg := range inv.Args {
				for _, variable := range RigidVarsIn(arg) {
					if variable.ID == local.ID {
						return fmt.Errorf("service protocol cannot depend on an operation-local type")
					}
				}
			}
		}
		if protocol != nil && !Equal(Row{Labels: []EffLabel{*protocol}}, Row{Labels: []EffLabel{*inv}}) {
			return fmt.Errorf("service operations disagree on their invocation protocol")
		}
		protocol = inv
		rest := op.Scheme.Body
		var last *TFun
		for range op.Arity {
			var ok bool
			last, ok = rest.(*TFun)
			if !ok {
				return fmt.Errorf("invalid service operation scheme")
			}
			rest = last.Ret
		}
		if last == nil || len(last.Eff.Labels) != 2 {
			return fmt.Errorf("service scheme lacks its implicit invocation requirement")
		}
		found := false
		for _, label := range last.Eff.Labels {
			if label.Unique == inv.Unique && Equal(Row{Labels: []EffLabel{label}}, Row{Labels: []EffLabel{*inv}}) {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("service scheme and invocation metadata disagree")
		}
	}
	return nil
}
