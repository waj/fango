package types

const (
	WorkOwnerTypeName = "Runtime.Work.Owner"
	WorkFacetTypeName = "Runtime.Work.Facet"
	WorkTypeName      = "Runtime.Work.Work"
	WorkOwnerName     = "Runtime.Work.owner"
	WorkRegisterName  = "Runtime.Work.register"
	WorkRunName       = "Runtime.Work.run"
	WorkFacetName     = "Runtime.Work.facet"
	WorkPackName      = "Runtime.Work.pack"
	WorkAdvanceName   = "Runtime.Work.advance"
	WorkCloseName     = "Runtime.Work.close"
)

func WorkIntrinsic(name string) bool {
	switch name {
	case WorkOwnerName, WorkRegisterName, WorkRunName, WorkFacetName, WorkPackName, WorkAdvanceName, WorkCloseName:
		return true
	}
	return false
}

func WorkProtocol(t Type) (request, reply, result Type, ok bool) {
	c, ok := t.(*TCon)
	if !ok || c.Name != WorkTypeName || len(c.Args) != 3 {
		return nil, nil, nil, false
	}
	return c.Args[0], c.Args[1], c.Args[2], true
}

// WorkShape fixes every introduction/elimination edge independently of bundled
// declaration text. In particular, opening cannot claim a pure execution row.
func WorkShape(name string, ty Type) bool {
	if !WorkIntrinsic(name) || ty == nil {
		return false
	}
	args := []Type{}
	effects := []Row{}
	rest := ty
	for range IntrinsicArity(name) {
		f, ok := rest.(*TFun)
		if !ok {
			return false
		}
		args = append(args, f.Arg)
		effects = append(effects, f.Eff)
		rest = f.Ret
	}
	for _, eff := range effects[:len(effects)-1] {
		if len(eff.Labels) != 0 || eff.Tail != nil {
			return false
		}
	}
	con := func(t Type, name string, n int) (*TCon, bool) {
		c, ok := t.(*TCon)
		return c, ok && c.Name == name && len(c.Args) == n
	}
	row := func(r Row, tail Type, drive bool) bool {
		if !(r.Tail == nil && tail == nil || Equal(r.Tail, tail)) {
			return false
		}
		if !drive {
			return len(r.Labels) == 0
		}
		return len(r.Labels) == 1 && r.Labels[0].Name == CoroutineDriveName && r.Labels[0].Suspension && !r.Labels[0].Abort
	}
	last := effects[len(effects)-1]
	switch name {
	case WorkOwnerName:
		scope, s := con(args[0], CoroutineScopeTypeName, 1)
		owner, o := con(rest, WorkOwnerTypeName, 1)
		return s && o && Equal(scope.Args[0], owner.Args[0]) && row(last, nil, false)
	case WorkRegisterName:
		_, f := con(args[0], CoroutineFacetTypeName, 0)
		work, w := con(rest, WorkTypeName, 3)
		factory, ok := args[1].(*TFun)
		if !f || !w || !ok || !row(factory.Eff, nil, false) {
			return false
		}
		pause, p := factory.Arg.(*TFun)
		body, b := factory.Ret.(*TFun)
		if !p || !b || len(pause.Eff.Labels) != 1 || pause.Eff.Tail != nil || pause.Eff.Labels[0].Name != CoroutineSuspensionName || !pause.Eff.Labels[0].Suspension || pause.Eff.Labels[0].Abort || len(pause.Eff.Labels[0].Args) != 0 {
			return false
		}
		return Equal(pause.Arg, work.Args[0]) && Equal(pause.Ret, work.Args[1]) && Equal(body.Arg, work.Args[1]) && Equal(body.Ret, work.Args[2]) && len(body.Eff.Labels) == 1 && body.Eff.Labels[0].Name == CoroutineSuspensionName && body.Eff.Labels[0].Suspension && !body.Eff.Labels[0].Abort && len(body.Eff.Labels[0].Args) == 0 && row(last, body.Eff.Tail, false)
	case WorkRunName:
		f, ok := args[0].(*TFun)
		if !ok {
			return false
		}
		owner, ok := con(f.Arg, WorkOwnerTypeName, 1)
		return ok && row(f.Eff, owner.Args[0], true) && row(last, owner.Args[0], true) && Equal(rest, f.Ret)
	case WorkFacetName:
		_, a := con(args[0], WorkOwnerTypeName, 1)
		_, b := con(rest, WorkFacetTypeName, 0)
		return a && b && row(last, nil, false)
	case WorkPackName:
		_, facet := con(args[0], WorkFacetTypeName, 0)
		cursor, c := con(args[1], CoroutineTypeName, 4)
		work, w := con(rest, WorkTypeName, 3)
		if !facet || !c || !w || !row(last, cursor.Args[3], false) {
			return false
		}
		for i := range 3 {
			if !Equal(cursor.Args[i], work.Args[i]) {
				return false
			}
		}
		return true
	case WorkAdvanceName, WorkCloseName:
		owner, o := con(args[0], WorkOwnerTypeName, 1)
		work, w := con(args[1], WorkTypeName, 3)
		if !o || !w || !row(last, owner.Args[0], true) {
			return false
		}
		if name == WorkCloseName {
			_, unit := con(rest, "()", 0)
			return unit
		}
		step, s := con(rest, CoroutineStepName, 2)
		return s && Equal(args[2], work.Args[1]) && Equal(step.Args[0], work.Args[0]) && Equal(step.Args[1], work.Args[2])
	}
	return false
}
