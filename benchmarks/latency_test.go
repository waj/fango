// Package benchmarks holds the compile-latency gate: `fango run` medians in
// three modes (cold, warm-unchanged, warm-changed) compared against
// checked-in baselines. A >20% median regression fails, as does exceeding
// the absolute budgets from DESIGN.md §8.9. Re-record baselines with
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
	"testing"
	"time"
)

var updateBaselines = flag.Bool("update-baselines", false, "rewrite baselines/latency.json")

type baselines struct {
	ColdMs          float64 `json:"cold_ms"`
	WarmUnchangedMs float64 `json:"warm_unchanged_ms"`
	WarmChangedMs   float64 `json:"warm_changed_ms"`
}

const (
	budgetColdMs          = 3000
	budgetWarmUnchangedMs = 200
	budgetWarmChangedMs   = 500
	regressionFactor      = 1.2
	// Absolute slack under the regression check: at small medians (a warm
	// no-op run is ~15ms) scheduler noise alone exceeds 20%, so a pure
	// percentage gate flakes. Real regressions we care about are bigger
	// than this.
	regressionSlackMs = 30
)

func TestCompileLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("latency gate skipped in -short mode")
	}

	fangoBin := buildCLI(t)
	work := t.TempDir()
	entry := filepath.Join(work, "hello.fango")
	writeProgram(t, entry, 3)

	runOnce := func() time.Duration {
		start := time.Now()
		cmd := exec.Command(fangoBin, "run", entry)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("fango run failed: %v\n%s", err, out)
		}
		return time.Since(start)
	}

	cold := medianMs(3, func() time.Duration {
		if err := os.RemoveAll(filepath.Join(work, ".fango")); err != nil {
			t.Fatal(err)
		}
		return runOnce()
	})

	runOnce() // prime
	warmUnchanged := medianMs(5, runOnce)

	lit := 4
	warmChanged := medianMs(5, func() time.Duration {
		writeProgram(t, entry, lit)
		lit++
		return runOnce()
	})

	t.Logf("cold=%.0fms (budget %dms)  warm-unchanged=%.0fms (budget %dms)  warm-changed=%.0fms (budget %dms)",
		cold, budgetColdMs, warmUnchanged, budgetWarmUnchangedMs, warmChanged, budgetWarmChangedMs)

	path := filepath.Join("baselines", "latency.json")
	if *updateBaselines {
		data, _ := json.MarshalIndent(baselines{cold, warmUnchanged, warmChanged}, "", "  ")
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

func medianMs(n int, f func() time.Duration) float64 {
	samples := make([]float64, n)
	for i := range samples {
		samples[i] = float64(f().Microseconds()) / 1000
	}
	sort.Float64s(samples)
	return samples[n/2]
}
