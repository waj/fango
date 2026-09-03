// The runtime-ratio gate (DESIGN.md §11): fango programs vs handwritten Go
// baselines. The Go binary IS the baseline — the ratio self-calibrates per
// machine, unlike the latency gate's absolute budgets. Scalar/first-order
// target: ≤ 1.2× (§11's ratio table is the arbiter for every future
// "do we need that optimization yet?" question).
package benchmarks

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const (
	ratioSlack = 15 * time.Millisecond
	timedRuns  = 7
)

// ratioCases pairs each perf program with its handwritten-Go baseline
// package, the output both must print (a free differential check at depth),
// and its per-case ratio limit (§11: 1.2× on scalar/first-order code, 3.0×
// on ADT/list-heavy code — cons lists and pointer trees race slices and
// loops, the honest idiomatic-Go ceiling).
//
// fib gates call overhead (S3, number-generic since S5); match gates
// decision-tree/enum dispatch — its baseline is the int-enum shape §8.10's
// enum-as-int upgrade would emit, so that ratio is the upgrade's arbiter.
// sum/mapfilter/tree land with S5 (§13: the full perf suite needs lists and
// trees): generic cons lists vs slices, generic map/filter/foldr chains vs
// staged slice loops, generic tree build+fold (GC pressure) vs pointer
// structs. strcat gates per-operation string overhead at the same
// asymptotics; a strings.Builder-shaped baseline is the future arbiter for
// §8.6's builder-based derived show, once show is user-callable.
//
// Limits are measured-informed ceilings (S5 measurements in parentheses):
// where the baseline allocates like fango does, the 2–3× target holds with
// room (tree 1.13×, strcat 1.04×); where a slice replaces a cons list
// wholesale, the per-cell allocation tax is structural — sum (6.8×) and
// mapfilter (3.8×) gate at that reality plus headroom, and are the arbiters
// for any future unboxed/fused list representation (§11).
var ratioCases = []struct {
	name     string
	program  string
	baseline string
	expected string
	limit    float64
}{
	{"fib", "perf/fib.fango", "perf/baseline/fib", "9227465\n", 1.2},
	{"match", "perf/match.fango", "perf/baseline/match", "-2834052877137561537\n", 1.2},
	{"sum", "perf/sum.fango", "perf/baseline/sum", "100001000000\n", 8.0},
	{"mapfilter", "perf/mapfilter.fango", "perf/baseline/mapfilter", "26999100000\n", 4.5},
	{"tree", "perf/tree.fango", "perf/baseline/tree", "42949017600\n", 3.0},
	{"strcat", "perf/strcat.fango", "perf/baseline/strcat", "True\n", 3.0},
}

func TestRuntimeRatio(t *testing.T) {
	if testing.Short() {
		t.Skip("runtime ratio gate skipped in -short mode")
	}
	fangoCLI := buildCLI(t)

	for _, tc := range ratioCases {
		t.Run(tc.name, func(t *testing.T) {
			work := t.TempDir()

			// Build the fango binary once via the real CLI.
			fangoBin := filepath.Join(work, tc.name+"_fango")
			build := exec.Command(fangoCLI, "build", "-o", fangoBin, tc.program)
			build.Env = append(os.Environ(), "FANGO_BUILD_DIR="+filepath.Join(work, "build"))
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("fango build: %v\n%s", err, out)
			}

			// Build the handwritten baseline once.
			goBin := filepath.Join(work, tc.name+"_go")
			gobuild := exec.Command("go", "build", "-o", goBin, ".")
			gobuild.Dir = tc.baseline
			if out, err := gobuild.CombinedOutput(); err != nil {
				t.Fatalf("go build baseline: %v\n%s", err, out)
			}

			for name, bin := range map[string]string{"fango": fangoBin, "go": goBin} {
				out, err := exec.Command(bin).Output()
				if err != nil {
					t.Fatalf("%s run: %v", name, err)
				}
				if string(out) != tc.expected {
					t.Fatalf("%s printed %q, want %q", name, out, tc.expected)
				}
			}

			timeOnce := func(bin string) time.Duration {
				start := time.Now()
				if err := exec.Command(bin).Run(); err != nil {
					t.Fatal(err)
				}
				return time.Since(start)
			}

			// One warmup each, then interleaved timed runs; the MINIMUM is
			// the honest estimator for CPU-bound work — noise is strictly
			// additive.
			timeOnce(fangoBin)
			timeOnce(goBin)
			fangoMin, goMin := time.Duration(1<<62), time.Duration(1<<62)
			for range timedRuns {
				if d := timeOnce(fangoBin); d < fangoMin {
					fangoMin = d
				}
				if d := timeOnce(goBin); d < goMin {
					goMin = d
				}
			}

			ratio := float64(fangoMin) / float64(goMin)
			t.Logf("%s: fango %v, go %v — ratio %.3f (target ≤ %.1f)", tc.name, fangoMin, goMin, ratio, tc.limit)
			limit := time.Duration(float64(goMin)*tc.limit) + ratioSlack
			if fangoMin > limit {
				t.Errorf("fango %s %v exceeds %.1f× handwritten Go (%v) + %v slack",
					tc.name, fangoMin, tc.limit, goMin, ratioSlack)
			}
		})
	}
}
