package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefinitions(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range shapes() {
		if workload(s, 3) == workload(s, 4) {
			t.Fatal("build edit must change source")
		}
		for _, c := range scenarios(s) {
			if seen[c.ID] {
				t.Fatalf("duplicate %s", c.ID)
			}
			seen[c.ID] = true
			if c.Kind != "lookup" && c.Kind != "extend" && c.Kind != "go" && !strings.Contains(workload(s, 3), functionName(c)+" n =") {
				t.Fatalf("missing exported probe %s", c.ID)
			}
		}
	}
	for _, v := range []string{"0:1", "1:0", "65:1", "1:1junk", "1:1,1:1"} {
		if _, err := parseShapes(v); err == nil {
			t.Fatalf("accepted %q", v)
		}
	}
}

func TestMeasurementValidation(t *testing.T) {
	c := scenarios(shape{1, 1})[0]
	v := sample{NS: 100, Steps: 10, Checksum: 55}
	data, _ := json.Marshal(v)
	if _, err := parseMeasurement(append([]byte("MEASURE "), data...), c, 10); err != nil {
		t.Fatal(err)
	}
	v.Checksum = 10
	data, _ = json.Marshal(v)
	if _, err := parseMeasurement(append([]byte("MEASURE "), data...), c, 10); err == nil {
		t.Fatal("accepted wrong checksum")
	}
	if _, err := parseMeasurement([]byte("PASS"), c, 10); err == nil {
		t.Fatal("accepted missing sample")
	}
}

func TestComparisons(t *testing.T) {
	e := evidence{Schema: schemaVersion, Complete: true, WorkloadHash: workloadHash([]shape{{1, 1}}), Config: config{Samples: 2, Rounds: 1, Procs: 1, Shapes: []shape{{1, 1}}}, Builds: []compiler{{Name: "current"}}}
	for _, c := range scenarios(shape{1, 1}) {
		for range 2 {
			want := int64(10)
			if checksummed(c.Kind) {
				want = 55
			}
			e.Observations = append(e.Observations, observation{Build: "current", Round: 1, Case: c, Sample: sample{NS: 100, Steps: 10, Checksum: want}})
		}
	}
	for _, mode := range []string{"cold", "warm", "changed"} {
		e.BuildObservations = append(e.BuildObservations, buildObservation{Build: "current", Shape: shape{1, 1}, Mode: mode, WallNS: 100, Timing: json.RawMessage(`{"stages":[]}`)})
	}
	if err := compatible(e, e); err != nil {
		t.Fatal(err)
	}
	old := e
	old.Observations = append([]observation(nil), e.Observations...)
	for i := range old.Observations {
		old.Observations[i].Sample.NS = 200
	}
	for _, c := range compareEvidence(e, &old) {
		if c.Kind == "historical" && (c.Ratio != 0.5 || c.Lower95 != 0.5 || c.Upper95 != 0.5) {
			t.Fatalf("unexpected %+v", c)
		}
	}
	old.WorkloadHash = "changed"
	if err := compatible(old, e); err == nil {
		t.Fatal("accepted changed definitions")
	}
	old = e
	old.Complete = false
	if err := compatible(old, e); err == nil {
		t.Fatal("accepted incomplete run")
	}
	old = e
	old.Config.Procs = 2
	if err := compatible(old, e); err == nil {
		t.Fatal("accepted different procs")
	}
	lo, hi := interval([]float64{1, 2, 3}, []float64{2, 4, 6})
	if lo > 2 || hi < 2 {
		t.Fatalf("interval [%f,%f] excludes ratio", lo, hi)
	}
}

// Compiles representative generated workloads and executes every probe without
// any latency assertion. Run -short for only the deterministic tooling tests.
func TestGeneratedPrograms(t *testing.T) {
	if testing.Short() {
		t.Skip("generated-program smoke test")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	b := compiler{Name: "current", Root: root, Binary: filepath.Join(t.TempDir(), "compiler")}
	if _, err = command(root, nil, "go", "build", "-o", b.Binary, "./cmd/fango"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []shape{{1, 1}, {3, 2}, {1, 64}, {64, 1}} {
		t.Run(fmt.Sprintf("h%d-m%d", s.Handlers, s.Members), func(t *testing.T) {
			p, err := prepareIn(b, s, 1, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range scenarios(s) {
				if _, err = measure(p, c, 10, 1); err != nil {
					t.Fatalf("%s: %v", c.ID, err)
				}
			}
			if s == (shape{1, 1}) {
				observations, err := measureBuild(b, p, 0, 1)
				if err != nil {
					t.Fatal(err)
				}
				if len(observations) != 3 {
					t.Fatal("missing build modes")
				}
				for _, o := range observations {
					var stats struct {
						Cache struct{ Checked struct{ Reused, Total int } }
					}
					if err := json.Unmarshal(o.Timing, &stats); err != nil {
						t.Fatal(err)
					}
					if o.Mode == "cold" && stats.Cache.Checked.Reused != 0 {
						t.Fatal("cold build reused Fango artifacts")
					}
					if o.Mode == "warm" && (stats.Cache.Checked.Total == 0 || stats.Cache.Checked.Reused != stats.Cache.Checked.Total) {
						t.Fatal("warm build missed checked artifacts")
					}
					if o.Mode == "changed" && stats.Cache.Checked.Reused == stats.Cache.Checked.Total {
						t.Fatal("meaningful edit did not invalidate entry")
					}
				}
			}
		})
	}
}
