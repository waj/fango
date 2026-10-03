package fangort

// EvidenceFamily carries the module-owned representations of one checked
// effect instance. Entries below its required transport are absent.
type EvidenceFamily struct {
	Origin       *EvidenceOrigin
	Direct, Exit any
}

type EvidenceBinding struct {
	Name      string
	Arguments []*TypeDescriptor
	Family    EvidenceFamily
}

func sameEvidenceBinding(name string, args []*TypeDescriptor, binding *EvidenceBinding) bool {
	if name != binding.Name || len(args) != len(binding.Arguments) {
		return false
	}
	for i := range args {
		if !sameDescriptor(args[i], binding.Arguments[i]) {
			return false
		}
	}
	return true
}

// NewEvidenceBinding owns immutable metadata for one activation view. Compiled
// evidence records and their transport adapters share this binding.
func NewEvidenceBinding(family EvidenceFamily) *EvidenceBinding {
	if family.Origin == nil {
		panic("fangort: evidence binding has no activation")
	}
	return &EvidenceBinding{Name: family.Origin.Name, Arguments: family.Origin.Arguments, Family: family}
}

// EvidenceRow is an explicit residual argument, never a global handler stack.
// Extensions and bindings are immutable.
type EvidenceRow struct {
	tail   *EvidenceRow
	inline [2]*EvidenceBinding
	extra  []*EvidenceBinding
}

func ExtendEvidenceRow(tail *EvidenceRow, bindings ...EvidenceBinding) *EvidenceRow {
	if len(bindings) == 0 {
		return tail
	}
	pointers := make([]*EvidenceBinding, len(bindings))
	for index, binding := range bindings {
		if binding.Arguments == nil && binding.Family.Origin != nil {
			binding.Arguments = binding.Family.Origin.Arguments
			bindings[index].Arguments = binding.Arguments
		}
		pointers[index] = &binding
	}
	return ExtendEvidenceBindings(tail, pointers...)
}

// ExtendEvidenceBindings shares activation metadata rather than copying and
// boxing it on every call. An identical visible binding needs no overlay.
func ExtendEvidenceBindings(tail *EvidenceRow, bindings ...*EvidenceBinding) *EvidenceRow {
	for index, binding := range bindings {
		if binding == nil {
			panic("fangort: nil residual evidence binding")
		}
		for _, previous := range bindings[:index] {
			if sameEvidenceBinding(binding.Name, binding.Arguments, previous) {
				panic("fangort: duplicate residual evidence binding")
			}
		}
	}
	var row *EvidenceRow
	count := 0
	for _, binding := range bindings {
		if visible := visibleBinding(tail, binding.Name, binding.Arguments); visible == binding {
			continue
		}
		if row == nil {
			row = &EvidenceRow{tail: tail}
		}
		if count < len(row.inline) {
			row.inline[count] = binding
		} else {
			row.extra = append(row.extra, binding)
		}
		count++
	}
	if row == nil {
		return tail
	}
	return row
}

func visibleBinding(row *EvidenceRow, name string, args []*TypeDescriptor) *EvidenceBinding {
	for ; row != nil; row = row.tail {
		for _, binding := range row.inline {
			if binding != nil && sameEvidenceBinding(name, args, binding) {
				return binding
			}
		}
		for _, binding := range row.extra {
			if sameEvidenceBinding(name, args, binding) {
				return binding
			}
		}
	}
	return nil
}

func (row *EvidenceRow) find(name string, args []*TypeDescriptor) (EvidenceFamily, bool) {
	for _, binding := range row.inline {
		if binding != nil && sameEvidenceBinding(name, args, binding) {
			return binding.Family, true
		}
	}
	for _, binding := range row.extra {
		if sameEvidenceBinding(name, args, binding) {
			return binding.Family, true
		}
	}
	return EvidenceFamily{}, false
}

type EvidenceMode uint8

const (
	DirectEvidence EvidenceMode = iota
	ExitEvidence
)

// RowEvidence projects a representation whose nominal type and availability
// have been proved by Core. A mismatch is an internal compiler invariant.
// HasRowEvidence reports whether row or its tail binds the effect.
func HasRowEvidence(row *EvidenceRow, name string, args ...*TypeDescriptor) bool {
	for ; row != nil; row = row.tail {
		if _, ok := row.find(name, args); ok {
			return true
		}
	}
	return false
}

func RowEvidence[T any](row *EvidenceRow, name string, mode EvidenceMode, args ...*TypeDescriptor) T {
	for row != nil {
		if family, ok := row.find(name, args); ok {
			var value any
			switch mode {
			case DirectEvidence:
				value = family.Direct
			case ExitEvidence:
				value = family.Exit
			}
			if typed, ok := value.(T); ok {
				return typed
			}
			panic("fangort: invalid residual evidence representation")
		}
		row = row.tail
	}
	panic("fangort: missing residual evidence")
}
