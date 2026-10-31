package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

type comparison struct {
	Kind, Baseline, Current, Case string
	Round                         int
	Ratio, Lower95, Upper95       float64
}

func compatible(old, current evidence) error {
	if old.Schema != schemaVersion || !old.Complete {
		return fmt.Errorf("saved evidence must be complete and use schema %d", schemaVersion)
	}
	if old.WorkloadHash != current.WorkloadHash || !reflect.DeepEqual(old.Config.Shapes, current.Config.Shapes) {
		return fmt.Errorf("saved workload/harness definitions or selected shapes differ")
	}
	if old.Config.Procs != current.Config.Procs {
		return fmt.Errorf("saved GOMAXPROCS differs")
	}
	// Raw samples need not share repetition counts or sample counts.
	if len(old.Observations) == 0 || len(old.BuildObservations) == 0 {
		return fmt.Errorf("saved evidence has no measurements")
	}
	for _, s := range current.Config.Shapes {
		for _, c := range scenarios(s) {
			if len(runtimeValues(old, "current", 0, c.ID)) == 0 {
				return fmt.Errorf("saved evidence missing %s", c.ID)
			}
		}
		for _, mode := range []string{"cold", "warm", "changed"} {
			if len(buildValues(old, "current", s, mode)) == 0 {
				return fmt.Errorf("saved evidence missing h%d-m%d build %s", s.Handlers, s.Members, mode)
			}
		}
	}
	for _, o := range old.Observations {
		want := o.Sample.Steps
		if want < 1 || want > 2_000_000_000 {
			return fmt.Errorf("invalid saved step count")
		}
		if checksummed(o.Case.Kind) {
			want = want * (want + 1) / 2
		}
		if o.Sample.NS <= 0 || o.Sample.Bytes < 0 || o.Sample.Allocs < 0 || o.Sample.Checksum != want {
			return fmt.Errorf("invalid saved sample for %s", o.Case.ID)
		}
	}
	for _, o := range old.BuildObservations {
		if o.WallNS <= 0 || !json.Valid(o.Timing) {
			return fmt.Errorf("invalid saved build observation")
		}
	}
	return nil
}

func unstable(values []float64) bool {
	if len(values) < 2 {
		return false
	}
	low, high := values[0], values[0]
	for _, v := range values {
		low = min(low, v)
		high = max(high, v)
	}
	return low <= 0 || high/low > 1.3
}

func environmentWarnings(a, b evidence) []string {
	var out []string
	if a.Environment.Go != b.Environment.Go {
		out = append(out, "Go versions differ")
	}
	if a.Environment.OS != b.Environment.OS || a.Environment.Arch != b.Environment.Arch || a.Environment.Host != b.Environment.Host || a.Environment.CPU != b.Environment.CPU {
		out = append(out, "Host/CPU environments differ")
	}
	if a.Config.MinNS != b.Config.MinNS || a.Config.Samples != b.Config.Samples || a.Config.Rounds != b.Config.Rounds {
		out = append(out, "Sampling settings differ")
	}
	return append(out, "Saved-run comparisons are historical; host load and time can affect ratios")
}

func runtimeValues(e evidence, build string, round int, id string) []float64 {
	var values []float64
	for _, o := range e.Observations {
		if o.Build == build && (round == 0 || round == o.Round) && o.Case.ID == id {
			values = append(values, float64(o.Sample.NS)/float64(o.Sample.Steps))
		}
	}
	return values
}

func buildValues(e evidence, build string, s shape, mode string) []float64 {
	var values []float64
	for _, o := range e.BuildObservations {
		if o.Build == build && o.Shape == s && o.Mode == mode {
			values = append(values, float64(o.WallNS))
		}
	}
	return values
}

func interval(a, b []float64) (float64, float64) {
	r := rand.New(rand.NewPCG(17, 29))
	ratios := make([]float64, 2000)
	x, y := make([]float64, len(a)), make([]float64, len(b))
	for i := range ratios {
		for j := range x {
			x[j] = a[r.IntN(len(a))]
		}
		for j := range y {
			y[j] = b[r.IntN(len(b))]
		}
		ratios[i] = median(y) / median(x)
	}
	sort.Float64s(ratios)
	return ratios[50], ratios[1949]
}

