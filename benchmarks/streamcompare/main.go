// Command streamcompare is an opt-in, same-host Stream performance comparison.
// It never changes the source checkout or the repository's timing baselines.
package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
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
	"slices"
	"strconv"
	"strings"
	"time"
)

//go:embed workload.fango
var workload string

//go:embed diagnostic.fango
var diagnostic string

//go:embed Pull.fango
var pullLibrary string

//go:embed controls.txt
var goControls string

var names = []string{"fold", "cursor", "map-filter", "deep-map", "from-list", "zip", "take", "reopen", "effect-cleanup", "coroutine", "coroutine-iterator", "independent-pull", "go-runtime", "go-specialized"}

type sample struct {
	NS, Bytes, Allocs   uint64
	Steps, Frames, Rows uint64
	Repetitions         int
	Checksum            int64
}
type build struct {
	Label, Revision, Root, Binary string
	CounterBinary                 string
	Modern                        bool
}
type observation struct {
	Round, Case int
	Build       string
	Sample      sample
}
type comparison struct {
	Round                                   int
	Case, Build                             string
	Ratio, Upper95, BytesRatio, AllocsRatio float64
	Verdict                                 string
}
type report struct {
	Started                                                time.Time
	GoVersion, Platform, Hardware, SourceHash, WorkingDiff string
	N, Samples, Rounds, Procs                              int
	Builds                                                 []build
	Observations                                           []observation
	Counters                                               []observation
	Comparisons                                            []comparison
	Verdict                                                string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	out := flag.String("out", "", "new output directory (required; keeps sources, binaries and JSON evidence)")
	refs := flag.String("revisions", "0043640bcf46c6418c5ca4ee1fb84374eee5ce96,69b181b,HEAD,worktree", "comma-separated revisions; first is baseline")
	count := flag.Int("samples", 15, "samples per case and build")
	rounds := flag.Int("rounds", 2, "independent rounds")
	n := flag.Int("n", 10000, "input elements (same on every build)")
	procs := flag.Int("procs", 1, "GOMAXPROCS for all measured processes")
	cases := flag.String("cases", "", "comma-separated cases; empty runs all")
	flag.Parse()
	if *out == "" || *n < 2 || *count < 3 || *rounds < 1 || *procs < 1 {
		return fmt.Errorf("require -out, n >= 2, samples >= 3, rounds >= 1, procs >= 1")
	}
	root, err := command("", nil, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	repo := strings.TrimSpace(string(root))
	dest, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	if err = os.Mkdir(dest, 0755); err != nil {
		return err
	}
	env := append(os.Environ(), "GOCACHE="+filepath.Join(dest, "gocache"), "GOMODCACHE="+filepath.Join(dest, "gomodcache"), "GOMAXPROCS="+strconv.Itoa(*procs))
	goVersion, err := command(repo, env, "go", "version")
	if err != nil {
		return err
	}
	hardware, _ := command(repo, env, "uname", "-a")
	diff, err := command(repo, env, "git", "diff", "--binary", "HEAD")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dest, "working.patch"), diff, 0644); err != nil {
		return err
	}
	h := sha256.Sum256([]byte(workload + diagnostic + pullLibrary + goControls + harness))
	r := report{Started: time.Now(), GoVersion: strings.TrimSpace(string(goVersion)), Platform: runtime.GOOS + "/" + runtime.GOARCH, Hardware: strings.TrimSpace(string(hardware)), SourceHash: hex.EncodeToString(h[:]), WorkingDiff: "working.patch", N: *n, Samples: *count, Rounds: *rounds, Procs: *procs}
	selected := []int{}
	for i, name := range names {
		if *cases == "" || slices.Contains(strings.Split(*cases, ","), name) {
			selected = append(selected, i)
		}
	}
	if len(selected) == 0 {
		return fmt.Errorf("no selected cases")
	}
	for i, ref := range strings.Split(*refs, ",") {
		label := fmt.Sprintf("%d-%s", i, strings.ReplaceAll(ref, "/", "_"))
		b := build{Label: label, Root: filepath.Join(dest, label)}
		if err = os.Mkdir(b.Root, 0755); err != nil {
			return err
		}
		resolved := ref
		if ref == "worktree" {
			resolved = "HEAD"
		}
		rev, e := command(repo, env, "git", "rev-parse", resolved)
		if e != nil {
			return e
		}
		b.Revision = strings.TrimSpace(string(rev))
		srcRoot := filepath.Join(b.Root, "repo")
		if err = archive(repo, b.Revision, srcRoot); err != nil {
			return err
		}
		if ref == "worktree" {
			// Copy tracked working files plus untracked sources; omit ignored build artifacts.
			files, e := command(repo, env, "git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
			if e != nil {
				return e
			}
			for _, p := range strings.Split(string(files), "\x00") {
				if p == "" {
					continue
				}
				data, e := os.ReadFile(filepath.Join(repo, p))
				if os.IsNotExist(e) {
					os.Remove(filepath.Join(srcRoot, p))
					continue
				}
				if e != nil {
					return e
				}
				if e = os.MkdirAll(filepath.Dir(filepath.Join(srcRoot, p)), 0755); e != nil {
					return e
				}
				if e = os.WriteFile(filepath.Join(srcRoot, p), data, 0644); e != nil {
					return e
				}
			}
		}
		fmt.Fprintln(os.Stderr, "building", label, b.Revision)
		cli := filepath.Join(b.Root, "fango")
		if _, err = command(srcRoot, env, "go", "build", "-o", cli, "./cmd/fango"); err != nil {
			return err
		}
		_, statErr := os.Stat(filepath.Join(srcRoot, "stdlib", "Coroutine.fango"))
		if statErr == nil {
			iteratorSource, e := os.ReadFile(filepath.Join(srcRoot, "stdlib", "Iterator.fango"))
			if e != nil {
				return e
			}
			// Diagnostics exercise the C2 API, including its checked owner
			// relationships. Earlier Coroutine APIs cannot compile this Pull
			// abstraction; retain their unchanged primary Stream cases only.
			b.Modern = bytes.Contains(iteratorSource, []byte("fromCoroutine :"))
		}
		sourceText := workload
		controls := "package main\nfunc probe(which,n int64)int64{return V_run(which,n)}\n"
		if b.Modern {
			sourceText = "import Coroutine\nimport Pull\n" + workload + diagnostic
			controls = goControls
			if err = os.WriteFile(filepath.Join(b.Root, "Pull.fango"), []byte(pullLibrary), 0644); err != nil {
				return err
			}
		}
		source := filepath.Join(b.Root, "probe.fango")
		if err = os.WriteFile(source, []byte(sourceText), 0644); err != nil {
			return err
		}
		generated := filepath.Join(b.Root, "generated")
		buildEnv := append(slices.Clone(env), "FANGO_ROOT="+srcRoot)
		if _, err = command(b.Root, buildEnv, cli, "build", "--emit-go", "-o", generated, source); err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(generated, "entries", "probe", "measure_test.go"), []byte(harness), 0644); err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(generated, "entries", "probe", "controls_test.go"), []byte(controls), 0644); err != nil {
			return err
		}
		b.Binary = filepath.Join(b.Root, "measure")
		if _, err = command(generated, env, "go", "test", "-c", "-o", b.Binary, "./entries/probe"); err != nil {
			return err
		}
		if err = instrument(generated); err != nil {
			return err
		}
		b.CounterBinary = filepath.Join(b.Root, "counters")
		if _, err = command(generated, env, "go", "test", "-c", "-o", b.CounterBinary, "./entries/probe"); err != nil {
			return err
		}
		r.Builds = append(r.Builds, b)
	}
	if len(r.Builds) < 2 {
		return fmt.Errorf("need at least two builds")
	}
	for _, c := range selected {
		for _, b := range availableBuilds(r.Builds, c) {
			b.Binary = b.CounterBinary
			vars := append(slices.Clone(env), "PROBE_ALLOCS="+profileName(b, c))
			m, e := measure(b, vars, c, *n, 1)
			if e != nil {
				return e
			}
			r.Counters = append(r.Counters, observation{Case: c, Build: b.Label, Sample: m})
		}
	}
	for round := 0; round < *rounds; round++ {
		for _, c := range selected {
			available := availableBuilds(r.Builds, c)
			if len(available) < 2 {
				continue
			}
			fastest := uint64(math.MaxUint64)
			for _, b := range available {
				for range 2 {
					calibration, e := measure(b, env, c, *n, 1)
					if e != nil {
						return e
					}
					fastest = min(fastest, calibration.NS)
				}
			}
			reps := max(1, int(math.Ceil(float64(300*time.Millisecond)/float64(max(1, fastest)))))
			for _, b := range available {
				if _, e := measure(b, env, c, *n, reps); e != nil {
					return e
				}
			}
			for s := 0; s < *count; s++ {
				for j := range available {
					idx := j
					if (s+round)%2 != 0 {
						idx = len(available) - 1 - j
					}
					b := available[idx]
					m, e := measure(b, env, c, *n, reps)
					if e != nil {
						return e
					}
					r.Observations = append(r.Observations, observation{round, c, b.Label, m})
				}
			}
			fmt.Fprintln(os.Stderr, "measured round", round+1, names[c], "repetitions", reps)
			if err = save(dest, &r); err != nil {
				return err
			}
		}
	}
	r.Verdict = "pass"
	for round := 0; round < *rounds; round++ {
		for _, c := range selected {
			available := availableBuilds(r.Builds, c)
			if len(available) < 2 {
				continue
			}
			for _, b := range available[1:] {
				base := samples(r.Observations, round, c, available[0].Label)
				other := samples(r.Observations, round, c, b.Label)
				cmp := compare(base, other)
				cmp.Round = round
				cmp.Case = names[c]
				cmp.Build = b.Label
				r.Comparisons = append(r.Comparisons, cmp)
				fmt.Printf("round %d %-14s %-14s %.3fx (upper95 %.3f) bytes %.3fx allocs %.3fx %s\n", round+1, names[c], b.Label, cmp.Ratio, cmp.Upper95, cmp.BytesRatio, cmp.AllocsRatio, cmp.Verdict)
				if c < 9 && b.Label == r.Builds[len(r.Builds)-1].Label && cmp.Verdict != "pass" {
					if r.Verdict != "fail" {
						r.Verdict = cmp.Verdict
					}
				}
			}
		}
	}
	if len(selected) != len(names) || *count < 15 || *rounds < 2 {
		r.Verdict = "inconclusive (partial run)"
	}
	fmt.Println("Verdict:", r.Verdict)
	return save(dest, &r)
}

