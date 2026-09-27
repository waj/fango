// Command asynccompare compares cooperative scheduling with the historical
// native Async executor. Timings are opt-in and never part of correctness CI.
package main

import (
	"archive/tar"
	"bytes"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed workload.fango
var workload string

//go:embed native_workload.fango
var nativeWorkload string

const baseline = "b102a4e10bb6c4199fd5445ca9003fa42b29be02"

type sample struct {
	NS, Bytes, Allocs uint64
	Reps              int
	Checksum          int64
}
type build struct{ Name, Revision, Binary, Dir string }
type scenario struct {
	Name           string
	Workers, Steps int
	Yield          bool
}
type observation struct {
	Round  int
	Case   scenario
	Build  string
	Sample sample
}
type evidence struct {
	Go, Host     string
	Procs        int
	Builds       []build
	Observations []observation
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	out := flag.String("out", "", "new directory for snapshots, binaries and JSON evidence")
	old := flag.String("baseline", baseline, "historical Async revision")
	samples := flag.Int("samples", 7, "samples per case and round")
	rounds := flag.Int("rounds", 2, "independent rounds")
	procs := flag.Int("procs", 1, "GOMAXPROCS for both executors")
	profile := flag.Bool("profile", false, "also collect CPU and allocation profiles for 16 workers")
	flag.Parse()
	if *out == "" || *samples < 1 || *rounds < 1 || *procs < 1 {
		return fmt.Errorf("require -out and positive samples, rounds and procs")
	}
	rootData, err := command(".", nil, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	root := strings.TrimSpace(string(rootData))
	dest, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	if err = os.Mkdir(dest, 0755); err != nil {
		return err
	}
	e := evidence{Go: runtime.Version(), Host: runtime.GOOS + "/" + runtime.GOARCH, Procs: *procs}
	for _, name := range []string{"baseline", "current"} {
		dir := filepath.Join(dest, name)
		if err = os.Mkdir(dir, 0755); err != nil {
			return err
		}
		ref := *old
		if name == "current" {
			ref = "HEAD"
		}
		rev, err := command(root, nil, "git", "rev-parse", ref)
		if err != nil {
			return err
		}
		if name == "baseline" {
			err = archive(root, ref, dir)
		} else {
			err = snapshot(root, dir)
		}
		if err != nil {
			return err
		}
		fmt.Printf("Building %s (%s)\n", name, strings.TrimSpace(string(rev)))
		cli := filepath.Join(dir, "fango")
		env := []string{"FANGO_ROOT=" + dir, "GOMAXPROCS=" + strconv.Itoa(*procs)}
		if _, err = command(dir, env, "go", "build", "-o", cli, "./cmd/fango"); err != nil {
			return err
		}
		source := "import Async\nimport List\n"
		annotation := "probe : Int -> Int -> Bool -> Int\n"
		ending := "probe workers limit yielding = Async.run (\\_ -> runWorkers workers limit yielding)\n"
		if name == "current" {
			annotation = "probe : Int -> Int -> Bool ->{IO} Int\n"
			source += "import Async.Cooperative\nimport Result exposing (Result(..))\n"
			ending = "probe workers limit yielding =\n    case Async.Cooperative.run (\\_ -> runWorkers workers limit yielding) of\n        Ok total -> total\n        Err _ -> -1\n"
		}
		source += "\n" + workload + "\n" + annotation + ending + "\nmain() = print (probe 16 20000 True)\n"
		if module, err := os.ReadFile(filepath.Join(dir, "stdlib", "Async.fango")); err == nil && bytes.Contains(module, []byte("effect Async err")) {
			source = nativeWorkload
		}
		if err = os.Mkdir(filepath.Join(dir, "workload"), 0755); err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(dir, "workload", "probe.fango"), []byte(source), 0644); err != nil {
			return err
		}
		generated := filepath.Join(dir, "generated")
		env = append(env, "FANGO_BUILD_DIR="+generated)
		if _, err = command(dir, env, cli, "build", "-o", filepath.Join(dir, "probe"), "workload/probe.fango"); err != nil {
			return err
		}
		entry := filepath.Join(generated, "entries", "probe")
		if err = os.WriteFile(filepath.Join(entry, "measure_test.go"), []byte(harness), 0644); err != nil {
			return err
		}
		binary := filepath.Join(dir, "measure")
		if _, err = command(generated, env, "go", "test", "-c", "-o", binary, "./entries/probe"); err != nil {
			return err
		}
		e.Builds = append(e.Builds, build{name, strings.TrimSpace(string(rev)), binary, generated})
	}
	cases := []scenario{{"yield-1", 1, 320000, true}, {"yield-2", 2, 160000, true}, {"yield-16", 16, 20000, true}, {"yield-128", 128, 2500, true}, {"short-128", 128, 16, true}, {"no-yield-16", 16, 20000, false}}
	reps := make([]int, len(cases))
	for i, c := range cases {
		fastest := math.Inf(1)
		for _, b := range e.Builds {
			s, err := measure(b, c, 1, *procs, "")
			if err != nil {
				return err
			}
			fastest = math.Min(fastest, float64(s.NS))
		}
		reps[i] = max(1, int(math.Ceil(float64(150*time.Millisecond)/fastest)))
	}
	for round := 1; round <= *rounds; round++ {
		for ci, c := range cases {
			values := make([][]float64, 2)
			for i := 0; i < *samples; i++ {
				for k := 0; k < 2; k++ {
					j := (i + round + k) % 2
					b := e.Builds[j]
					s, err := measure(b, c, reps[ci], *procs, "")
					if err != nil {
						return err
					}
					e.Observations = append(e.Observations, observation{round, c, b.Name, s})
					values[j] = append(values[j], float64(s.NS)/float64(s.Reps))
				}
			}
			oldNS, newNS := median(values[0]), median(values[1])
			fmt.Printf("round %d %-12s old %8.3f ms  new %8.3f ms  ratio %.3f\n", round, c.Name, oldNS/1e6, newNS/1e6, newNS/oldNS)
			data, _ := json.MarshalIndent(e, "", "  ")
			if err = os.WriteFile(filepath.Join(dest, "results.json"), data, 0644); err != nil {
				return err
			}
		}
	}
	if *profile {
		for _, b := range e.Builds {
			if _, err := measure(b, cases[2], max(10, reps[2]), *procs, filepath.Join(dest, b.Name)); err != nil {
				return err
			}
		}
	}
	fmt.Println("Evidence:", filepath.Join(dest, "results.json"))
	return nil
}
func median(v []float64) float64 {
	sort.Float64s(v)
	n := len(v)
	if n%2 == 1 {
		return v[n/2]
	}
	return (v[n/2-1] + v[n/2]) / 2
}
func command(dir string, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s %v: %w\n%s", name, args, err, out)
	}
	return out, nil
}
func measure(b build, c scenario, reps, procs int, profile string) (sample, error) {
	env := []string{fmt.Sprintf("GOMAXPROCS=%d", procs), fmt.Sprintf("WORKERS=%d", c.Workers), fmt.Sprintf("STEPS=%d", c.Steps), fmt.Sprintf("REPS=%d", reps), fmt.Sprintf("YIELD=%t", c.Yield)}
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
			err = json.Unmarshal([]byte(strings.TrimPrefix(line, "MEASURE ")), &s)
			return s, err
		}
	}
	return sample{}, fmt.Errorf("missing measurement: %s", out)
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

