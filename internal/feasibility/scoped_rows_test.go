package feasibility

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

// Test-only effect-contract algebra. Nominal labels include their parameter
// identity. Control-owner obligations are modeled separately in control_test.go.
// Variables stand for module-exported row parameters, not erased runtime data.
type effectRow struct {
	Labels map[string]string
	Vars   map[string]bool
}

func row(labels ...string) effectRow {
	r := effectRow{Labels: map[string]string{}, Vars: map[string]bool{}}
	for _, label := range labels {
		r.Labels[label] = "()"
	}
	return r
}

func joinRows(a, b effectRow) (effectRow, error) {
	out := row()
	for _, r := range []effectRow{a, b} {
		for label, payload := range r.Labels {
			if old, ok := out.Labels[label]; ok && old != payload {
				return effectRow{}, fmt.Errorf("incompatible parameter of %s", label)
			}
			out.Labels[label] = payload
		}
		for v := range r.Vars {
			out.Vars[v] = true
		}
	}
	return out, nil
}

func includes(budget, required effectRow) bool {
	if len(budget.Vars) != 0 || len(required.Vars) != 0 {
		return false
	}
	for label, payload := range required.Labels {
		if allowed, ok := budget.Labels[label]; !ok || allowed != payload {
			return false
		}
	}
	return true
}

type effectContract struct {
	Now   effectRow
	Owned map[string]effectRow // owner identity/parameter -> latent effect row
}

func emptyContract() effectContract { return effectContract{row(), map[string]effectRow{}} }

func joinContracts(a, b effectContract) (effectContract, error) {
	out := emptyContract()
	var err error
	out.Now, err = joinRows(a.Now, b.Now)
	if err != nil {
		return out, err
	}
	for _, c := range []effectContract{a, b} {
		for owner, required := range c.Owned {
			out.Owned[owner], err = joinRows(out.Owned[owner], required)
			if err != nil {
				return out, err
			}
		}
	}
	return out, nil
}

type rowTerm struct {
	op, label, payload, owner string
	children                  []rowTerm
	imported                  *effectContract
	rows                      map[string]effectRow
	owners                    map[string]string
}

func substituteRow(r effectRow, subst map[string]effectRow) (effectRow, error) {
	out := row()
	for label, payload := range r.Labels {
		out.Labels[label] = payload
	}
	for variable := range r.Vars {
		actual, ok := subst[variable]
		if !ok {
			return effectRow{}, fmt.Errorf("unresolved row %s", variable)
		}
		var err error
		out, err = joinRows(out, actual)
		if err != nil {
			return effectRow{}, err
		}
	}
	return out, nil
}

// Reconstruct from the tiny executable tree, rather than trusting a supplied
// budget. A surrounding handler may consume immediate effects, but only an
// owning drain consumes a registration obligation. Handling inside a child
// changes that child's actual row before registration.
func reconstructRows(n rowTerm) (effectContract, error) {
	c := emptyContract()
	for _, child := range n.children {
		other, err := reconstructRows(child)
		if err != nil {
			return c, err
		}
		c, err = joinContracts(c, other)
		if err != nil {
			return c, err
		}
	}
	switch n.op {
	case "sequence":
	case "perform":
		c.Now.Labels[n.label] = n.payload
	case "row parameter":
		c.Now.Vars[n.label] = true
	case "handle":
		delete(c.Now.Labels, n.label)
	case "register":
		if n.owner == "" {
			return c, fmt.Errorf("missing scope association")
		}
		var err error
		c.Owned[n.owner], err = joinRows(c.Owned[n.owner], c.Now)
		if err != nil {
			return c, err
		}
	case "drain":
		var err error
		c.Now, err = joinRows(c.Now, c.Owned[n.owner])
		if err != nil {
			return c, err
		}
		delete(c.Owned, n.owner)
	case "import":
		if n.imported == nil {
			return c, fmt.Errorf("missing module contract")
		}
		var err error
		c.Now, err = substituteRow(n.imported.Now, n.rows)
		if err != nil {
			return c, err
		}
		for symbolic, required := range n.imported.Owned {
			actual, ok := n.owners[symbolic]
			if !ok || actual == "" {
				return c, fmt.Errorf("unresolved owner %s", symbolic)
			}
			r, err := substituteRow(required, n.rows)
			if err != nil {
				return c, err
			}
			c.Owned[actual], err = joinRows(c.Owned[actual], r)
			if err != nil {
				return c, err
			}
		}
	default:
		return c, fmt.Errorf("unknown row term")
	}
	return c, nil
}

