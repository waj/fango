package machine

import (
	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/types"
)

// Scope's source contract checks the release callback before row widening.
// Its Machine member transports that callback synchronously.
func synchronousScopeMember(source *core.Def) (*core.Def, []int) {
	if _, ok := source.Body.(*core.Bracket); !ok {
		return source, nil
	}
	copy, params := synchronousScopeSignature(source)
	if params == nil {
		return source, nil
	}
	replacements := map[string]types.Type{}
	args, _ := core.PeelFun(copy.Type, 3)
	for _, i := range params {
		replacements[source.Params[i]] = args[i]
	}
	copy.Body = core.Rewrite(source.Body, identityType, func(e core.Expr) core.Expr {
		if ref, ok := e.(*core.VarRef); ok && ref.Local && replacements[ref.Name] != nil {
			ref.Ty = replacements[ref.Name]
		}
		if call, ok := e.(*core.App); ok && call.CalleeKind == core.Value {
			if ref, ok := call.Callee.(*core.VarRef); ok && replacements[ref.Name] != nil {
				call.Control = types.Control{Transport: types.Exit}
			}
		}
		return e
	})
	return copy, params
}

// synchronousScopeSignature is the same transport decision taken from the
// declaration alone, so a consumer can link against the member its dependency
// lowered without reading that dependency's body.
func synchronousScopeSignature(source *core.Def) (*core.Def, []int) {
	if source.Name != types.ScopeBracketName || len(source.Params) != 3 {
		return source, nil
	}
	args, result := core.PeelFun(source.Type, 3)
	for _, i := range []int{1} {
		fn := *args[i].(*types.TFun)
		fn.Control = types.Control{Transport: types.Exit}
		args[i] = &fn
	}
	copy := *source
	copy.Type = result
	var arrows []*types.TFun
	for ty := source.Type; len(arrows) < 3; {
		fn := ty.(*types.TFun)
		arrows = append(arrows, fn)
		ty = fn.Ret
	}
	for i := 2; i >= 0; i-- {
		fn := *arrows[i]
		fn.Arg, fn.Ret, fn.Control = args[i], copy.Type, core.ArrowControl(source.Type, i+1)
		copy.Type = &fn
	}
	return &copy, []int{1}
}
