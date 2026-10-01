package codegen

import (
	"bytes"
	"go/format"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

const boundOperationSource = `package p
func f(ev *Evidence) {
    var bound Fn = Fn{Direct: func(local *Evidence, row Row, value int) Unit {
        { local.Op_write(value); return fangort.UnitValue }
    }, Exit: func(local *ExitEvidence, row Row, value int) (Unit, *Exit) {
        return local.Op_write(value)
    }}
    _ = func(row Row, value int) Unit { return bound.Direct(ev, row, value) }
    _ = func(row Row, value int) (Unit, *Exit) { return bound.Exit(ev.Exit, row, value) }
}`

func lowerBoundOperation(t *testing.T, source string) string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	(&gen{}).inlineBoundOperations(file)
	var formatted bytes.Buffer
	if err := format.Node(&formatted, fset, file); err != nil {
		t.Fatal(err)
	}
	return formatted.String()
}

func TestBoundOperationForwarding(t *testing.T) {
	got := lowerBoundOperation(t, boundOperationSource)
	for _, want := range []string{"ev.Op_write(value)", "ev.Exit.Op_write(value)", "_ = bound"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{"return bound.Direct(", "return bound.Exit("} {
		if strings.Contains(got, forbidden) {
			t.Errorf("unoptimized %q in:\n%s", forbidden, got)
		}
	}
}

func TestBoundOperationForwardingUnitArgument(t *testing.T) {
	source := strings.Replace(boundOperationSource, "bound.Direct(ev, row, value)", "bound.Direct(ev, row, fangort.UnitValue)", 1)
	got := lowerBoundOperation(t, source)
	if !strings.Contains(got, "ev.Op_write(fangort.UnitValue)") {
		t.Fatalf("unit argument was not forwarded:\n%s", got)
	}
}

func TestBoundOperationForwardingDisabled(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", boundOperationSource, 0)
	if err != nil {
		t.Fatal(err)
	}
	(&gen{disableOptimizations: true}).inlineBoundOperations(file)
	var formatted bytes.Buffer
	if err := format.Node(&formatted, fset, file); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(formatted.String(), "return bound.Direct(") {
		t.Fatalf("disabled optimization changed the call:\n%s", formatted.String())
	}
}

func TestBoundOperationForwardingGuards(t *testing.T) {
	for name, altered := range map[string]string{
		"side effect":      strings.Replace(boundOperationSource, "bound.Direct(ev, row, value)", "bound.Direct(ev, row, value + 1)", 1),
		"dropped selector": strings.Replace(boundOperationSource, "bound.Direct(ev, row, value)", "bound.Direct(ev, holder.row, value)", 1),
		"reassigned":       strings.Replace(boundOperationSource, "    _ = func(row Row, value int) Unit", "    bound = other\n    _ = func(row Row, value int) Unit", 1),
		"member changed":   strings.Replace(boundOperationSource, "    _ = func(row Row, value int) Unit", "    bound.Direct = other.Direct\n    _ = func(row Row, value int) Unit", 1),
		"member addressed": strings.Replace(boundOperationSource, "    _ = func(row Row, value int) Unit", "    _ = &bound.Direct\n    _ = func(row Row, value int) Unit", 1),
		"shadowed":         strings.Replace(boundOperationSource, "    _ = func(row Row, value int) Unit", "    _ = func(bound Fn) Unit { return bound.Direct(ev, row, value) }\n    _ = func(row Row, value int) Unit", 1),
	} {
		t.Run(name, func(t *testing.T) {
			got := lowerBoundOperation(t, altered)
			if !strings.Contains(got, "return bound.Direct(") {
				t.Fatalf("unsafe call rewritten:\n%s", got)
			}
			if name == "reassigned" || name == "member changed" || name == "member addressed" || name == "shadowed" {
				if !strings.Contains(got, "return bound.Exit(") {
					t.Fatalf("unstable forwarder rewritten:\n%s", got)
				}
			}
		})
	}
}
