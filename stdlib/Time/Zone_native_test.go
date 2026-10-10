package native

import (
	"bytes"
	"encoding/binary"
	"sync"
	"testing"
	"time"
)

// Small controlled TZif v1 fixtures avoid depending on the host's tzdb for
// the resolution contract. Offsets and transition instants are exact seconds.
func testZone(t *testing.T, offsets []int32, transitions []int32, indices []byte) *timeZone {
	t.Helper()
	var data bytes.Buffer
	data.WriteString("TZif")
	data.Write(make([]byte, 16))
	for _, count := range []int32{0, 0, 0, int32(len(transitions)), int32(len(offsets)), 2} {
		if err := binary.Write(&data, binary.BigEndian, count); err != nil {
			t.Fatal(err)
		}
	}
	for _, at := range transitions {
		if err := binary.Write(&data, binary.BigEndian, at); err != nil {
			t.Fatal(err)
		}
	}
	data.Write(indices)
	for _, offset := range offsets {
		if err := binary.Write(&data, binary.BigEndian, offset); err != nil {
			t.Fatal(err)
		}
		data.Write([]byte{0, 0})
	}
	data.WriteString("X\x00")
	location, err := time.LoadLocationFromTZData("Test/Controlled", data.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return &timeZone{location, "Test/Controlled"}
}

func unix(t *testing.T, text string) int64 {
	t.Helper()
	value, err := time.Parse(time.RFC3339, text)
	if err != nil {
		t.Fatal(err)
	}
	return value.Unix()
}

func TestZoneResolution(t *testing.T) {
	spring := unix(t, "2024-03-10T07:00:00Z")
	autumn := unix(t, "2024-11-03T06:00:00Z")
	zone := testZone(t, []int32{-18000, -14400}, []int32{int32(spring), int32(autumn)}, []byte{1, 0})
	for _, tc := range []struct {
		local          string
		kind           int64
		earlier, later string
	}{
		{"2024-03-10T01:59:59Z", 0, "2024-03-10T06:59:59Z", "2024-03-10T06:59:59Z"},
		{"2024-03-10T02:00:00Z", 2, "2024-03-10T06:00:00Z", "2024-03-10T07:00:00Z"},
		{"2024-03-10T02:30:00Z", 2, "2024-03-10T06:30:00Z", "2024-03-10T07:30:00Z"},
		{"2024-03-10T03:00:00Z", 0, "2024-03-10T07:00:00Z", "2024-03-10T07:00:00Z"},
		{"2024-11-03T01:30:00Z", 1, "2024-11-03T05:30:00Z", "2024-11-03T06:30:00Z"},
		{"2024-11-03T02:00:00Z", 0, "2024-11-03T07:00:00Z", "2024-11-03T07:00:00Z"},
	} {
		t.Run(tc.local, func(t *testing.T) {
			got := ZoneResolve(zone, unix(t, tc.local)).(*timeResolution)
			if got.kind != tc.kind || got.earlier != unix(t, tc.earlier) || got.later != unix(t, tc.later) {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestHalfHourAndSkippedDate(t *testing.T) {
	transition := unix(t, "2024-10-05T15:30:00Z")
	half := testZone(t, []int32{37800, 39600}, []int32{int32(transition)}, []byte{1})
	got := ZoneResolve(half, unix(t, "2024-10-06T02:15:00Z")).(*timeResolution)
	if got.kind != 2 || got.later-got.earlier != 1800 || got.later != transition+900 {
		t.Fatalf("half hour: %+v", got)
	}
	transition = unix(t, "2011-12-30T10:00:00Z")
	skip := testZone(t, []int32{-36000, 50400}, []int32{int32(transition)}, []byte{1})
	start, finish := unix(t, "2011-12-30T00:00:00Z"), unix(t, "2011-12-31T00:00:00Z")
	if got := ZoneFirst(skip, start, finish).(*timeResolution); got.kind != 3 {
		t.Fatalf("skipped date: %+v", got)
	}
	if got := ZoneFirst(skip, start, finish+86400).(*timeResolution); got.kind != 0 || got.earlier != transition {
		t.Fatalf("after skipped date: %+v", got)
	}
}

func TestFirstInstantAndSecondOffsets(t *testing.T) {
	start := unix(t, "2024-01-01T00:00:00Z")
	zone := testZone(t, []int32{0, 3600}, []int32{int32(start)}, []byte{1})
	if got := ZoneFirst(zone, start, start+86400).(*timeResolution); got.kind != 0 || got.earlier != start {
		t.Fatalf("midnight gap: %+v", got)
	}
	fixed := ZoneFixed(37)
	if ZoneIdentifier(fixed) != "+00:00:37" || ZoneOffset(fixed, start) != 37 {
		t.Fatal("lost second offset")
	}
	if got := ZoneResolve(fixed, start).(*timeResolution); got.kind != 0 || got.earlier != start-37 {
		t.Fatalf("second offset: %+v", got)
	}
	if ZoneIdentifier(ZoneFixed(0)) != "UTC" {
		t.Fatal("zero offset is not UTC")
	}
}

func TestZoneLoadConcurrent(t *testing.T) {
	for _, name := range []string{"", "Local", "../UTC", "/etc/localtime", "NoSuch/TimeZone"} {
		if ZoneLoadOK(ZoneLoadRaw(name)) {
			t.Fatalf("accepted %q", name)
		}
	}
	var wg sync.WaitGroup
	values := make([]any, 32)
	for i := range values {
		wg.Add(1)
		go func() { defer wg.Done(); values[i] = ZoneLoaded(ZoneLoadRaw("America/New_York")) }()
	}
	wg.Wait()
	for _, value := range values {
		if value == (*timeZone)(nil) || value != values[0] {
			t.Fatal("load did not share immutable rules")
		}
	}
}

func TestClockSample(t *testing.T) {
	sample := ClockRead()
	rebuilt := time.Unix(ClockSeconds(sample), ClockNanos(sample))
	if !rebuilt.Equal(sample.(time.Time)) {
		t.Fatal("sample was not read atomically")
	}
}

func expectPanic(t *testing.T, what string, run func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatalf("%s did not panic", what)
		}
	}()
	run()
}

func TestTrustedContracts(t *testing.T) {
	// Time.Zone keeps every local date inside the calendar by relying on
	// offsets strictly within a day; data outside that contract is a fault.
	start := unix(t, "2024-01-01T00:00:00Z")
	wide := testZone(t, []int32{90000}, []int32{int32(start)}, []byte{0})
	expectPanic(t, "offset beyond a day", func() { ZoneOffset(wide, start) })
	if got := ZoneResolve(wide, start).(*timeResolution); got.kind != 3 {
		t.Fatalf("resolver accepted an offset beyond a day: %+v", got)
	}
	expectPanic(t, "clock beyond year 9999", func() { ClockSeconds(time.Unix(instantMax+1, 0)) })
	expectPanic(t, "clock before year 1", func() { ClockSeconds(time.Unix(instantMin-1, 0)) })
	if ClockSeconds(time.Unix(instantMax, 0)) != instantMax {
		t.Fatal("clock rejected the last supported second")
	}
}

func TestSystemZoneDetection(t *testing.T) {
	t.Setenv("TZ", "Asia/Tokyo")
	if zone := detectSystemZone(); zone.name != "Asia/Tokyo" || zone != ZoneLoaded(ZoneLoadRaw("Asia/Tokyo")) {
		t.Fatalf("TZ name not shared with a named load: %+v", zone)
	}
	t.Setenv("TZ", ":UTC")
	if zone := detectSystemZone(); zone != utcTimeZone {
		t.Fatalf("TZ=:UTC is not the UTC zone: %+v", zone)
	}
	t.Setenv("TZ", "Not/AZone")
	zone := detectSystemZone()
	if zone.name == "" || zone.location == nil {
		t.Fatalf("no fallback zone: %+v", zone)
	}
	if ZoneSystem() != ZoneSystem() {
		t.Fatal("system zone is not detected once")
	}
}
