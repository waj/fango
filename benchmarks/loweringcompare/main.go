// Command loweringcompare measures the Go calling convention on an idle host.
// It is opt-in; correctness CI only builds this package.
package main

import (
	"archive/tar"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

//go:embed workload.fango
var workload string

//go:embed measure.txt
var harness string

type sample struct {
	NS, Bytes, Allocs uint64
	Reps              int
	Checksum          int64
}
type build struct {
	Name, Revision, Binary, Dir string
	SourceBytes, BinaryBytes    int64
	Builds                      []buildSample
}
type buildSample struct{ Cold, Warm, Changed time.Duration }
type scenario struct {
	Name string
	Size int
}
type observation struct {
	Round  int
	Case   scenario
	Build  string
	Sample sample
}
type comparison struct {
	Round                                                                               int
	Case                                                                                scenario
	Ratio, Lower95, Upper95, BaselineAllocs, CurrentAllocs, BaselineBytes, CurrentBytes float64
}
type acceptance struct {
	Round          int
	GeometricRatio float64
}
type evidence struct {
	Go, Host, Patch                                   string
	Procs                                             int
	Builds                                            []build
	Observations                                      []observation
	Comparisons                                       []comparison
	CallbackControl                                   []acceptance
	ColdBuildRatio, WarmBuildRatio, ChangedBuildRatio float64
	MacroObservations                                 []macroObservation
	MacroComparisons                                  []macroComparison
	Regressions                                       []scenario
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	out := flag.String("out", "", "new directory for source snapshots, binaries and evidence")
	baseline := flag.String("baseline", "HEAD", "baseline compiler revision")
	samples := flag.Int("samples", 7, "alternating samples per case and round")
	rounds := flag.Int("rounds", 2, "measurement rounds")
	procs := flag.Int("procs", 1, "GOMAXPROCS for builds and runs")
	macros := flag.Bool("macros", true, "compare existing macro runtime programs too")
	reuse := flag.Bool("reuse", false, "reuse -out snapshots and only repeat macro comparisons")
	profile := flag.Bool("profile", false, "also retain CPU/allocation profiles and Go compiler diagnostics")
	flag.Parse()
	if *out == "" || *samples < 1 || *rounds < 1 || *procs < 1 {
		return fmt.Errorf("require -out and positive samples, rounds and procs")
	}
	data, err := command(".", nil, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	root := strings.TrimSpace(string(data))
	dest, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	if *reuse {
		data, err := os.ReadFile(filepath.Join(dest, "results.json"))
		if err != nil {
			return err
		}
		var e evidence
		if err = json.Unmarshal(data, &e); err != nil {
			return err
		}
		if len(e.Builds) != 2 || e.Procs != *procs {
			return fmt.Errorf("incompatible existing comparison")
		}
		write := func() error {
			data, err := json.MarshalIndent(e, "", "  ")
			if err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dest, "results.json"), data, 0644)
		}
		return compareMacros(&e, *samples, *rounds, *procs, write)
	}
	if err = os.Mkdir(dest, 0755); err != nil {
		return err
	}
	patch, err := command(root, nil, "git", "diff", "--binary", "HEAD")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dest, "working-tree.patch"), patch, 0644); err != nil {
		return err
	}
	e := evidence{Go: runtime.Version(), Host: runtime.GOOS + "/" + runtime.GOARCH, Procs: *procs, Patch: "working-tree.patch"}
	for _, name := range []string{"baseline", "current"} {
		dir := filepath.Join(dest, name)
		if err = os.Mkdir(dir, 0755); err != nil {
			return err
		}
		ref := *baseline
		if name == "current" {
			ref = "HEAD"
			err = snapshot(root, dir)
		} else {
			err = archive(root, ref, dir)
		}
		if err != nil {
			return err
		}
		revision, err := command(root, nil, "git", "rev-parse", ref)
		if err != nil {
			return err
		}
		entryDir := filepath.Join(dir, "workload")
		if err = os.MkdirAll(entryDir, 0755); err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(entryDir, "probe.fango"), []byte(workload), 0644); err != nil {
			return err
		}
		cli := filepath.Join(dir, "compiler")
		env := []string{"GOPROXY=off", "GOTOOLCHAIN=local", "FANGO_ROOT=" + dir, fmt.Sprintf("GOMAXPROCS=%d", *procs)}
		if _, err = command(dir, env, "go", "build", "-o", cli, "./cmd/fango"); err != nil {
			return err
		}
		generated := filepath.Join(dir, "generated")
		env = append(env, "FANGO_BUILD_DIR="+generated)
		b := build{Name: name, Revision: strings.TrimSpace(string(revision)), Dir: generated, Binary: filepath.Join(dir, "measure")}
		if _, err = command(dir, env, cli, "build", "--emit-go", "-no-cache", "-o", generated, "workload/probe.fango"); err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(generated, "entries", "probe", "measure_test.go"), []byte(harness), 0644); err != nil {
			return err
		}
		if _, err = command(generated, env, "go", "test", "-c", "-o", b.Binary, "./entries/probe"); err != nil {
			return err
		}
		info, err := os.Stat(b.Binary)
		if err != nil {
			return err
		}
		b.BinaryBytes = info.Size()
		err = filepath.WalkDir(generated, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if !entry.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") && (strings.Contains(path, "/modules/") || strings.Contains(path, "/entries/")) {
				info, err := entry.Info()
				if err != nil {
					return err
				}
				b.SourceBytes += info.Size()
			}
			return nil
		})
		if err != nil {
			return err
		}
		if *profile {
			diagnostics, err := command(generated, env, "go", "test", "-c", "-gcflags=fangobuild/...=-m=2", "-o", filepath.Join(dir, "diagnostic"), "./entries/probe")
			if err != nil {
				return err
			}
			if err = os.WriteFile(filepath.Join(dir, "go-diagnostics.txt"), diagnostics, 0644); err != nil {
				return err
			}
		}
		e.Builds = append(e.Builds, b)
	}
	// Alternate complete builds as well as runtime samples. Cold means a cold
	// Fango cache; Go's shared machine-code cache is deliberately preserved.
	for i := range *samples {
		for k := range 2 {
			j := (i + k) % 2
			b := &e.Builds[j]
			dir := filepath.Dir(b.Dir)
			cli := filepath.Join(dir, "compiler")
			env := []string{"GOPROXY=off", "GOTOOLCHAIN=local", "FANGO_ROOT=" + dir, "FANGO_BUILD_DIR=" + b.Dir, fmt.Sprintf("GOMAXPROCS=%d", *procs)}
			entry := filepath.Join(dir, "workload", "probe.fango")
			timed := func(extra ...string) (time.Duration, error) {
				start := time.Now()
				args := append([]string{"build", "-o", filepath.Join(dir, "workload.bin")}, extra...)
				args = append(args, "workload/probe.fango")
				_, err := command(dir, env, cli, args...)
				return time.Since(start), err
			}
			var timing buildSample
			timing.Cold, err = timed("-no-cache")
			if err != nil {
				return err
			}
			timing.Warm, err = timed()
			if err != nil {
				return err
			}
			changed := strings.Replace(workload, "main() = ()", "main() = if True then () else ()", 1)
			if err = os.WriteFile(entry, []byte(changed), 0644); err != nil {
				return err
			}
			timing.Changed, err = timed()
			if err != nil {
				return err
			}
			if err = os.WriteFile(entry, []byte(workload), 0644); err != nil {
				return err
			}
			b.Builds = append(b.Builds, timing)
		}
	}

	cases := []scenario{{"fold", 1}, {"fold", 1000}, {"fold", 10000}, {"unary", 10000}, {"exit", 10000}, {"state", 10000}, {"bracket", 10000}, {"dictionary", 10000}, {"wide", 10000}, {"partial", 10000}, {"go-fold", 10000}, {"go-unary", 10000}}
	repetitions := make([]int, len(cases))
	for i, c := range cases {
		for _, b := range e.Builds {
			reps := 1
			for {
				s, err := measure(b, c, reps, *procs, "")
				if err != nil {
					return err
				}
				if s.NS >= uint64(100*time.Millisecond) {
					break
				}
				growth := min(1000, max(2, int(math.Ceil(float64(100*time.Millisecond)/float64(max(1, s.NS))))))
				if reps > 1_000_000_000/growth {
					return fmt.Errorf("calibration overflow: %s", c.Name)
				}
				reps *= growth
			}
			repetitions[i] = max(repetitions[i], reps)
		}
	}

	write := func() error {
		data, err := json.MarshalIndent(e, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dest, "results.json"), data, 0644)
	}
	for round := 1; round <= *rounds; round++ {
		for ci, c := range cases {
			values := [2][]float64{}
			allocations, allocated := [2][]float64{}, [2][]float64{}
			for i := range *samples {
				for k := range 2 {
					j := (i + round + k) % 2
					b := e.Builds[j]
					s, err := measure(b, c, repetitions[ci], *procs, "")
					if err != nil {
						return err
					}
					e.Observations = append(e.Observations, observation{round, c, b.Name, s})
					values[j] = append(values[j], float64(s.NS)/float64(s.Reps))
					allocations[j] = append(allocations[j], float64(s.Allocs)/float64(s.Reps))
					allocated[j] = append(allocated[j], float64(s.Bytes)/float64(s.Reps))
				}
			}
			ratio := median(values[1]) / median(values[0])
			low, high := interval(values[0], values[1])
			comparison := comparison{round, c, ratio, low, high, median(allocations[0]), median(allocations[1]), median(allocated[0]), median(allocated[1])}
			e.Comparisons = append(e.Comparisons, comparison)
			fmt.Printf("round %d %-10s %5d ratio %.3f [%.3f, %.3f] allocs %.1f -> %.1f bytes %.0f -> %.0f\n", round, c.Name, c.Size, ratio, low, high, comparison.BaselineAllocs, comparison.CurrentAllocs, comparison.BaselineBytes, comparison.CurrentBytes)
			if err = write(); err != nil {
				return err
			}
		}
	}
	for round := 1; round <= *rounds; round++ {
		sum, count := 0.0, 0
		for _, c := range e.Comparisons {
			if c.Round != round || c.Case.Size != 10000 {
				continue
			}
			switch c.Case.Name {
			case "fold", "unary", "exit", "state", "bracket", "wide":
				sum += math.Log(c.Ratio)
				count++
			}
		}
		ratio := math.Exp(sum / float64(count))
		e.CallbackControl = append(e.CallbackControl, acceptance{round, ratio})
		fmt.Printf("round %d callback/control geometric ratio %.3f (target <=0.900)\n", round, ratio)
	}
	for _, c := range cases {
		regressed := *rounds >= 2 && *samples >= 7
		for round := 1; round <= *rounds; round++ {
			for _, result := range e.Comparisons {
				if result.Round == round && result.Case == c {
					regressed = regressed && result.Lower95 > 1.05
				}
			}
		}
		if regressed {
			e.Regressions = append(e.Regressions, c)
		}
	}
	var cold, warm, changed [2][]float64
	for j, b := range e.Builds {
		for _, s := range b.Builds {
			cold[j] = append(cold[j], float64(s.Cold))
			warm[j] = append(warm[j], float64(s.Warm))
			changed[j] = append(changed[j], float64(s.Changed))
		}
	}
	e.ColdBuildRatio = median(cold[1]) / median(cold[0])
	e.WarmBuildRatio = median(warm[1]) / median(warm[0])
	e.ChangedBuildRatio = median(changed[1]) / median(changed[0])
	fmt.Printf("complete build ratios cold %.3f warm %.3f changed %.3f (cold/changed target <1.100)\n", e.ColdBuildRatio, e.WarmBuildRatio, e.ChangedBuildRatio)

	if *profile {
		for _, c := range []scenario{{"fold", 10000}, {"dictionary", 10000}, {"wide", 10000}} {
			for _, b := range e.Builds {
				if _, err := measure(b, c, 10000, *procs, filepath.Join(dest, b.Name, c.Name)); err != nil {
					return err
				}
			}
		}
	}
	if *macros {
		if err = compareMacros(&e, *samples, *rounds, *procs, write); err != nil {
			return err
		}
	}
	fmt.Println("Evidence:", filepath.Join(dest, "results.json"))
	return write()
}

