package modules

import (
	"slices"
	"testing"
)

func TestInterpolationWithoutPrelude(t *testing.T) {
	d := t.TempDir()
	entry := write(t, d, "Main.fango", "{-# no-prelude #-}\nmodule Main exposing (main)\nmain = \"#{True}\"\n")
	r, errs := Load(entry)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	var owners []string
	for _, unit := range r.Units {
		owners = append(owners, unit.Name)
	}
	if !slices.Contains(owners, "Basics") || !slices.Contains(owners, "Text.Builder") {
		t.Fatalf("missing syntax dependencies: %v", owners)
	}
}
