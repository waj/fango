package natives

import (
	"errors"
	"fmt"
)

// installPrompt supplies evaluator-owned traps, not sidecar bindings.
func installPrompt(t map[string]Spec) {
	// The REPL's evaluator answers Runtime.Prompt.level before the table.
	t["Runtime.Prompt.level"] = Spec{Arity: 1, Eval: func(_ *Runtime, _ []any) (any, error) {
		return nil, errors.New("Runtime.Prompt.level runs only inside a prompt level")
	}}
	// A prompt value computed by an effectful declaration keeps this as its
	// body: whichever evaluator ran the declaration holds the memo, and one
	// that does not must not perform the effect again.
	t["Runtime.Prompt.stale"] = Spec{Arity: 1, Eval: func(_ *Runtime, args []any) (any, error) {
		return nil, fmt.Errorf("`%s` holds the result of an effectful declaration that ran before the native worker started or was rebuilt; define it again", args[0].(string))
	}}
}
