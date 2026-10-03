package natives

import stdlib "github.com/waj/fango/stdlib"

func installRegex(t map[string]Spec) {
	t["Regex.compileRaw"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.CompileRaw(args[0].(string)), nil }}
	t["Regex.compileSucceeded"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.CompileSucceeded(args[0]), nil }}
	t["Regex.compiledRegex"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.CompiledRegex(args[0]), nil }}
	t["Regex.compileMessage"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.CompileMessage(args[0]), nil }}
	t["Regex.pattern"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.Pattern(args[0]), nil }}
	t["Regex.matches"] = Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.Matches(args[0], args[1].(string)), nil }}
	t["Regex.replaceAll"] = Spec{Arity: 3, Eval: func(_ *Runtime, args []any) (any, error) {
		return stdlib.ReplaceAll(args[0], args[1].(string), args[2].(string)), nil
	}}
	t["Regex.replaceAllLiteral"] = Spec{Arity: 3, Eval: func(_ *Runtime, args []any) (any, error) {
		return stdlib.ReplaceAllLiteral(args[0], args[1].(string), args[2].(string)), nil
	}}
	t["Regex.escape"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.Escape(args[0].(string)), nil }}
	t["Regex.findRaw"] = Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.FindRaw(args[0], args[1].(string)), nil }}
	t["Regex.findAllRaw"] = Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.FindAllRaw(args[0], args[1].(string)), nil }}
	t["Regex.matchCount"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.MatchCount(args[0]), nil }}
	t["Regex.captureCount"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.CaptureCount(args[0]), nil }}
	t["Regex.captureName"] = Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.CaptureName(args[0], args[1].(int64)), nil }}
	t["Regex.matchText"] = Spec{Arity: 3, Eval: func(_ *Runtime, args []any) (any, error) {
		return stdlib.MatchText(args[0], args[1].(int64), args[2].(int64)), nil
	}}
	t["Regex.matchStart"] = Spec{Arity: 3, Eval: func(_ *Runtime, args []any) (any, error) {
		return stdlib.MatchStart(args[0], args[1].(int64), args[2].(int64)), nil
	}}
	t["Regex.matchEnd"] = Spec{Arity: 3, Eval: func(_ *Runtime, args []any) (any, error) {
		return stdlib.MatchEnd(args[0], args[1].(int64), args[2].(int64)), nil
	}}
	t["Regex.splitRaw"] = Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.SplitRaw(args[0], args[1].(string)), nil }}
	t["Regex.splitCount"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.SplitCount(args[0]), nil }}
	t["Regex.splitItem"] = Spec{Arity: 2, Eval: func(_ *Runtime, args []any) (any, error) { return stdlib.SplitItem(args[0], args[1].(int64)), nil }}
}
