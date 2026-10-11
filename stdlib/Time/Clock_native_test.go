package native

import (
	"strings"
	"testing"
	"time"
)

func TestClockElapsedSamples(t *testing.T) {
	// Add preserves the monotonic component of Now, while Unix supplies
	// controlled samples without one. Both paths preserve exact fractions.
	for _, anchor := range []time.Time{time.Now(), time.Unix(1700000000, 123456789)} {
		for _, interval := range []time.Duration{0, time.Nanosecond, 1500 * time.Millisecond, -1500 * time.Millisecond} {
			sample := clockElapsedBetween(anchor, anchor.Add(interval))
			seconds, nanos := ClockElapsedSeconds(sample), ClockElapsedNanos(sample)
			if nanos < 0 || nanos >= int64(time.Second) {
				t.Fatalf("fraction outside normalized range: %d", nanos)
			}
			if rebuilt := time.Duration(seconds)*time.Second + time.Duration(nanos); rebuilt != interval {
				t.Fatalf("elapsed sample = %s, want %s", rebuilt, interval)
			}
		}
	}
}

func TestClockElapsedRead(t *testing.T) {
	anchor := ClockRead().(time.Time).Add(-time.Second)
	sample := ClockElapsedRead(anchor)
	if ClockElapsedSeconds(sample) < 1 {
		t.Fatal("elapsed read did not use the supplied origin")
	}
}

func TestClockRangeFault(t *testing.T) {
	defer func() {
		message, ok := recover().(string)
		if !ok || !strings.HasPrefix(message, "Clock.runStepped reading outside supported instant range") {
			t.Fatalf("unexpected range fault: %q", message)
		}
	}()
	ClockRangeFault("Clock.runStepped")
}
