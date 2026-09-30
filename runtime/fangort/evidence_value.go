package fangort

// EvidenceValue is the compiled residual-row ABI. Small rows travel by value;
// overflow layers and activation bindings remain immutable shared objects.
type EvidenceValue struct {
	inline [2]*EvidenceBinding
	tail   *EvidenceRow
}

func (row EvidenceValue) visible(name string, args []*TypeDescriptor) *EvidenceBinding {
	for _, binding := range row.inline {
		if binding != nil && sameEvidenceBinding(name, args, *binding) {
			return binding
		}
	}
	return visibleBinding(row.tail, name, args)
}

func ExtendEvidenceValue(row EvidenceValue, bindings ...*EvidenceBinding) EvidenceValue {
	for i, binding := range bindings {
		if binding == nil {
			panic("fangort: nil residual evidence binding")
		}
		for _, prior := range bindings[:i] {
			if sameEvidenceBinding(binding.Name, binding.Arguments, *prior) {
				panic("fangort: duplicate residual evidence binding")
			}
		}
	}
	for _, binding := range bindings {
		if row.visible(binding.Name, binding.Arguments) == binding {
			continue
		}
		placed := false
		for i, prior := range row.inline {
			if prior == nil || sameEvidenceBinding(binding.Name, binding.Arguments, *prior) {
				row.inline[i], placed = binding, true
				break
			}
		}
		if !placed {
			row.tail = ExtendEvidenceBindings(row.tail, row.inline[:]...)
			row.inline = [2]*EvidenceBinding{binding}
		}
	}
	return row
}

func ValueEvidence[T any](row EvidenceValue, name string, mode EvidenceMode, args ...*TypeDescriptor) T {
	binding := row.visible(name, args)
	if binding == nil {
		panic("fangort: missing residual evidence")
	}
	value := binding.Family.Direct
	if mode == ExitEvidence {
		value = binding.Family.Exit
	}
	if typed, ok := value.(T); ok {
		return typed
	}
	panic("fangort: invalid residual evidence representation")
}

// Value rebases a compiled invocation row using the same activation memo as
// captured lexical evidence. Materialization is confined to task creation.
func (f *EvidenceFork) Value(row EvidenceValue) EvidenceValue {
	var bindings []*EvidenceBinding
	for _, binding := range row.inline {
		if binding != nil {
			bindings = append(bindings, binding)
		}
	}
	return EvidenceValue{tail: f.Row(ExtendEvidenceBindings(row.tail, bindings...))}
}
