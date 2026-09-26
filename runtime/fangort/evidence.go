package fangort

// EvidenceFamily carries the module-owned representations of one checked
// effect instance. Entries below its required transport are absent.
type EvidenceFamily struct {
	Direct, Exit any
}

type EvidenceBinding struct {
	Name   string
	Family EvidenceFamily
}

// EvidenceRow is an explicit residual argument, never a global handler stack.
// Extensions and bindings are immutable.
type EvidenceRow struct {
	tail   *EvidenceRow
	fields map[string]EvidenceFamily
}

func ExtendEvidenceRow(tail *EvidenceRow, bindings ...EvidenceBinding) *EvidenceRow {
	if len(bindings) == 0 {
		return tail
	}
	fields := make(map[string]EvidenceFamily, len(bindings))
	for _, binding := range bindings {
		if _, exists := fields[binding.Name]; exists {
			panic("fangort: duplicate residual evidence binding")
		}
		fields[binding.Name] = binding.Family
	}
	return &EvidenceRow{tail: tail, fields: fields}
}

type EvidenceMode uint8

const (
	DirectEvidence EvidenceMode = iota
	ExitEvidence
)

// RowEvidence projects a representation whose nominal type and availability
// have been proved by Core. A mismatch is an internal compiler invariant.
func RowEvidence[T any](row *EvidenceRow, name string, mode EvidenceMode) T {
	for row != nil {
		if family, ok := row.fields[name]; ok {
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
