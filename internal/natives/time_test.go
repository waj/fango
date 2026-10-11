package natives

import "testing"

func TestClockNativeStaging(t *testing.T) {
	for name, safe := range map[string]bool{
		"clockRead":           false,
		"clockElapsedRead":    false,
		"clockSeconds":        true,
		"clockNanos":          true,
		"clockElapsedSeconds": true,
		"clockElapsedNanos":   true,
		"clockRangeFault":     true,
	} {
		spec, ok := Lookup("Time.Clock." + name)
		if !ok || spec.CompileTimeSafe != safe {
			t.Errorf("%s: registered=%t, compile-time-safe=%t, want %t", name, ok, spec.CompileTimeSafe, safe)
		}
	}
}
