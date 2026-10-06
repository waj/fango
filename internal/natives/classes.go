package natives

func installScalarInstances(t map[string]Spec) {
	t["Basics.intAdd"] = Spec{Arity: 2, Foldable: true, Eval: func(_ *Runtime, args []any) (any, error) { return evalBasics("add", args[0], args[1]), nil }}
	t["Basics.intSub"] = Spec{Arity: 2, Foldable: true, Eval: func(_ *Runtime, args []any) (any, error) { return evalBasics("sub", args[0], args[1]), nil }}
	t["Basics.intMul"] = Spec{Arity: 2, Foldable: true, Eval: func(_ *Runtime, args []any) (any, error) { return evalBasics("mul", args[0], args[1]), nil }}
	t["Basics.intEq"] = Spec{Arity: 2, Foldable: false, Eval: func(_ *Runtime, args []any) (any, error) { return evalBasics("eq", args[0], args[1]), nil }}
	t["Basics.intLt"] = Spec{Arity: 2, Foldable: false, Eval: func(_ *Runtime, args []any) (any, error) { return evalBasics("lt", args[0], args[1]), nil }}
	t["Basics.intGt"] = Spec{Arity: 2, Foldable: false, Eval: func(_ *Runtime, args []any) (any, error) { return evalBasics("gt", args[0], args[1]), nil }}
	t["Basics.intLe"] = Spec{Arity: 2, Foldable: false, Eval: func(_ *Runtime, args []any) (any, error) { return evalBasics("le", args[0], args[1]), nil }}
	t["Basics.intGe"] = Spec{Arity: 2, Foldable: false, Eval: func(_ *Runtime, args []any) (any, error) { return evalBasics("ge", args[0], args[1]), nil }}
	t["Basics.intNegate"] = Spec{Arity: 1, Foldable: true, Eval: func(_ *Runtime, args []any) (any, error) { return -args[0].(int64), nil }}
	t["Basics.floatAdd"] = Spec{Arity: 2, Foldable: true, Eval: func(_ *Runtime, args []any) (any, error) { return evalBasics("add", args[0], args[1]), nil }}
	t["Basics.floatSub"] = Spec{Arity: 2, Foldable: true, Eval: func(_ *Runtime, args []any) (any, error) { return evalBasics("sub", args[0], args[1]), nil }}
	t["Basics.floatMul"] = Spec{Arity: 2, Foldable: true, Eval: func(_ *Runtime, args []any) (any, error) { return evalBasics("mul", args[0], args[1]), nil }}
	t["Basics.floatEq"] = Spec{Arity: 2, Foldable: false, Eval: func(_ *Runtime, args []any) (any, error) { return evalBasics("eq", args[0], args[1]), nil }}
	t["Basics.floatLt"] = Spec{Arity: 2, Foldable: false, Eval: func(_ *Runtime, args []any) (any, error) { return evalBasics("lt", args[0], args[1]), nil }}
	t["Basics.floatGt"] = Spec{Arity: 2, Foldable: false, Eval: func(_ *Runtime, args []any) (any, error) { return evalBasics("gt", args[0], args[1]), nil }}
	t["Basics.floatLe"] = Spec{Arity: 2, Foldable: false, Eval: func(_ *Runtime, args []any) (any, error) { return evalBasics("le", args[0], args[1]), nil }}
	t["Basics.floatGe"] = Spec{Arity: 2, Foldable: false, Eval: func(_ *Runtime, args []any) (any, error) { return evalBasics("ge", args[0], args[1]), nil }}
	t["Basics.floatNegate"] = Spec{Arity: 1, Foldable: true, Eval: func(_ *Runtime, args []any) (any, error) { return -args[0].(float64), nil }}
	t["Basics.stringEq"] = Spec{Arity: 2, Foldable: false, Eval: func(_ *Runtime, args []any) (any, error) { return evalBasics("eq", args[0], args[1]), nil }}
	t["Basics.stringLt"] = Spec{Arity: 2, Foldable: false, Eval: func(_ *Runtime, args []any) (any, error) { return evalBasics("lt", args[0], args[1]), nil }}
	t["Basics.stringGt"] = Spec{Arity: 2, Foldable: false, Eval: func(_ *Runtime, args []any) (any, error) { return evalBasics("gt", args[0], args[1]), nil }}
	t["Basics.stringLe"] = Spec{Arity: 2, Foldable: false, Eval: func(_ *Runtime, args []any) (any, error) { return evalBasics("le", args[0], args[1]), nil }}
	t["Basics.stringGe"] = Spec{Arity: 2, Foldable: false, Eval: func(_ *Runtime, args []any) (any, error) { return evalBasics("ge", args[0], args[1]), nil }}
	t["Basics.boolEq"] = Spec{Arity: 2, Foldable: false, Eval: func(_ *Runtime, args []any) (any, error) { return evalBasics("eq", args[0], args[1]), nil }}
	t["Basics.charEq"] = Spec{Arity: 2, Foldable: false, Eval: func(_ *Runtime, args []any) (any, error) { return args[0].(rune) == args[1].(rune), nil }}
	t["Basics.charLt"] = Spec{Arity: 2, Foldable: false, Eval: func(_ *Runtime, args []any) (any, error) { return args[0].(rune) < args[1].(rune), nil }}
	t["Basics.charGt"] = Spec{Arity: 2, Foldable: false, Eval: func(_ *Runtime, args []any) (any, error) { return args[0].(rune) > args[1].(rune), nil }}
	t["Basics.charLe"] = Spec{Arity: 2, Foldable: false, Eval: func(_ *Runtime, args []any) (any, error) { return args[0].(rune) <= args[1].(rune), nil }}
	t["Basics.charGe"] = Spec{Arity: 2, Foldable: false, Eval: func(_ *Runtime, args []any) (any, error) { return args[0].(rune) >= args[1].(rune), nil }}
	t["Basics.floatFromInt"] = Spec{Arity: 1, Foldable: true, Eval: func(_ *Runtime, args []any) (any, error) { return float64(args[0].(int64)), nil }}
}
