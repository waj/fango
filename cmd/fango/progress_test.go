package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// buildVerbose runs one build in dir and returns what each stream received.
func buildVerbose(t *testing.T, entry string, args ...string) (stdout, stderr string) {
	t.Helper()
	var out, errs bytes.Buffer
	code := run(append(append([]string{"build"}, args...), "-o", filepath.Join(filepath.Dir(entry), "out"), entry), &out, &errs)
	if code != 0 {
		t.Fatalf("build %v exit %d: %s", args, code, errs.String())
	}
	return out.String(), errs.String()
}

func writeEntry(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	entry := filepath.Join(dir, "Main.fango")
	if err := os.WriteFile(entry, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return entry
}

// A cold build compiles its entry; a second one reuses it. Progress is stderr
// only, so a program's own output is never mixed with the compiler's.
func TestVerboseReportsReuse(t *testing.T) {
	entry := writeEntry(t, "main = 1\n")

	stdout, cold := buildVerbose(t, entry, "-v")
	if stdout != "" {
		t.Fatalf("build wrote to stdout: %q", stdout)
	}
	if !strings.Contains(cold, "Checking  <entry>\n") {
		t.Errorf("cold build did not report checking the entry:\n%s", cold)
	}
	if strings.Contains(cold, "(from cache)") {
		t.Errorf("cold build reused something:\n%s", cold)
	}
	if !strings.Contains(cold, "Linking  go build") {
		t.Errorf("cold build did not report linking:\n%s", cold)
	}

	stdout, warm := buildVerbose(t, entry, "-v")
	if stdout != "" {
		t.Fatalf("build wrote to stdout: %q", stdout)
	}
	if !strings.Contains(warm, "Checking  <entry> (from cache)") {
		t.Errorf("warm build did not reuse the entry:\n%s", warm)
	}
	if !strings.Contains(warm, "cached") {
		t.Errorf("warm build summary reported no reuse:\n%s", warm)
	}
}

// -no-cache compiles from source however warm the cache is.
func TestNoCacheCompilesEverything(t *testing.T) {
	entry := writeEntry(t, "main = 1\n")
	buildVerbose(t, entry, "-v")

	_, out := buildVerbose(t, entry, "-vv", "-no-cache")
	if strings.Contains(out, "(from cache)") {
		t.Errorf("-no-cache reused an artifact:\n%s", out)
	}
	if !strings.Contains(out, "disabled by -no-cache") {
		t.Errorf("-no-cache did not say so:\n%s", out)
	}
}

// The stage table accounts for the whole build: every row it prints, plus the
// unattributed remainder, adds up to the total it reports.
func TestStageTableReconciles(t *testing.T) {
	entry := writeEntry(t, "main = 1\n")
	_, out := buildVerbose(t, entry, "-vv")

	rows, total, seenTotal := 0, 0.0, false
	var sum float64
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[0] == "total" {
			total, seenTotal = parseMillis(t, fields[1]), true
			continue
		}
		if seenTotal {
			continue
		}
		if d, ok := millis(fields[len(fields)-2]); ok {
			sum += d
			rows++
		}
	}
	if !seenTotal {
		t.Fatalf("-vv printed no total:\n%s", out)
	}
	if rows == 0 {
		t.Fatalf("-vv printed no stage rows:\n%s", out)
	}
	// Rows are rounded for display, so they reconcile to within rounding
	// rather than exactly.
	if diff := sum - total; diff > 1 || diff < -1 {
		t.Errorf("stage rows sum to %.1fms but total is %.1fms:\n%s", sum, total, out)
	}
}

// A warm build reports reading artifacts it did not write.
func TestCacheBlockCountsBytes(t *testing.T) {
	entry := writeEntry(t, "main = 1\n")
	buildVerbose(t, entry, "-v")

	_, out := buildVerbose(t, entry, "-vv")
	line := ""
	for _, candidate := range strings.Split(out, "\n") {
		if strings.Contains(candidate, "checked") && strings.Contains(candidate, "read") {
			line = candidate
		}
	}
	if line == "" {
		t.Fatalf("-vv printed no checked cache line:\n%s", out)
	}
	if strings.Contains(line, "0 B read") {
		t.Errorf("warm build read nothing from cache: %q", line)
	}
	if !strings.Contains(line, "reused") {
		t.Errorf("checked cache line reports no reuse: %q", line)
	}
}

// -timings json is the same measurements in machine-readable form, and
// replaces the prose rather than adding to it.
func TestTimingsJSON(t *testing.T) {
	entry := writeEntry(t, "main = 1\n")
	_, out := buildVerbose(t, entry, "-timings", "json")
	if strings.Contains(out, "Checking") {
		t.Errorf("-timings json also printed progress:\n%s", out)
	}
	var got timings
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if got.TotalNS <= 0 {
		t.Errorf("total_ns = %d", got.TotalNS)
	}
	if len(got.Stages) == 0 {
		t.Fatalf("no stages reported: %s", out)
	}
	var measured int64
	for _, stage := range got.Stages {
		measured += stage.NS
	}
	if measured > got.TotalNS {
		t.Errorf("stages total %d ns, more than the build's %d ns", measured, got.TotalNS)
	}
}

// Without a verbosity flag the compiler stays silent on success, which is what
// every fixture comparison depends on.
func TestQuietByDefault(t *testing.T) {
	entry := writeEntry(t, "main = 1\n")
	stdout, stderr := buildVerbose(t, entry)
	if stdout != "" || stderr != "" {
		t.Errorf("default build was not quiet: stdout=%q stderr=%q", stdout, stderr)
	}
}

func millis(field string) (float64, bool) {
	switch {
	case strings.HasSuffix(field, "ms"):
		return parseFloat(strings.TrimSuffix(field, "ms")), true
	case strings.HasSuffix(field, "µs"):
		return parseFloat(strings.TrimSuffix(field, "µs")) / 1000, true
	case strings.HasSuffix(field, "s"):
		return parseFloat(strings.TrimSuffix(field, "s")) * 1000, true
	}
	return 0, false
}

func parseMillis(t *testing.T, field string) float64 {
	t.Helper()
	d, ok := millis(field)
	if !ok {
		t.Fatalf("not a duration: %q", field)
	}
	return d
}

func parseFloat(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}
