// Package benchmarks holds the compile-latency gate: `fango run` medians in
// three modes (cold, warm-unchanged, warm-changed) compared against
// checked-in baselines. A >20% median regression fails, as does exceeding
// the absolute budgets in doc/design.md, "Testing and performance".
// Re-record baselines with
//
//	go test ./benchmarks -update-baselines
package benchmarks

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

var updateBaselines = flag.Bool("update-baselines", false, "rewrite baselines/latency.json")

type baselines struct {
	ColdMs          float64 `json:"cold_ms"`
	WarmUnchangedMs float64 `json:"warm_unchanged_ms"`
	WarmChangedMs   float64 `json:"warm_changed_ms"`

	// The ADT-heavy latency program (~500 lines of types and cases); see
	// doc/design.md, "Testing and performance".
	ADTColdMs          float64 `json:"adt_cold_ms"`
	ADTWarmUnchangedMs float64 `json:"adt_warm_unchanged_ms"`
	ADTWarmChangedMs   float64 `json:"adt_warm_changed_ms"`

	// The generics-heavy program (~500 lines, many distinct instantiations)
	// detects generic build-time and code-size regressions.
	PolyColdMs          float64 `json:"poly_cold_ms"`
	PolyWarmUnchangedMs float64 `json:"poly_warm_unchanged_ms"`
	PolyWarmChangedMs   float64 `json:"poly_warm_changed_ms"`
}

const (
	budgetColdMs          = 3000
	budgetWarmUnchangedMs = 200
	budgetWarmChangedMs   = 500
	// The ADT-heavy program's warm-changed budget: dominated by `go build`
	// of a ~1500-line generated main.go, legitimately above hello's 500 ms
	// (the tighter budgets apply only to the small hello program).
	budgetADTWarmChangedMs = 1000
	regressionFactor       = 1.2
	// Absolute slack under the regression check: at small medians (a warm
	// no-op run is ~15ms) scheduler noise alone exceeds 20%, so a pure
	// percentage gate flakes. Real regressions we care about are bigger
	// than this.
	regressionSlackMs = 30
)

// measure runs one program through the three modes. write(lit) rewrites the
// program with a distinguishing literal — warm-changed touches it per run.
func measure(t *testing.T, fangoBin, entry, work string, write func(lit int)) (cold, warmUnchanged, warmChanged float64) {
	t.Helper()
	write(3)
	runOnce := func() time.Duration {
		start := time.Now()
		cmd := exec.Command(fangoBin, "run", entry)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("fango run failed: %v\n%s", err, out)
		}
		return time.Since(start)
	}

	cold = medianMs(3, func() time.Duration {
		if err := os.RemoveAll(filepath.Join(work, ".fango")); err != nil {
			t.Fatal(err)
		}
		return runOnce()
	})

	runOnce() // prime
	warmUnchanged = medianMs(5, runOnce)

	lit := 4
	warmChanged = medianMs(5, func() time.Duration {
		write(lit)
		lit++
		return runOnce()
	})
	return cold, warmUnchanged, warmChanged
}

func TestCompileLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("latency gate skipped in -short mode")
	}

	fangoBin := buildCLI(t)

	helloWork := t.TempDir()
	helloEntry := filepath.Join(helloWork, "hello.fango")
	cold, warmUnchanged, warmChanged := measure(t, fangoBin, helloEntry, helloWork, func(lit int) {
		writeProgram(t, helloEntry, lit)
	})

	adtWork := t.TempDir()
	adtEntry := filepath.Join(adtWork, "adt500.fango")
	adtCold, adtWarmUnchanged, adtWarmChanged := measure(t, fangoBin, adtEntry, adtWork, func(lit int) {
		writeADTProgram(t, adtEntry, lit)
	})

	polyWork := t.TempDir()
	polyEntry := filepath.Join(polyWork, "poly500.fango")
	polyCold, polyWarmUnchanged, polyWarmChanged := measure(t, fangoBin, polyEntry, polyWork, func(lit int) {
		writePolyProgram(t, polyEntry, lit)
	})

	t.Logf("hello: cold=%.0fms warm-unchanged=%.0fms warm-changed=%.0fms  adt500: cold=%.0fms warm-unchanged=%.0fms warm-changed=%.0fms  poly500: cold=%.0fms warm-unchanged=%.0fms warm-changed=%.0fms",
		cold, warmUnchanged, warmChanged, adtCold, adtWarmUnchanged, adtWarmChanged,
		polyCold, polyWarmUnchanged, polyWarmChanged)

	path := filepath.Join("baselines", "latency.json")
	if *updateBaselines {
		data, _ := json.MarshalIndent(baselines{
			cold, warmUnchanged, warmChanged,
			adtCold, adtWarmUnchanged, adtWarmChanged,
			polyCold, polyWarmUnchanged, polyWarmChanged,
		}, "", "  ")
		if err := os.MkdirAll("baselines", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing %s (run with -update-baselines to record): %v", path, err)
	}
	var base baselines
	if err := json.Unmarshal(data, &base); err != nil {
		t.Fatal(err)
	}

	check := func(name string, got, baseline, budget float64) {
		if got > budget {
			t.Errorf("%s median %.0fms exceeds the absolute budget %.0fms", name, got, budget)
		}
		if got > baseline*regressionFactor+regressionSlackMs {
			t.Errorf("%s median %.0fms regressed >20%% vs baseline %.0fms", name, got, baseline)
		}
	}
	check("cold", cold, base.ColdMs, budgetColdMs)
	check("warm-unchanged", warmUnchanged, base.WarmUnchangedMs, budgetWarmUnchangedMs)
	check("warm-changed", warmChanged, base.WarmChangedMs, budgetWarmChangedMs)
	check("adt-cold", adtCold, base.ADTColdMs, budgetColdMs)
	check("adt-warm-unchanged", adtWarmUnchanged, base.ADTWarmUnchangedMs, budgetWarmUnchangedMs)
	check("adt-warm-changed", adtWarmChanged, base.ADTWarmChangedMs, budgetADTWarmChangedMs)
	check("poly-cold", polyCold, base.PolyColdMs, budgetColdMs)
	check("poly-warm-unchanged", polyWarmUnchanged, base.PolyWarmUnchangedMs, budgetWarmUnchangedMs)
	check("poly-warm-changed", polyWarmChanged, base.PolyWarmChangedMs, budgetADTWarmChangedMs)
}