func measure(b build, c scenario, reps, procs int, profile string) (sample, error) {
	env := []string{fmt.Sprintf("GOMAXPROCS=%d", procs), fmt.Sprintf("SIZE=%d", c.Size), fmt.Sprintf("REPS=%d", reps), "CASE=" + c.Name}
	args := []string{"-test.run=^TestMeasure$", "-test.v"}
	if profile != "" {
		args = append(args, "-test.cpuprofile="+profile+".cpu", "-test.memprofile="+profile+".mem")
	}
	out, err := command(b.Dir, env, b.Binary, args...)
	if err != nil {
		return sample{}, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "MEASURE ") {
			var s sample
			err := json.Unmarshal([]byte(strings.TrimPrefix(line, "MEASURE ")), &s)
			return s, err
		}
	}
	return sample{}, fmt.Errorf("missing measurement: %s", out)
}

func median(values []float64) float64 {
	v := append([]float64(nil), values...)
	sort.Float64s(v)
	if len(v)%2 == 1 {
		return v[len(v)/2]
	}
	return (v[len(v)/2-1] + v[len(v)/2]) / 2
}

func interval(old, current []float64) (float64, float64) {
	random := rand.New(rand.NewPCG(1, 2))
	ratios := make([]float64, 2000)
	a, b := make([]float64, len(old)), make([]float64, len(current))
	for i := range ratios {
		for j := range a {
			a[j] = old[random.IntN(len(old))]
		}
		for j := range b {
			b[j] = current[random.IntN(len(current))]
		}
		ratios[i] = median(b) / median(a)
	}
	sort.Float64s(ratios)
	return ratios[50], ratios[1949]
}