func compareEvidence(e evidence, prior *evidence) []comparison {
	var out []comparison
	add := func(kind, baseline, current, id string, round int, a, b []float64) {
		if len(a) == 0 || len(b) == 0 || median(a) <= 0 {
			return
		}
		lo, hi := interval(a, b)
		out = append(out, comparison{kind, baseline, current, id, round, median(b) / median(a), lo, hi})
	}
	for _, b := range e.Builds {
		for round := 1; round <= e.Config.Rounds; round++ {
			for _, s := range e.Config.Shapes {
				for _, c := range scenarios(s) {
					base := c
					base.Shape = shape{1, 1}
					if base.Operation == "last" {
						base.Operation = "first"
					}
					base.ID = fmt.Sprintf("h1-m1/%s/%s/%s", base.Kind, base.Position, base.Operation)
					add("scaling", b.Name+":"+base.ID, b.Name, c.ID, round, runtimeValues(e, b.Name, round, base.ID), runtimeValues(e, b.Name, round, c.ID))
					if b.Name == "current" {
						add("revision", "baseline", "current", c.ID, round, runtimeValues(e, "baseline", round, c.ID), runtimeValues(e, "current", round, c.ID))
						if prior != nil {
							add("historical", "saved-current", "current", c.ID, round, runtimeValues(*prior, "current", 0, c.ID), runtimeValues(e, "current", round, c.ID))
						}
					}
				}
			}
		}
		for _, s := range e.Config.Shapes {
			for _, mode := range []string{"cold", "warm", "changed"} {
				id := fmt.Sprintf("h%d-m%d/build/%s", s.Handlers, s.Members, mode)
				add("build-scaling", b.Name+":h1-m1", b.Name, id, 0, buildValues(e, b.Name, shape{1, 1}, mode), buildValues(e, b.Name, s, mode))
				if b.Name == "current" {
					add("build-revision", "baseline", "current", id, 0, buildValues(e, "baseline", s, mode), buildValues(e, "current", s, mode))
					if prior != nil {
						add("build-historical", "saved-current", "current", id, 0, buildValues(*prior, "current", s, mode), buildValues(e, "current", s, mode))
					}
				}
			}
		}
	}
	return out
}

