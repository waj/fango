package native

import (
	"fmt"
	"time"
)

// The supported Instant range, one day inside years 1 through 9999 (see
// Time.Internal). A host clock outside it is broken rather than a value.
const instantMin, instantMax int64 = -62135510400, 253402214399

func ClockRead() any { return time.Now() }
func ClockSeconds(sample any) int64 {
	seconds := sample.(time.Time).Unix()
	if seconds < instantMin || seconds > instantMax {
		panic(fmt.Sprintf("system clock reads Unix second %d, outside years 1 through 9999", seconds))
	}
	return seconds
}
func ClockNanos(sample any) int64 { return int64(sample.(time.Time).Nanosecond()) }

// Sub uses the monotonic components retained in the immutable samples. The
// duration is sampled once so its scalar accessors cannot observe different
// readings. ClockRead's wall fields are irrelevant to this calculation.
func clockElapsedBetween(anchor, current time.Time) time.Duration { return current.Sub(anchor) }
func ClockElapsedRead(anchor any) any {
	return clockElapsedBetween(anchor.(time.Time), time.Now())
}
func ClockElapsedSeconds(sample any) int64 {
	duration := sample.(time.Duration)
	seconds := int64(duration / time.Second)
	if duration%time.Second < 0 {
		seconds--
	}
	return seconds
}
func ClockElapsedNanos(sample any) int64 {
	nanos := int64(sample.(time.Duration) % time.Second)
	if nanos < 0 {
		nanos += int64(time.Second)
	}
	return nanos
}

// Deterministic range failures do not introduce IO or an outer Fail handler.
// Returning a scalar keeps this terminal helper within the existing native ABI.
func ClockRangeFault(runner string) int64 {
	panic(runner + " reading outside supported instant range (0001-01-02T00:00:00Z through 9999-12-30T23:59:59.999999999Z)")
}
