// Package feasibility contains test-only models of proposed C0/C4/A0 contracts.
// Nothing here is linked into the compiler or grants a source program access
// to Coroutine or Async. See doc/roadmap-coroutines.md and roadmap-async.md.
package feasibility

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
)

type lifetime struct {
	name   string
	parent *lifetime
}

func outlives(outer, inner *lifetime) bool {
	for s := inner; s != nil; s = s.parent {
		if outer == s {
			return true
		}
	}
	return false
}

type capability struct {
	owner    *lifetime
	producer bool
	shared   bool
}

// This conservative rule deliberately has no non-retaining reply exception.
func retain(destination *lifetime, captures []capability, child bool) error {
	for _, c := range captures {
		if c.owner == nil {
			return fmt.Errorf("missing capture owner")
		}
		if c.producer || !outlives(c.owner, destination) || child && !c.shared {
			return fmt.Errorf("invalid retention from %s into %s", c.owner.name, destination.name)
		}
	}
	return nil
}

type protocol struct{ request, reply, result string }
type owner struct {
	name string
	protocol
}
type control struct {
	kind string
	to   *owner
}
type controls map[control]bool

func (c controls) without(kind string, o *owner) controls {
	out := controls{}
	for k := range c {
		if k.kind != kind || k.to != o {
			out[k] = true
		}
	}
	return out
}

func (c controls) row() []string {
	var row []string
	for k := range c {
		if !slices.Contains(row, k.kind) {
			row = append(row, k.kind)
		}
	}
	slices.Sort(row)
	return row
}

// A tiny semantic tree allows reconstruction from operations, independently of
// the claimed summary. No nominal-label subtraction is used for discharge.
type term struct {
	kind     string
	owner    *owner
	protocol protocol
	children []term
}

func reconstruct(n term, active map[*owner]bool) (controls, error) {
	result := controls{}
	if n.kind != "sequence" {
		if n.owner == nil || n.owner.protocol != n.protocol {
			return nil, fmt.Errorf("missing owner or mismatched exchange")
		}
	}
	switch n.kind {
	case "Suspension":
		if !active[n.owner] {
			return nil, fmt.Errorf("pause outside producer execution")
		}
		result[control{n.kind, n.owner}] = true
	case "close", "advance":
		if active[n.owner] {
			return nil, fmt.Errorf("overlapping advance/close")
		}
		result[control{"Drive", n.owner}] = true
		active[n.owner] = true
		defer delete(active, n.owner)
	case "boundary", "sequence":
	default:
		return nil, fmt.Errorf("unknown operation")
	}
	for _, child := range n.children {
		c, err := reconstruct(child, active)
		if err != nil {
			return nil, err
		}
		for k := range c {
			result[k] = true
		}
	}
	if n.kind == "advance" {
		result = result.without("Suspension", n.owner)
	}
	if n.kind == "boundary" {
		result = result.without("Drive", n.owner)
	}
	return result, nil
}

func TestOwnerSensitiveDischargeAndIndependentReconstruction(t *testing.T) {
	task := &owner{"task", protocol{"Wait", "Unit", "Unit"}}
	pull := &owner{"pull", protocol{"Int", "Unit", "Unit"}}
	pause := func(o *owner) term { return term{kind: "Suspension", owner: o, protocol: o.protocol} }
	drive := func(o *owner, children ...term) term {
		return term{"advance", o, o.protocol, children}
	}
	boundary := func(o *owner, children ...term) term {
		return term{"boundary", o, o.protocol, children}
	}
	inner := boundary(pull, drive(pull, pause(task), pause(pull)))
	got, err := reconstruct(inner, map[*owner]bool{task: true})
	if err != nil || !reflect.DeepEqual(got.row(), []string{"Suspension"}) || !got[control{"Suspension", task}] {
		t.Fatalf("unfinished pull must be Machine with outer Suspension: %v %v", got, err)
	}
	whole := boundary(task, drive(task, inner))
	got, err = reconstruct(whole, map[*owner]bool{})
	if err != nil || len(got) != 0 {
		t.Fatalf("outer boundary should discharge its own control: %v %v", got, err)
	}
	// An outer advance inside an unrelated boundary keeps its Drive obligation.
	got, err = reconstruct(boundary(pull, drive(task)), map[*owner]bool{})
	if err != nil || !got[control{"Drive", task}] {
		t.Fatalf("foreign Drive swallowed: %v %v", got, err)
	}
	for _, bad := range []term{
		pause(task), // Driver does not hold producer authority.
		{kind: "Suspension", protocol: task.protocol},
		{kind: "advance", owner: task, protocol: pull.protocol},
		drive(task, drive(task)),
		drive(task, term{kind: "close", owner: task, protocol: task.protocol}),
	} {
		if _, err := reconstruct(bad, map[*owner]bool{}); err == nil {
			t.Fatalf("accepted malformed/reentrant term: %+v", bad)
		}
	}
	// A cached summary claiming Direct is rejected by reconstruction.
	actual, err := reconstruct(inner, map[*owner]bool{task: true})
	claimed := controls{}
	if err != nil || reflect.DeepEqual(actual, claimed) {
		t.Fatal("false synchronous summary survived reconstruction")
	}
	if _, err := reconstruct(term{kind: "sequence", children: []term{drive(task), drive(task)}}, map[*owner]bool{}); err != nil {
		t.Fatal("sequential aliases should be legal:", err)
	}
}

func TestRetentionAndChildCapabilityContracts(t *testing.T) {
	root := &lifetime{name: "root"}
	scope := &lifetime{"dynamic owner", root}
	helper := &lifetime{"helper resource", scope}
	for _, test := range []struct {
		name        string
		destination *lifetime
		captures    []capability
		child, ok   bool
	}{
		{"missing capture owner", scope, []capability{{}}, false, false},
		{"immutable reply", scope, nil, false, true},
		{"outer resource request", scope, []capability{{owner: root}}, false, true},
		{"producer local request", scope, []capability{{owner: helper}}, false, false},
		{"short reply retained by outer coroutine", scope, []capability{{owner: helper}}, false, false},
		{"pause hidden in ADT or closure", scope, []capability{{owner: scope, producer: true}}, false, false},
		{"scope handle escapes", root, []capability{{owner: scope}}, false, false},
		{"helper allocation in caller scope", scope, nil, true, true},
		{"helper local allocation capture", scope, []capability{{owner: helper}}, true, false},
		{"hidden parent mutable evidence", scope, []capability{{owner: root}}, true, false},
		{"shared context service", scope, []capability{{owner: scope, shared: true}}, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := retain(test.destination, test.captures, test.child) == nil; got != test.ok {
				t.Fatalf("accepted = %v, want %v", got, test.ok)
			}
		})
	}
}