const harness = `package main
import("encoding/json";"fmt";"os";"runtime";"strconv";"testing";"time")
var checksum int64
func TestMeasure(t *testing.T){
 read:=func(k string)int{v,e:=strconv.Atoi(os.Getenv(k));if e!=nil{t.Fatal(e)};return v}
 workers,steps,reps:=read("WORKERS"),read("STEPS"),read("REPS")
 yielding,e:=strconv.ParseBool(os.Getenv("YIELD"));if e!=nil{t.Fatal(e)}
 probe:=func()int64{return V_probe(int64(workers),int64(steps),yielding)}
 want:=int64(workers)*int64(steps);if got:=probe();got!=want{t.Fatalf("checksum %d != %d",got,want)}
 runtime.GC();var before,after runtime.MemStats;runtime.ReadMemStats(&before)
 start:=time.Now();for i:=0;i<reps;i++{checksum=probe()};elapsed:=time.Since(start)
 runtime.ReadMemStats(&after);if checksum!=want{t.Fatalf("checksum %d != %d",checksum,want)}
 result:=struct{NS,Bytes,Allocs uint64;Reps int;Checksum int64}{uint64(elapsed),after.TotalAlloc-before.TotalAlloc,after.Mallocs-before.Mallocs,reps,checksum}
 data,e:=json.Marshal(result);if e!=nil{t.Fatal(e)};fmt.Println("MEASURE",string(data))
}
`
