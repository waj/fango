package natives

import stdlib "github.com/waj/fango/stdlib"

// installState supplies the atomic State runner's cell. Its operations are
// deterministic within one evaluation, so a runner may execute at compile time.
func installState(t map[string]Spec) {
	t["State.newAtomicCell"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.NewAtomicCell(args[0]), nil }}
	t["State.loadAtomic"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.LoadAtomic(args[0]), nil }}
	t["State.atomicValue"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.AtomicValue(args[0]), nil }}
	t["State.replaceAtomic"] = Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) {
		return stdlib.ReplaceAtomic(args[0], args[1]), nil
	}}
	t["State.storeAtomic"] = Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) {
		stdlib.StoreAtomic(args[0], args[1])
		return struct{}{}, nil
	}}
}
