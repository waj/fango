package fangort

import "testing"

func TestCharBoundaryValidation(t *testing.T) {
	if RequireValidChar("ok", 'λ') != 'λ' {
		t.Fatal("valid Unicode rejected")
	}
	for name, invalid := range map[string]func(){
		"char": func() { RequireValidChar("bad", rune(0xD800)) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("invalid native result was accepted")
				}
			}()
			invalid()
		})
	}
}

// A cleanup scope forwards an exit it did not raise, so recording a release
// failure against it must not edit the request the raising frame still holds.
func TestSuppressCopiesRatherThanEditingThePrimaryExit(t *testing.T) {
	target := &ExitTarget{Marker: 1}
	primary := &ExitRequest{Target: target, Effect: "Fail.Fail", Operation: 0, Payload: []any{"body"}}
	inner := &ExitRequest{Target: target, Effect: "Fail.Fail", Operation: 0, Payload: []any{"inner release"}}
	outer := &ExitRequest{Target: target, Effect: "Fail.Fail", Operation: 0, Payload: []any{"outer release"}}

	once := Suppress(primary, inner)
	twice := Suppress(once, outer)

	if len(primary.Suppressed) != 0 {
		t.Fatalf("primary gained %d suppressed exits; it must be left alone", len(primary.Suppressed))
	}
	if len(once.Suppressed) != 1 || once.Suppressed[0] != inner {
		t.Fatalf("one suppression = %v, want just the inner release", once.Suppressed)
	}
	if len(twice.Suppressed) != 2 || twice.Suppressed[0] != inner || twice.Suppressed[1] != outer {
		t.Fatalf("two suppressions = %v, want inner-to-outer order", twice.Suppressed)
	}
	if twice.Target != target || twice.Payload[0] != "body" {
		t.Fatalf("suppression changed the primary exit's identity or payload: %+v", twice)
	}
	if got := Suppress(primary, nil); got != primary {
		t.Fatalf("suppressing nothing returned a copy")
	}
	if got := Suppress(nil, inner); got != inner {
		t.Fatalf("suppressing into nothing = %v, want the secondary exit", got)
	}
}
