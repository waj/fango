package types

import (
	"fmt"
)

// FunctionDescriptorShape retains callable type identity even though functions
// remain opaque to failure payload inspection.
func FunctionDescriptorShape(fn *TFun) (string, []Type) {
	name := fmt.Sprintf("<function;open=%t", FunctionOpenRow(fn))
	args := []Type{fn.Arg, fn.Ret}
	for _, effect := range SortedRow(fn.Eff).Labels {
		name += ";" + effect.Name + fmt.Sprintf("/%d", len(effect.Args))
		args = append(args, effect.Args...)
	}
	return name + ">", args
}
