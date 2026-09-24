package types

// CoroutineProtocol extracts the three value indices. The fourth index is an
// effect row before elaboration and its erased representation afterwards.
func CoroutineProtocol(t Type) (request, reply, result Type, ok bool) {
	c, ok := t.(*TCon)
	if !ok || c.Name != CoroutineTypeName || len(c.Args) != 4 {
		return nil, nil, nil, false
	}
	return c.Args[0], c.Args[1], c.Args[2], true
}

func CoroutineShape(name string, ty Type) bool {
	fn, ok := ty.(*TFun)
	if !ok {
		return false
	}
	row := func(r Row, name string, tail Type) bool {
		if !(r.Tail == nil && tail == nil || Equal(r.Tail, tail)) || len(r.Labels) != 1 {
			return false
		}
		l := r.Labels[0]
		return l.Name == name && l.Suspension && !l.Abort && len(l.Args) == 0
	}
	pure := func(r Row) bool { return len(r.Labels) == 0 && r.Tail == nil }
	if name == CoroutineFacetName {
		facet, ok := fn.Ret.(*TCon)
		return CoroutineScopeType(fn.Arg) && ok && facet.Name == CoroutineFacetTypeName && len(facet.Args) == 0 && pure(fn.Eff)
	}
	if name == CoroutineScopeName {
		driver, ok := fn.Arg.(*TFun)
		if !ok || !CoroutineScopeType(driver.Arg) {
			return false
		}
		e := driver.Arg.(*TCon).Args[0]
		return row(driver.Eff, CoroutineDriveName, e) && Equal(driver.Ret, fn.Ret) && len(fn.Eff.Labels) == 0 && Equal(fn.Eff.Tail, e)
	}
	if name == CoroutineCreateName {
		last, ok := fn.Ret.(*TFun)
		if !ok || !CoroutineScopeType(fn.Arg) || !pure(fn.Eff) || !pure(last.Eff) {
			return false
		}
		q, r, a, ok := CoroutineProtocol(last.Ret)
		if !ok || !Equal(last.Ret.(*TCon).Args[3], fn.Arg.(*TCon).Args[0]) {
			return false
		}
		factory, ok := last.Arg.(*TFun)
		if !ok || !pure(factory.Eff) {
			return false
		}
		pause, ok := factory.Arg.(*TFun)
		if !ok {
			return false
		}
		body, ok := factory.Ret.(*TFun)
		if !ok {
			return false
		}
		return Equal(pause.Arg, q) && Equal(pause.Ret, r) && row(pause.Eff, CoroutineSuspensionName, nil) && Equal(body.Arg, r) && Equal(body.Ret, a) && row(body.Eff, CoroutineSuspensionName, fn.Arg.(*TCon).Args[0])
	}
	if name == CoroutineWithName {
		producer, ok := fn.Arg.(*TFun)
		if !ok || !pure(fn.Eff) || !pure(producer.Eff) {
			return false
		}
		body, ok := producer.Ret.(*TFun)
		if !ok {
			return false
		}
		pause, ok := producer.Arg.(*TFun)
		if !ok {
			return false
		}
		driverArrow, ok := fn.Ret.(*TFun)
		if !ok {
			return false
		}
		driver, ok := driverArrow.Arg.(*TFun)
		if !ok {
			return false
		}
		request, reply, result, ok := CoroutineProtocol(driver.Arg)
		if !ok {
			return false
		}
		e := driver.Arg.(*TCon).Args[3]
		return Equal(pause.Arg, request) && Equal(pause.Ret, reply) && row(pause.Eff, CoroutineSuspensionName, nil) &&
			Equal(body.Arg, reply) && Equal(body.Ret, result) && row(body.Eff, CoroutineSuspensionName, e) &&
			row(driver.Eff, CoroutineDriveName, e) && Equal(driver.Ret, driverArrow.Ret) &&
			len(driverArrow.Eff.Labels) == 0 && Equal(driverArrow.Eff.Tail, e)
	}
	request, reply, result, ok := CoroutineProtocol(fn.Arg)
	if !ok {
		return false
	}
	e := fn.Arg.(*TCon).Args[3]
	if name == CoroutineCloseName {
		return row(fn.Eff, CoroutineDriveName, e) && func() bool { c, ok := fn.Ret.(*TCon); return ok && c.Name == "()" && len(c.Args) == 0 }()
	}
	next, ok := fn.Ret.(*TFun)
	if !ok || !pure(fn.Eff) || !Equal(next.Arg, reply) || !row(next.Eff, CoroutineDriveName, e) {
		return false
	}
	step, ok := next.Ret.(*TCon)
	return ok && step.Name == CoroutineStepName && len(step.Args) == 2 && Equal(step.Args[0], request) && Equal(step.Args[1], result)
}

func CoroutineScopeType(t Type) bool {
	c, ok := t.(*TCon)
	return ok && c.Name == CoroutineScopeTypeName && len(c.Args) == 1
}