func command(dir string, env []string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s %v: %w\n%s", name, args, err, out)
	}
	return out, nil
}

func snapshot(root, dest string) error {
	data, err := command(root, nil, "git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return err
	}
	for _, p := range strings.Split(string(data), "\x00") {
		if p == "" {
			continue
		}
		// Root-level extensionless files are local CLI build products, not
		// source inputs (the tracked Makefile is explicitly retained).
		if !strings.Contains(p, "/") && filepath.Ext(p) == "" && p != "Makefile" && p != "LICENSE" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, p))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dest, p)
		if err = os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		if err = os.WriteFile(target, body, 0644); err != nil {
			return err
		}
	}
	return nil
}
func archive(root, ref, dest string) error {
	data, err := command(root, nil, "git", "archive", ref)
	if err != nil {
		return err
	}
	r := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := r.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		p := filepath.Join(dest, h.Name)
		if h.Typeflag == tar.TypeDir {
			if err = os.MkdirAll(p, 0755); err != nil {
				return err
			}
		} else if h.Typeflag == tar.TypeReg {
			if err = os.MkdirAll(filepath.Dir(p), 0755); err != nil {
				return err
			}
			body, err := io.ReadAll(r)
			if err != nil {
				return err
			}
			if err = os.WriteFile(p, body, os.FileMode(h.Mode)); err != nil {
				return err
			}
		}
	}
}