func perform(label string) rowTerm { return rowTerm{op: "perform", label: label, payload: "()"} }
func registerTo(owner string, child rowTerm) rowTerm {
	return rowTerm{op: "register", owner: owner, children: []rowTerm{child}}
}
func handleRow(label string, child rowTerm) rowTerm {
	return rowTerm{op: "handle", label: label, children: []rowTerm{child}}
}
func drainRows(owner string, child rowTerm) rowTerm {
	return rowTerm{op: "drain", owner: owner, children: []rowTerm{child}}
}

func TestScopedRowsDoNotLoseUnawaitedOrLocallyHandledWork(t *testing.T) {
	for _, test := range []struct {
		name string
		body rowTerm
		want effectRow
	}{
		{"ignored IO child", registerTo("root", perform("IO")), row("IO")},
		{"handler around spawn cannot catch later child failure", handleRow("Failure", registerTo("root", perform("Failure"))), row("Failure")},
		{"handler inside child contains expected failure", registerTo("root", handleRow("Failure", perform("Failure"))), row()},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, err := reconstructRows(drainRows("root", test.body))
			if err != nil || len(c.Owned) != 0 || !reflect.DeepEqual(c.Now, test.want) {
				t.Fatalf("contract = %+v, %v", c, err)
			}
		})
	}
	// Consuming inner scheduling evidence does not consume outer ownership.
	inner := drainRows("inner", handleRow("Failure", registerTo("outer", perform("Failure"))))
	c, err := reconstructRows(inner)
	if err != nil || !reflect.DeepEqual(c.Owned["outer"], row("Failure")) {
		t.Fatal("foreign scope obligation disappeared", c, err)
	}
	c, err = reconstructRows(drainRows("outer", inner))
	if err != nil || !reflect.DeepEqual(c.Now, row("Failure")) {
		t.Fatal("outer drain lost failure", c, err)
	}
}

func TestScopedRowsSurviveModuleInstantiationAndRejectForgedSummaries(t *testing.T) {
	helper := registerTo("context parameter", rowTerm{op: "row parameter", label: "e"})
	contract, err := reconstructRows(helper)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(contract)
	if err != nil {
		t.Fatal(err)
	}
	var imported effectContract
	if err := json.Unmarshal(data, &imported); err != nil {
		t.Fatal(err)
	}
	// The imported callback summary is also what a stored closure/dictionary
	// slot must carry. Its owner argument is not the invoking inner context.
	call := rowTerm{op: "import", imported: &imported, rows: map[string]effectRow{"e": row("IO")}, owners: map[string]string{"context parameter": "outer"}}
	c, err := reconstructRows(drainRows("inner", call))
	if err != nil || !reflect.DeepEqual(c.Owned["outer"], row("IO")) {
		t.Fatal(c, err)
	}
	actual, _ := reconstructRows(helper)
	delete(imported.Owned, "context parameter")
	if reflect.DeepEqual(actual, imported) {
		t.Fatal("missing serialized obligation accepted")
	}
	if includes(row(), c.Owned["outer"]) {
		t.Fatal("narrow owner admitted effectful child")
	}
	if _, err := reconstructRows(registerTo("", perform("IO"))); err == nil {
		t.Fatal("missing owner accepted")
	}
	call.imported = &contract
	call.owners = nil
	if _, err := reconstructRows(call); err == nil {
		t.Fatal("unknown owner silently became current context")
	}
	call.owners = map[string]string{"context parameter": "outer"}
	call.rows = nil
	if _, err := reconstructRows(call); err == nil {
		t.Fatal("unresolved row erased")
	}
	left, right := row(), row()
	left.Labels["Fail"], right.Labels["Fail"] = "Int", "String"
	if _, err := joinRows(left, right); err == nil {
		t.Fatal("incompatible nominal effect parameters admitted")
	}
}

// B is the owner's hidden budget skolem; E is one task's residual row. The
// compiler primitive must recover B from the scoped registration facet and
// synthesize project/inject with an inclusion proof. This Go prototype passes
// those arguments explicitly; it does not implement that Fango elaboration.
type rowScope struct{ budget effectRow }
type packedJob struct {
	owner *rowScope
	drive func()
}
type rowDriver[B any] struct {
	scope    *rowScope
	current  B
	queue    []packedJob // No result-type or residual-row index on the queue.
	failures []failure[B]
}
type scopedTask[A, E any] struct {
	owner  *rowScope
	result *cell[A, E]
}