func buildCLI(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fango")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/waj/fango/cmd/fango")
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building fango CLI: %v\n%s", err, out)
	}
	return bin
}

func writeProgram(t *testing.T, path string, lit int) {
	t.Helper()
	src := fmt.Sprintf("main = 1 + 2 * %d\n", lit)
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeADTProgram writes the ADT-heavy latency program: ~40 three-
// constructor types, a case-dense function per type, and a main folding
// them all — ~500 lines. warm-changed touches only main's literal.
func writeADTProgram(t *testing.T, path string, lit int) {
	t.Helper()
	var b strings.Builder
	const n = 40
	for i := range n {
		fmt.Fprintf(&b, "type T%d = A%d Int | B%d Int Int | C%d\n\n", i, i, i, i)
		fmt.Fprintf(&b, "f%d : T%d -> Int\n", i, i)
		fmt.Fprintf(&b, "f%d v =\n", i)
		b.WriteString("    case v of\n")
		fmt.Fprintf(&b, "        A%d x -> x + %d\n", i, i)
		fmt.Fprintf(&b, "        B%d x y -> x * y - %d\n", i, i)
		fmt.Fprintf(&b, "        C%d -> %d\n\n", i, i)
		fmt.Fprintf(&b, "g%d n =\n", i)
		b.WriteString("    case n of\n")
		fmt.Fprintf(&b, "        0 -> f%d C%d\n", i, i)
		fmt.Fprintf(&b, "        1 -> f%d (A%d n)\n", i, i)
		fmt.Fprintf(&b, "        _ -> f%d (B%d n %d)\n\n", i, i, i)
	}
	b.WriteString("total =\n")
	for i := range n {
		fmt.Fprintf(&b, "    t%d = g%d %d\n", i, i, i%3)
	}
	b.WriteString("    t0")
	for i := 1; i < n; i++ {
		fmt.Fprintf(&b, " + t%d", i)
	}
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "main = total + %d\n", lit)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writePolyProgram writes the generics-heavy latency program: ~25
// parameterized two-ctor types, a generic case function and a generic
// builder per type, and a main instantiating every one at Int, Float, and
// String — many distinct instantiations, the Go-generics build-blowup
// detector (risk #3). warm-changed touches only main's literal.
func writePolyProgram(t *testing.T, path string, lit int) {
	t.Helper()
	var b strings.Builder
	const n = 25
	for i := range n {
		fmt.Fprintf(&b, "type T%d a = A%d a | B%d a (T%d a)\n\n", i, i, i, i)
		fmt.Fprintf(&b, "depth%d : T%d a -> Int\n", i, i)
		fmt.Fprintf(&b, "depth%d v =\n", i)
		b.WriteString("    case v of\n")
		fmt.Fprintf(&b, "        A%d _ -> 1\n", i)
		fmt.Fprintf(&b, "        B%d _ rest -> 1 + depth%d rest\n\n", i, i)
		fmt.Fprintf(&b, "mk%d : Int -> a -> T%d a\n", i, i)
		fmt.Fprintf(&b, "mk%d n x = if n < 1 then A%d x else B%d x (mk%d (n - 1) x)\n\n", i, i, i, i)
	}
	b.WriteString("total =\n")
	for i := range n {
		fmt.Fprintf(&b, "    i%d = depth%d (mk%d %d 1)\n", i, i, i, i%4)
		fmt.Fprintf(&b, "    f%d = depth%d (mk%d %d 1.5)\n", i, i, i, (i+1)%4)
		fmt.Fprintf(&b, "    s%d = depth%d (mk%d %d \"s\")\n", i, i, i, (i+2)%4)
	}
	b.WriteString("    i0")
	for i := range n {
		if i > 0 {
			fmt.Fprintf(&b, " + i%d", i)
		}
		fmt.Fprintf(&b, " + f%d + s%d", i, i)
	}
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "main = total + %d\n", lit)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func medianMs(n int, f func() time.Duration) float64 {
	samples := make([]float64, n)
	for i := range samples {
		samples[i] = float64(f().Microseconds()) / 1000
	}
	sort.Float64s(samples)
	return samples[n/2]
}
