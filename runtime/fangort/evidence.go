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

func sameEvidenceBinding(name string, args []*TypeDescriptor, binding EvidenceBinding) bool {
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

// EvidenceRow is an explicit residual argument, never a global handler stack.
// Extensions and bindings are immutable.
type EvidenceRow struct {
	tail   *EvidenceRow
	inline [2]EvidenceBinding
	count  int
	extra  []EvidenceBinding
}

func ExtendEvidenceRow(tail *EvidenceRow, bindings ...EvidenceBinding) *EvidenceRow {
	if len(bindings) == 0 {
		return tail
	}
	row := &EvidenceRow{tail: tail, count: len(bindings)}
	for index, binding := range bindings {
		if binding.Arguments == nil && binding.Family.Origin != nil {
			binding.Arguments = binding.Family.Origin.Arguments
			bindings[index].Arguments = binding.Arguments
		}
		for previous := 0; previous < index; previous++ {
			if sameEvidenceBinding(binding.Name, binding.Arguments, bindings[previous]) {
				panic("fangort: duplicate residual evidence binding")
			}
		}
		if index < len(row.inline) {
			row.inline[index] = binding
		} else {
			row.extra = append(row.extra, binding)
		}
	}
	return row
}

func (row *EvidenceRow) find(name string, args []*TypeDescriptor) (EvidenceFamily, bool) {
	for index := 0; index < row.count && index < len(row.inline); index++ {
		binding := row.inline[index]
		if sameEvidenceBinding(name, args, binding) {
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
