package codegen

import "testing"

// A module name is an identifier, but an entry program is named by its file,
// and the Go tool refuses several file names outright as a path component —
// on every host, not only Windows. Those programs compile today, so the entry
// package has to be spelled for them rather than rejected.
func TestEntryLinkNameSpellsStemsGoRefuses(t *testing.T) {
	usable := []string{"Main", "Geometry.Point", "My_Module", "calculator", "todo2"}
	for _, stem := range usable {
		if got := EntryLinkName(stem); got != stem {
			t.Errorf("EntryLinkName(%q) = %q, want it used as it is", stem, got)
		}
	}

	// A device name, a name Go reads as a short-name backup, a character Go
	// rejects outright, a device name ahead of the first dot, and a stem that
	// survives nothing of itself.
	seen := map[string]string{}
	for _, stem := range append([]string{"aux", "COM1", "report~1", "Main (copy)", "aux.Point", "...", "", "a-b"}, usable...) {
		got := EntryLinkName(stem)
		if !usablePathComponent(got) {
			t.Errorf("EntryLinkName(%q) = %q, which Go would refuse too", stem, got)
		}
		if other, clash := seen[got]; clash {
			t.Errorf("EntryLinkName(%q) and EntryLinkName(%q) both give %q", stem, other, got)
		}
		seen[got] = stem
	}
}

func TestUnitPathGivesEachProgramItsOwnPackage(t *testing.T) {
	first := UnitPath(Unit{Name: "", Program: "calculator", Entry: true})
	second := UnitPath(Unit{Name: "todo", Program: "todo", Entry: true})
	if first == second {
		t.Fatalf("two programs share the generated path %q", first)
	}
	if want := "entries/calculator/main.go"; first != want {
		t.Errorf("entry path = %q, want %q", first, want)
	}
	if want := "modules/Geometry/Point/module.go"; UnitPath(Unit{Name: "Geometry.Point"}) != want {
		t.Errorf("module path = %q, want %q", UnitPath(Unit{Name: "Geometry.Point"}), want)
	}
}
