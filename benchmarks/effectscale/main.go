// Command effectscale measures handler scaling. Timing is opt-in, never a CI gate.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const schemaVersion = 1

type config struct {
	Samples, Rounds, Procs int
	MinNS                  int64
	Shapes                 []shape
}
type environment struct{ Go, OS, Arch, Host, CPU, Load string }
type evidence struct {
	Schema                       int
	Complete                     bool
	Started                      string
	Config                       config
	Environment                  environment
	WorkloadHash                 string
	ReferenceFile, ReferenceHash string
	Builds                       []compiler
	Observations                 []observation
	BuildObservations            []buildObservation
	Comparisons                  []comparison
	Warnings                     []string
}
type compiler struct {
	Name, Revision, SourceHash, Root, Binary string
	Programs                                 []program
}
type program struct {
	Shape                    shape
	Root, Generated, Binary  string
	SourceBytes, BinaryBytes int64
}
type sample struct{ NS, Bytes, Allocs, Steps, Checksum int64 }
type observation struct {
	Build  string
	Round  int
	Case   scenario
	Sample sample
}
type buildObservation struct {
	Build  string
	Shape  shape
	Sample int
	Mode   string
	WallNS int64
	Timing json.RawMessage
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	out := flag.String("out", "", "new evidence directory outside the checkout")
	baseline := flag.String("baseline", "", "Git revision to rebuild and alternate with current")
	compare := flag.String("compare", "", "saved results.json to compare with current")
	samples := flag.Int("samples", 7, "samples per runtime round and build mode")
	rounds := flag.Int("rounds", 2, "runtime rounds")
	procs := flag.Int("procs", 1, "GOMAXPROCS for builds and measurements")
	minimum := flag.Duration("min-time", 200*time.Millisecond, "minimum computation duration per runtime sample")
	selection := flag.String("shapes", "", "optional comma-separated handler:member shapes (default full sweep)")
	flag.Parse()
	if *out == "" || *samples < 1 || *rounds < 1 || *procs < 1 || *minimum <= 0 || (*baseline != "" && *compare != "") || flag.NArg() != 0 {
		return fmt.Errorf("require -out and positive measurement settings; -baseline and -compare are mutually exclusive")
	}
	selected, err := parseShapes(*selection)
	if err != nil {
		return err
	}
	rootBytes, err := command(".", nil, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	root := strings.TrimSpace(string(rootBytes))
	dest, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	// Resolve the existing parent too, so symlinked output cannot enter the snapshot.
	parent, err := filepath.EvalSymlinks(filepath.Dir(dest))
	if err != nil {
		return err
	}
	dest = filepath.Join(parent, filepath.Base(dest))
	if dest == root || strings.HasPrefix(dest, root+string(os.PathSeparator)) {
		return fmt.Errorf("-out must be outside the checkout")
	}
	e := evidence{Schema: schemaVersion, Started: time.Now().UTC().Format(time.RFC3339), Config: config{*samples, *rounds, *procs, int64(*minimum), selected}, Environment: hostEnvironment(), WorkloadHash: workloadHash(selected)}
	var prior *evidence
	var referenceBytes []byte
	if *compare != "" {
		data, err := os.ReadFile(*compare)
		if err != nil {
			return err
		}
		prior = &evidence{}
		if err = json.Unmarshal(data, prior); err != nil {
			return err
		}
		if err = compatible(*prior, e); err != nil {
			return err
		}
		e.Warnings = append(e.Warnings, environmentWarnings(*prior, e)...)
		referenceBytes = data
		e.ReferenceFile, e.ReferenceHash = "reference-results.json", hash(data)
	}
	if err = os.Mkdir(dest, 0755); err != nil {
		return err
	}
	if prior != nil {
		if err = os.WriteFile(filepath.Join(dest, e.ReferenceFile), referenceBytes, 0644); err != nil {
			return err
		}
	}
	if err = writeEvidence(dest, e); err != nil {
		return err
	}
	for _, name := range []string{"baseline", "current"} {
		if name == "baseline" && *baseline == "" {
			continue
		}
		b := compiler{Name: name, Root: filepath.Join(dest, name, "source")}
		if err = os.MkdirAll(b.Root, 0755); err != nil {
			return err
		}
		ref := "HEAD"
		if name == "baseline" {
			ref = *baseline
		}
		rev, err := command(root, nil, "git", "rev-parse", ref+"^{commit}")
		if err != nil {
			return err
		}
		b.Revision = strings.TrimSpace(string(rev))
		if name == "current" {
			err = snapshot(root, b.Root)
			if err == nil {
				patch, e1 := command(root, nil, "git", "diff", "--binary", "HEAD")
				if e1 != nil {
					return e1
				}
				if err = os.WriteFile(filepath.Join(dest, "working-tree.patch"), patch, 0644); err != nil {
					return err
				}
				status, e1 := command(root, nil, "git", "status", "--short")
				if e1 != nil {
					return e1
				}
				if err = os.WriteFile(filepath.Join(dest, "working-tree-status.txt"), status, 0644); err != nil {
					return err
				}
			}
		} else {
			err = archive(root, ref, b.Root)
		}
		if err != nil {
			return err
		}
		b.SourceHash, err = treeHash(b.Root)
		if err != nil {
			return err
		}
		b.Binary = filepath.Join(dest, name, "compiler")
		fmt.Printf("Building %s compiler %s\n", name, b.Revision)
		if _, err = command(b.Root, compilerEnv(b, *procs), "go", "build", "-o", b.Binary, "./cmd/fango"); err != nil {
			return err
		}
		for _, s := range selected {
			fmt.Printf("Preparing %s h%d m%d\n", name, s.Handlers, s.Members)
			p, err := prepare(b, s, *procs)
			if err != nil {
				return err
			}
			b.Programs = append(b.Programs, p)
		}
		e.Builds = append(e.Builds, b)
	}
	if err = writeEvidence(dest, e); err != nil {
		return err
	}
	// Verify every checksum before any expensive timing sweep.
	for _, b := range e.Builds {
		for _, p := range b.Programs {
			for _, c := range scenarios(p.Shape) {
				if _, err = measure(p, c, 10, *procs); err != nil {
					return err
				}
			}
		}
	}
	steps := map[string]int64{}
	for _, s := range selected {
		for _, c := range scenarios(s) {
			n := int64(1000)
			for _, b := range e.Builds {
				p := findProgram(b, s)
				for {
					v, err := measure(p, c, n, *procs)
					if err != nil {
						return err
					}
					if v.NS >= int64(*minimum) {
						break
					}
					growth := int64(math.Ceil(float64(*minimum) / float64(max(1, v.NS)) * 1.1))
					growth = max(2, min(1000, growth))
					if n > 2_000_000_000/growth {
						return fmt.Errorf("calibration exceeds checksum-safe limit: %s", c.ID)
					}
					n *= growth
				}
			}
			steps[c.ID] = n
		}
	}
	random := rand.New(rand.NewPCG(42, 7))
	var all []scenario
	for _, s := range selected {
		all = append(all, scenarios(s)...)
	}
	for round := 1; round <= *rounds; round++ {
		for sampleIndex := 0; sampleIndex < *samples; sampleIndex++ {
			for _, ci := range random.Perm(len(all)) {
				c := all[ci]
				for k := range len(e.Builds) {
					b := e.Builds[(sampleIndex+round+k)%len(e.Builds)]
					v, err := measure(findProgram(b, c.Shape), c, steps[c.ID], *procs)
					if err != nil {
						return err
					}
					e.Observations = append(e.Observations, observation{b.Name, round, c, v})
				}
			}
			fmt.Printf("Runtime round %d sample %d/%d complete (%d cases)\n", round, sampleIndex+1, *samples, len(all))
			if err = writeEvidence(dest, e); err != nil {
				return err
			}
		}
		// Repeat a variable case once, retaining the original observations too.
		// This diagnoses noise without silently selecting a favorable sample.
		for _, c := range all {
			variable := false
			for _, b := range e.Builds {
				variable = variable || unstable(runtimeValues(e, b.Name, round, c.ID))
			}
			if !variable {
				continue
			}
			e.Warnings = append(e.Warnings, fmt.Sprintf("Runtime round %d %s varied by over 30%%; repeated %d samples (originals retained)", round, c.ID, *samples))
			for i := 0; i < *samples; i++ {
				for k := range len(e.Builds) {
					b := e.Builds[(i+round+k)%len(e.Builds)]
					v, err := measure(findProgram(b, c.Shape), c, steps[c.ID], *procs)
					if err != nil {
						return err
					}
					e.Observations = append(e.Observations, observation{b.Name, round, c, v})
				}
			}
			fmt.Printf("Repeated variable round %d %s\n", round, c.ID)
			if err = writeEvidence(dest, e); err != nil {
				return err
			}
		}
	}
	for sampleIndex := 0; sampleIndex < *samples; sampleIndex++ {
		for _, si := range random.Perm(len(selected)) {
			for k := range len(e.Builds) {
				b := e.Builds[(sampleIndex+k)%len(e.Builds)]
				p := findProgram(b, selected[si])
				values, err := measureBuild(b, p, sampleIndex, *procs)
				if err != nil {
					return err
				}
				e.BuildObservations = append(e.BuildObservations, values...)
			}
		}
		fmt.Printf("Build sample %d/%d complete\n", sampleIndex+1, *samples)
		if err = writeEvidence(dest, e); err != nil {
			return err
		}
	}
	e.Complete = true
	e.Comparisons = compareEvidence(e, prior)
	if err = writeEvidence(dest, e); err != nil {
		return err
	}
	fmt.Printf("Evidence: %s\n", filepath.Join(dest, "results.json"))
	return nil
}

func parseShapes(value string) ([]shape, error) {
	if value == "" {
		return shapes(), nil
	}
	var out []shape
	seen := map[shape]bool{}
	for _, item := range strings.Split(value, ",") {
		var s shape
		if _, err := fmt.Sscanf(item, "%d:%d", &s.Handlers, &s.Members); err != nil || fmt.Sprintf("%d:%d", s.Handlers, s.Members) != item || s.Handlers < 1 || s.Members < 1 || s.Handlers > 64 || s.Members > 64 || seen[s] {
			return nil, fmt.Errorf("invalid/duplicate shape %q (use handler:member, each 1..64)", item)
		}
		seen[s] = true
		out = append(out, s)
	}
	return out, nil
}

func hash(data []byte) string { v := sha256.Sum256(data); return hex.EncodeToString(v[:]) }
func workloadHash(ss []shape) string {
	var b strings.Builder
	for _, s := range ss {
		b.WriteString(workload(s, 3))
		b.WriteString(harness(s))
	}
	b.WriteString(sidecar)
	return hash([]byte(b.String()))
}

func findProgram(b compiler, s shape) program {
	for _, p := range b.Programs {
		if p.Shape == s {
			return p
		}
	}
	panic("missing prepared shape")
}
