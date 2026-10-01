package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiagnosticExpectations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.json")
	// Includes literal multibyte text, an escaped scalar, an escaped quote,
	// a surrogate pair, all punctuation, exponent notation, and whitespace.
	data := `[{"é":"a\n\"\u00e9\ud83d\ude9a","n":-1.2e+3,"t":true,"f":false,"v":null}]` + "\n"
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
	scalar, tokens, err := diagnosticExpectations(path)
	if err != nil {
		t.Fatal(err)
	}
	if tokens != "23 15 7 1 1" {
		t.Fatalf("unexpected token checksum %s", tokens)
	}
	// Source scalar checksum counts the escape spelling, not decoded JSON text.
	if scalar != "74 5762 75" {
		t.Fatalf("unexpected scalar checksum %s", scalar)
	}
	for _, bad := range []string{"{} trailing", string([]byte{'"', 0xff, '"'}), "{", `[01]`} {
		if err := os.WriteFile(path, []byte(bad), 0644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := diagnosticExpectations(path); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestBoundCellDiagnosticPreservesActivationAndState(t *testing.T) {
	project := t.TempDir()
	dir := filepath.Join(project, "modules", "Runtime", "Local")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "module.go")
	source := `package local
func adapters(){
 read := func(row Row, unit Unit) Value { return t_record0.Direct(ev25, row, unit) }
 readExit := func(row Row, unit Unit) (Value, Exit) { return t_record0.Exit(ev25.Exit, row, unit) }
 write := func(row Row, value Value) Unit { return t_record1.Direct(ev25, row, value) }
 writeExit := func(row Row, value Value) (Unit, Exit) { return t_record1.Exit(ev25.Exit, row, value) }
 dynamic := func(row Row, unit Unit) Value { return t_record0.Direct(invocationEvidence, row, unit) }
 _ = read; _ = readExit; _ = write; _ = writeExit; _ = dynamic
}
func state(){ v := cell.Snapshot(); cell.Store(v) }
`
	if err := os.WriteFile(path, []byte(source), 0644); err != nil {
		t.Fatal(err)
	}
	counts, err := transformDiagnosticProject(project, "bound-cell")
	if err != nil {
		t.Fatal(err)
	}
	if counts["bound_cell_calls"] != 4 {
		t.Fatalf("rewrite count %v", counts)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{"ev25.Op_Runtime_dot_Local_dot_readState()", "ev25.Exit.Op_Runtime_dot_Local_dot_readState()", "ev25.Op_Runtime_dot_Local_dot_writeState(value)", "ev25.Exit.Op_Runtime_dot_Local_dot_writeState(value)", "return fangort.UnitValue", "t_record0.Direct(invocationEvidence, row, unit)", "cell.Snapshot()", "cell.Store(v)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}
