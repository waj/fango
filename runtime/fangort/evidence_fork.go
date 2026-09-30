package fangort

import "fmt"

// EvidenceOrigin identifies one lexical activation. Rebuild reconstructs its
// operation closures against child evidence while retaining its shared state.
// Abort activations have neither a fixed family nor a rebuild function: a child
// must supply a replacement.
type EvidenceOrigin struct {
	Name      string
	Arguments []*TypeDescriptor
	Resolve   func() *EvidenceOrigin
	Rebuild   func(*EvidenceFork) EvidenceFamily
	// Fixed is safe when operation closures have no evidence/row dependencies
	// to rebase. Their state and immutable lexical values are already shared.
	Fixed *EvidenceBinding
}

// EvidenceFork belongs to one child invocation, not a goroutine-global stack.
// Memoization preserves aliases, including dependencies hidden by row shadowing.
type EvidenceFork struct {
	overrides *EvidenceRow
	memo      map[*EvidenceOrigin]EvidenceFamily
	building  map[*EvidenceOrigin]bool
}

func NewEvidenceFork(overrides *EvidenceRow) *EvidenceFork {
	return &EvidenceFork{overrides: overrides, memo: make(map[*EvidenceOrigin]EvidenceFamily), building: make(map[*EvidenceOrigin]bool)}
}

func evidenceFamily(row *EvidenceRow, name string, args []*TypeDescriptor) (EvidenceFamily, bool) {
	for ; row != nil; row = row.tail {
		if family, ok := row.find(name, args); ok {
			return family, true
		}
	}
	return EvidenceFamily{}, false
}

func (f *EvidenceFork) Family(origin *EvidenceOrigin) EvidenceFamily {
	for origin != nil && origin.Resolve != nil {
		origin = origin.Resolve()
	}
	if origin == nil {
		panic("Async: inherited handler has no activation identity")
	}
	if replacement, ok := evidenceFamily(f.overrides, origin.Name, origin.Arguments); ok {
		target := replacement.Origin
		for target != nil && target.Resolve != nil {
			target = target.Resolve()
		}
		if target == nil || len(origin.Arguments) != len(target.Arguments) {
			panic("Async: incompatible inherited handler " + origin.Name)
		}
		for i, arg := range origin.Arguments {
			if !sameDescriptor(arg, target.Arguments[i]) {
				panic("Async: incompatible inherited handler " + origin.Name)
			}
		}
		return replacement
	}
	if family, ok := f.memo[origin]; ok {
		return family
	}
	if origin.Fixed != nil {
		return origin.Fixed.Family
	}
	if origin.Rebuild == nil {
		panic(fmt.Sprintf("Async: cannot inherit abort handler %s; handle it inside the task", origin.Name))
	}
	if f.building[origin] {
		panic("Async: cyclic inherited handler evidence")
	}
	f.building[origin] = true
	family := origin.Rebuild(f)
	delete(f.building, origin)
	f.memo[origin] = family
	return family
}

func ForkEvidence[T any](fork *EvidenceFork, origin *EvidenceOrigin, mode EvidenceMode) T {
	family := fork.Family(origin)
	value := family.Direct
	if mode == ExitEvidence {
		value = family.Exit
	}
	if typed, ok := value.(T); ok {
		return typed
	}
	panic(fmt.Sprintf("Async: incompatible inherited handler %s", origin.Name))
}

// Row rebuilds visible bindings only. A shadowed tail entry is not inherited.
func (f *EvidenceFork) Row(row *EvidenceRow) *EvidenceRow {
	var seen []EvidenceBinding
	var bindings []EvidenceBinding
	for ; row != nil; row = row.tail {
		for index := 0; index < len(row.inline)+len(row.extra); index++ {
			var binding *EvidenceBinding
			if index < len(row.inline) {
				binding = row.inline[index]
			} else {
				binding = row.extra[index-len(row.inline)]
			}
			if binding == nil {
				continue
			}
			shadowed := false
			for _, prior := range seen {
				if sameEvidenceBinding(binding.Name, binding.Arguments, prior) {
					shadowed = true
					break
				}
			}
			if !shadowed {
				seen = append(seen, *binding)
				bindings = append(bindings, EvidenceBinding{Name: binding.Name, Arguments: binding.Arguments, Family: f.Family(binding.Family.Origin)})
			}
		}
	}
	return ExtendEvidenceRow(nil, bindings...)
}

// DeferredEvidenceOrigin follows the same immutable row projection as the
// deferred operation record; it never captures a current goroutine context.
func DeferredEvidenceOrigin(row *EvidenceRow, name string, args ...*TypeDescriptor) *EvidenceOrigin {
	return &EvidenceOrigin{Name: name, Arguments: args, Resolve: func() *EvidenceOrigin {
		family, ok := evidenceFamily(row, name, args)
		if !ok {
			panic("Async: missing inherited handler " + name)
		}
		return family.Origin
	}}
}