func availableBuilds(builds []build, c int) []build {
	if c < 9 {
		return builds
	}
	var out []build
	for _, b := range builds {
		if b.Modern {
			out = append(out, b)
		}
	}
	return out
}

func command(dir string, env []string, name string, args ...string) ([]byte, error) {
	c := exec.Command(name, args...)
	c.Dir = dir
	c.Env = env
	data, err := c.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s %v: %w\n%s", name, args, err, data)
	}
	return data, nil
}
func archive(repo, revision, dest string) error {
	data, err := command(repo, nil, "git", "archive", revision)
	if err != nil {
		return err
	}
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		h, e := tr.Next()
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return e
		}
		p := filepath.Join(dest, h.Name)
		switch h.Typeflag {
		case tar.TypeDir:
			if e = os.MkdirAll(p, 0755); e != nil {
				return e
			}
		case tar.TypeReg:
			if e = os.MkdirAll(filepath.Dir(p), 0755); e != nil {
				return e
			}
			f, e := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode))
			if e != nil {
				return e
			}
			_, e = io.Copy(f, tr)
			ce := f.Close()
			if e != nil {
				return e
			}
			if ce != nil {
				return ce
			}
		}
	}
}
func measure(b build, env []string, c, n, reps int) (sample, error) {
	vars := append(slices.Clone(env), fmt.Sprintf("PROBE_CASE=%d", c), fmt.Sprintf("PROBE_N=%d", n), fmt.Sprintf("PROBE_REPS=%d", reps))
	data, err := command(b.Root, vars, b.Binary, "-test.run=^TestMeasure$")
	if err != nil {
		return sample{}, err
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.HasPrefix(line, "MEASURE ") {
			var s sample
			if err = json.Unmarshal([]byte(strings.TrimPrefix(line, "MEASURE ")), &s); err != nil {
				return s, err
			}
			if s.Checksum != expected(c, int64(n)) {
				return s, fmt.Errorf("%s/%s checksum %d want %d", b.Label, names[c], s.Checksum, expected(c, int64(n)))
			}
			return s, nil
		}
	}
	return sample{}, fmt.Errorf("no measurement: %s", data)
}
func expected(c int, n int64) int64 {
	sum := n * (n + 1) / 2
	half := n / 2
	switch c {
	case 2:
		var v int64
		for x := int64(1); x <= n; x++ {
			if x+1 > half {
				v += x + 1
			}
		}
		return v
	case 3:
		return sum + 4*n
	case 5:
		return 2 * sum
	case 6:
		return half * (2*n - half + 1) / 2
	case 7:
		return 36 * n
	case 8:
		return half*(2*n-half+1)/2 + half + 1000
	}
	return sum
}
func samples(obs []observation, round, c int, label string) []sample {
	var out []sample
	for _, o := range obs {
		if o.Round == round && o.Case == c && o.Build == label {
			out = append(out, o.Sample)
		}
	}
	return out
}
func median(v []float64) float64 {
	v = slices.Clone(v)
	slices.Sort(v)
	n := len(v)
	if n%2 == 1 {
		return v[n/2]
	}
	return (v[n/2-1] + v[n/2]) / 2
}
func compare(a, b []sample) comparison {
	av, bv, ab, bb, aa, ba := []float64{}, []float64{}, []float64{}, []float64{}, []float64{}, []float64{}
	for _, s := range a {
		av = append(av, float64(s.NS)/float64(s.Repetitions))
		ab = append(ab, float64(s.Bytes)/float64(s.Repetitions))
		aa = append(aa, float64(s.Allocs)/float64(s.Repetitions))
	}
	for _, s := range b {
		bv = append(bv, float64(s.NS)/float64(s.Repetitions))
		bb = append(bb, float64(s.Bytes)/float64(s.Repetitions))
		ba = append(ba, float64(s.Allocs)/float64(s.Repetitions))
	}
	rng := rand.New(rand.NewPCG(1, 2))
	boot := make([]float64, 10000)
	for i := range boot {
		x, y := make([]float64, len(av)), make([]float64, len(bv))
		for j := range x {
			x[j] = av[rng.IntN(len(av))]
		}
		for j := range y {
			y[j] = bv[rng.IntN(len(bv))]
		}
		boot[i] = median(y) / median(x)
	}
	slices.Sort(boot)
	r := comparison{Ratio: median(bv) / median(av), Upper95: boot[9500], BytesRatio: ratio(median(bb), median(ab)), AllocsRatio: ratio(median(ba), median(aa)), Verdict: "fail"}
	if r.Ratio <= 1 && r.Upper95 <= 1.03 && r.BytesRatio <= 1 && r.AllocsRatio <= 1 {
		r.Verdict = "pass"
	} else if boot[500] <= 1 && r.BytesRatio <= 1 && r.AllocsRatio <= 1 {
		r.Verdict = "inconclusive"
	}
	return r
}
func ratio(a, b float64) float64 {
	if b == 0 {
		if a == 0 {
			return 1
		}
		return math.MaxFloat64
	}
	return a / b
}
func save(dest string, r *report) error {
	data, e := json.MarshalIndent(r, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(filepath.Join(dest, "results.json"), append(data, '\n'), 0644)
}

const harness = `package main
import("encoding/json";"fmt";"os";"runtime";"runtime/pprof";"strconv";"testing";"time")
var sink int64
var readCounters=func()(uint64,uint64,uint64){return 0,0,0}
func TestMeasure(t *testing.T){
 read:=func(k string)int{v,e:=strconv.Atoi(os.Getenv(k));if e!=nil{t.Fatal(e)};return v}
 which,n,reps:=read("PROBE_CASE"),read("PROBE_N"),read("PROBE_REPS")
	sink=probe(int64(which),int64(n));runtime.GC()
 var before,after runtime.MemStats;runtime.ReadMemStats(&before)
	s0,f0,r0:=readCounters()
	start:=time.Now();for i:=0;i<reps;i++{sink=probe(int64(which),int64(n))};elapsed:=time.Since(start)
 runtime.ReadMemStats(&after)
	s1,f1,r1:=readCounters()
 result:=struct{NS,Bytes,Allocs,Steps,Frames,Rows uint64;Repetitions int;Checksum int64}{uint64(elapsed),after.TotalAlloc-before.TotalAlloc,after.Mallocs-before.Mallocs,s1-s0,f1-f0,r1-r0,reps,sink}
 data,e:=json.Marshal(result);if e!=nil{t.Fatal(e)};fmt.Println("MEASURE",string(data))
 if path:=os.Getenv("PROBE_ALLOCS");path!=""{runtime.GC();f,e:=os.Create(path);if e!=nil{t.Fatal(e)};if e=pprof.Lookup("allocs").WriteTo(f,0);e!=nil{t.Fatal(e)};if e=f.Close();e!=nil{t.Fatal(e)}}
}
`
