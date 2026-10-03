package fangort

// PromptLevel stands for Runtime.Prompt.level in compiled code. Only the REPL's
// evaluator can run a prompt level; it answers the call before reaching here.
func PromptLevel(any) Unit {
	panic("Runtime.Prompt.level runs only at the REPL prompt")
}