func writeReport(dest string, e evidence) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Effect comparison\n\nComplete: %t. %s, %s/%s, %s. GOMAXPROCS=%d.\n\n", e.Complete, e.Environment.Go, e.Environment.OS, e.Environment.Arch, e.Environment.CPU, e.Config.Procs)
	for _, w := range e.Warnings {
		fmt.Fprintf(&b, "- %s\n", w)
	}
	fmt.Fprintln(&b, "\nRuntime excludes process startup. Fango steady-state windows exclude handler setup; setup/extend count complete constructions. Allocations exclude pre-window setup. Stateful calls increment one cell and return a checksum; stateless calls are a separate workload. Cold builds use a fresh Fango cache and generated project; Go caches stay warm. Source size includes all probe variants and imported modules, excludes runtime support and test harness; binary size is the measurement executable.")
	fmt.Fprintln(&b, "| Build | Round | Case | ns/step | allocs/step | bytes/step | min/max ns/step |\n|---|---:|---|---:|---:|---:|---|")
	f, err := os.Create(filepath.Join(dest, "runtime.csv"))
	if err != nil {
		return err
	}
	csvOut := csv.NewWriter(f)
	err = csvOut.Write([]string{"build", "round", "case", "ns", "steps", "allocs", "bytes", "checksum"})
	if err != nil {
		f.Close()
		return err
	}
	for _, o := range e.Observations {
		err = csvOut.Write([]string{o.Build, fmt.Sprint(o.Round), o.Case.ID, fmt.Sprint(o.Sample.NS), fmt.Sprint(o.Sample.Steps), fmt.Sprint(o.Sample.Allocs), fmt.Sprint(o.Sample.Bytes), fmt.Sprint(o.Sample.Checksum)})
		if err != nil {
			f.Close()
			return err
		}
	}
	csvOut.Flush()
	err = csvOut.Error()
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	buildRows := [][]string{{"build", "handlers", "members", "sample", "mode", "wall_ns", "timing_json"}}
	for _, o := range e.BuildObservations {
		// Whitespace-free JSON is convenient in a single CSV cell.
		var timing any
		if err := json.Unmarshal(o.Timing, &timing); err != nil {
			return err
		}
		data, err := json.Marshal(timing)
		if err != nil {
			return err
		}
		buildRows = append(buildRows, []string{o.Build, fmt.Sprint(o.Shape.Handlers), fmt.Sprint(o.Shape.Members), fmt.Sprint(o.Sample), o.Mode, fmt.Sprint(o.WallNS), string(data)})
	}
	if err := writeCSV(filepath.Join(dest, "build.csv"), buildRows); err != nil {
		return err
	}
	for _, build := range e.Builds {
		for round := 1; round <= e.Config.Rounds; round++ {
			for _, s := range e.Config.Shapes {
				for _, c := range scenarios(s) {
					values := runtimeValues(e, build.Name, round, c.ID)
					if len(values) == 0 {
						continue
					}
					sort.Float64s(values)
					var allocs, bytes []float64
					for _, o := range e.Observations {
						if o.Build == build.Name && o.Round == round && o.Case.ID == c.ID {
							allocs = append(allocs, float64(o.Sample.Allocs)/float64(o.Sample.Steps))
							bytes = append(bytes, float64(o.Sample.Bytes)/float64(o.Sample.Steps))
						}
					}
					fmt.Fprintf(&b, "| %s | %d | %s | %.3f | %.6f | %.3f | %.3f–%.3f |\n", build.Name, round, c.ID, median(values), median(allocs), median(bytes), values[0], values[len(values)-1])
				}
			}
		}
	}
	fmt.Fprintln(&b, "\n| Build | Shape | Go bytes | Binary bytes | Cold ms | Warm ms | Changed ms |\n|---|---|---:|---:|---:|---:|---:|")
	for _, build := range e.Builds {
		for _, p := range build.Programs {
			fmt.Fprintf(&b, "| %s | h%d-m%d | %d | %d | %.1f | %.1f | %.1f |\n", build.Name, p.Shape.Handlers, p.Shape.Members, p.SourceBytes, p.BinaryBytes, median(buildValues(e, build.Name, p.Shape, "cold"))/1e6, median(buildValues(e, build.Name, p.Shape, "warm"))/1e6, median(buildValues(e, build.Name, p.Shape, "changed"))/1e6)
		}
	}
	fmt.Fprintln(&b, "\n| Build | Shape | Mode | Stage | Median ms |\n|---|---|---|---|---:|")
	for _, build := range e.Builds {
		for _, s := range e.Config.Shapes {
			for _, mode := range []string{"cold", "warm", "changed"} {
				stages := map[string][]float64{}
				for _, o := range e.BuildObservations {
					if o.Build == build.Name && o.Shape == s && o.Mode == mode {
						var timing struct {
							Stages []struct {
								Stage string
								NS    int64
							}
						}
						if err = json.Unmarshal(o.Timing, &timing); err != nil {
							return err
						}
						for _, v := range timing.Stages {
							stages[v.Stage] = append(stages[v.Stage], float64(v.NS)/1e6)
						}
					}
				}
				var names []string
				for name := range stages {
					names = append(names, name)
				}
				sort.Strings(names)
				for _, name := range names {
					fmt.Fprintf(&b, "| %s | h%d-m%d | %s | %s | %.1f |\n", build.Name, s.Handlers, s.Members, mode, name, median(stages[name]))
				}
			}
		}
	}
	fmt.Fprintln(&b, "\nRatios are current/reference; smaller is faster. Intervals resample within each runtime round; historical references pool saved rounds. Build intervals resample build samples. No automatic pass/fail thresholds.\n\n| Kind | Round | Case | Reference | Ratio | 95% interval |\n|---|---:|---|---|---:|---|")
	for _, c := range e.Comparisons {
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %.3f | %.3f–%.3f |\n", c.Kind, c.Round, c.Case, c.Baseline, c.Ratio, c.Lower95, c.Upper95)
	}
	return os.WriteFile(filepath.Join(dest, "report.md"), []byte(b.String()), 0644)
}

func writeCSV(path string, rows [][]string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	err = w.WriteAll(rows)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
