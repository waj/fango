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
