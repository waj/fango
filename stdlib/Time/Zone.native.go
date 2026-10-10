package native

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"
)

type timeZone struct {
	location *time.Location
	name     string
}

type timeZoneLoad struct{ zone *timeZone }
type timeResolution struct {
	kind           int64 // unique, overlap, gap, empty
	earlier, later int64
}

var utcTimeZone = &timeZone{time.UTC, "UTC"}
var timeZoneCache sync.Map

func ZoneUTC() any { return utcTimeZone }
func ZoneFixed(seconds int64) any {
	if seconds == 0 {
		return utcTimeZone
	}
	sign, magnitude := "+", seconds
	if magnitude < 0 {
		sign, magnitude = "-", -magnitude
	}
	name := fmt.Sprintf("%s%02d:%02d", sign, magnitude/3600, magnitude/60%60)
	if magnitude%60 != 0 {
		name += fmt.Sprintf(":%02d", magnitude%60)
	}
	return &timeZone{time.FixedZone(name, int(seconds)), name}
}
func ZoneLoadRaw(name string) any {
	if name == "" || name == "Local" || strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
		return &timeZoneLoad{}
	}
	if name == "UTC" {
		return &timeZoneLoad{utcTimeZone}
	}
	if cached, ok := timeZoneCache.Load(name); ok {
		return &timeZoneLoad{cached.(*timeZone)}
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return &timeZoneLoad{}
	}
	zone, _ := timeZoneCache.LoadOrStore(name, &timeZone{location, name})
	return &timeZoneLoad{zone.(*timeZone)}
}

var (
	systemZoneOnce sync.Once
	systemZone     *timeZone
)

// ZoneSystem answers the machine's zone, detected once per process: a TZ
// name first, then the IANA name behind the /etc/localtime symlink, and
// otherwise Go's Local rules under the name "Local". A detected name shares
// the identity that loading it by name would produce.
func ZoneSystem() any {
	systemZoneOnce.Do(func() { systemZone = detectSystemZone() })
	return systemZone
}

func detectSystemZone() *timeZone {
	if name := strings.TrimPrefix(os.Getenv("TZ"), ":"); name != "" {
		if zone := ZoneLoadRaw(name).(*timeZoneLoad).zone; zone != nil {
			return zone
		}
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.LastIndex(target, "zoneinfo/"); i >= 0 {
			if zone := ZoneLoadRaw(target[i+len("zoneinfo/"):]).(*timeZoneLoad).zone; zone != nil {
				return zone
			}
		}
	}
	return &timeZone{time.Local, "Local"}
}

func ZoneLoadOK(result any) bool     { return result.(*timeZoneLoad).zone != nil }
func ZoneLoaded(result any) any      { return result.(*timeZoneLoad).zone }
func ZoneIdentifier(zone any) string { return zone.(*timeZone).name }

// ZoneOffset answers the offset at a Unix second. IANA data and the public
// fixed-offset constructor keep offsets strictly within a day; Time.Zone
// relies on that to keep every local date inside the calendar, so corrupt
// external TZif data is a trusted-native violation rather than a value.
func ZoneOffset(zone any, seconds int64) int64 {
	z := zone.(*timeZone)
	_, offset := time.Unix(seconds, 0).In(z.location).Zone()
	if offset <= -86400 || offset >= 86400 {
		panic(fmt.Sprintf("timezone %s has an offset of %d seconds at %d, outside a day", z.name, offset, seconds))
	}
	return int64(offset)
}

// IANA civil offsets and the public fixed-offset constructor are within 24
// hours. Walk every actual offset interval in the candidate window, rather
// than sampling hourly or trusting time.Date's unspecified overlap choice.
// Invalid external TZif data with offsets outside this contract is rejected
// by the resolver instead of producing an unchecked candidate.
func timeIntervals(zone *timeZone, start, finish int64, visit func(int64, int64, int64) bool) {
	for at := start; at < finish; {
		instant := time.Unix(at, 0).In(zone.location)
		_, offset := instant.Zone()
		_, end := instant.ZoneBounds()
		next := finish
		if !end.IsZero() && end.Unix() < next {
			next = end.Unix()
		}
		// Go's extrapolated POSIX rule can return the year boundary as the
		// interval end even at that boundary. Advance to its next second and
		// query again; do not loop forever or skip that second.
		if next <= at {
			next = at + 1
		}
		if !visit(at, next, int64(offset)) {
			return
		}
		at = next
	}
}

func ZoneResolve(raw any, local int64) any {
	zone := raw.(*timeZone)
	result := &timeResolution{kind: 3}
	var candidates []int64
	timeIntervals(zone, local-86400, local+86401, func(start, finish, offset int64) bool {
		if offset <= -86400 || offset >= 86400 {
			return true
		}
		candidate := local - offset
		if candidate >= start && candidate < finish {
			candidates = append(candidates, candidate)
		}
		_, after := time.Unix(finish, 0).In(zone.location).Zone()
		if int64(after) > offset && local >= finish+offset && local < finish+int64(after) {
			result = &timeResolution{2, local - int64(after), local - offset}
		}
		return true
	})
	if len(candidates) != 0 {
		lo, hi := candidates[0], candidates[0]
		for _, candidate := range candidates[1:] {
			lo, hi = min(lo, candidate), max(hi, candidate)
		}
		kind := int64(0)
		if lo != hi {
			kind = 1
		}
		return &timeResolution{kind, lo, hi}
	}
	return result
}
func ZoneResolveKind(result any) int64 { return result.(*timeResolution).kind }
func ZoneEarlier(result any) int64     { return result.(*timeResolution).earlier }
func ZoneLater(result any) int64       { return result.(*timeResolution).later }
func ZoneFirst(raw any, start, finish int64) any {
	zone := raw.(*timeZone)
	result := &timeResolution{kind: 3}
	timeIntervals(zone, start-86400, finish+86400, func(lo, hi, offset int64) bool {
		if offset <= -86400 || offset >= 86400 {
			return true
		}
		candidate := max(lo, start-offset)
		if candidate < min(hi, finish-offset) && (result.kind != 0 || candidate < result.earlier) {
			result = &timeResolution{0, candidate, candidate}
		}
		return result.kind != 0
	})
	return result
}