func packWork[A, E, B any](driver *rowDriver[B], required effectRow, project func(B) E, inject func(failure[E]) failure[B], action func(E) completion[A, E]) (scopedTask[A, E], packedJob, error) {
	if driver.scope == nil || !includes(driver.scope.budget, required) {
		return scopedTask[A, E]{}, packedJob{}, fmt.Errorf("effect budget not proved")
	}
	result := &cell[A, E]{}
	job := packedJob{owner: driver.scope, drive: func() {
		if result.ready {
			panic("second execution")
		}
		result.value = action(project(driver.current))
		result.ready = true
		if result.value.failed != nil {
			driver.failures = append(driver.failures, inject(*result.value.failed))
		}
	}}
	return scopedTask[A, E]{driver.scope, result}, job, nil
}

func (d *rowDriver[B]) enqueue(job packedJob) error {
	if job.owner != d.scope || job.drive == nil {
		return fmt.Errorf("foreign or malformed package")
	}
	d.queue = append(d.queue, job)
	return nil
}
func (d *rowDriver[B]) step() {
	job := d.queue[0]
	d.queue[0] = packedJob{}
	d.queue = d.queue[1:]
	job.drive()
}

type numberEvidence struct{ fail func(int) delivery }
type textEvidence struct{ fail func(string) delivery }

func adaptFailure[E, B any](f failure[E], project func(B) E) failure[B] {
	out := failure[B]{replay: func(b B) delivery { return f.replay(project(b)) }}
	for _, child := range f.suppressed {
		out.suppressed = append(out.suppressed, adaptFailure(child, project))
	}
	return out
}

func TestNullaryQueuePacksDistinctResidualRowsAndReplaysTypedFailures(t *testing.T) {
	d := &rowDriver[evidence]{scope: &rowScope{row("NumberFailure", "TextFailure")}, current: at("execution")}
	numberProject := func(e evidence) numberEvidence { return numberEvidence{e.integer} }
	numberInject := func(f failure[numberEvidence]) failure[evidence] {
		return adaptFailure(f, numberProject)
	}
	textProject := func(e evidence) textEvidence { return textEvidence{e.text} }
	textInject := func(f failure[textEvidence]) failure[evidence] {
		return adaptFailure(f, textProject)
	}
	number, nJob, err := packWork(d, row("NumberFailure"), numberProject, numberInject, func(numberEvidence) completion[int, numberEvidence] {
		return completion[int, numberEvidence]{value: 42}
	})
	if err != nil {
		t.Fatal(err)
	}
	word, sJob, err := packWork(d, row("TextFailure"), textProject, textInject, func(textEvidence) completion[string, textEvidence] {
		return completion[string, textEvidence]{failed: &failure[textEvidence]{
			replay:     func(e textEvidence) delivery { return e.fail("bad text") },
			suppressed: []failure[textEvidence]{{replay: func(e textEvidence) delivery { return e.fail("release") }}},
		}}
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range []packedJob{nJob, sJob} {
		if err := d.enqueue(job); err != nil {
			t.Fatal(err)
		}
	}
	if number.result.ready || word.result.ready {
		t.Fatal("packing executed the child")
	}
	for len(d.queue) > 0 {
		d.step()
	}
	if number.result.value.value != 42 || !number.result.ready || len(d.failures) != 1 {
		t.Fatal("heterogeneous completion failed")
	}
	// Observing this task and draining its owner use different evidence types
	// and live targets; no parent exit target was stored in the completion.
	observed := word.result.value.failed.replay(textProject(at("await")))
	drained := d.failures[0].replay(at("owner drain"))
	if observed.target != "await" || drained.target != "owner drain" || *observed.text != "bad text" || *drained.text != "bad text" {
		t.Fatal("typed replay failed")
	}
	if len(d.failures[0].suppressed) != 1 || *d.failures[0].suppressed[0].replay(at("owner drain")).text != "release" {
		t.Fatal("row injection lost nested cleanup reports")
	}
	other := &rowDriver[evidence]{scope: &rowScope{d.scope.budget}}
	if err := other.enqueue(nJob); err == nil {
		t.Fatal("same budget confused two owners")
	}
	if _, _, err := packWork(d, row("IO"), numberProject, numberInject, func(numberEvidence) completion[int, numberEvidence] { return completion[int, numberEvidence]{} }); err == nil {
		t.Fatal("unchecked effect erased by package")
	}
	unknown := row()
	unknown.Vars["e"] = true
	if _, _, err := packWork(d, unknown, numberProject, numberInject, func(numberEvidence) completion[int, numberEvidence] { return completion[int, numberEvidence]{} }); err == nil {
		t.Fatal("unknown row treated as empty")
	}
}
