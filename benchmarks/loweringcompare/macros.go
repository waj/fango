package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type macroObservation struct {
	Round       int
	Name, Build string
	NS          uint64
	Checksum    string
}

type macroComparison struct {
	Round                                          int
	Name                                           string
	BaselineNS, CurrentNS, Ratio, Lower95, Upper95 float64
}

// These are the existing runtime gate programs. Compare original/current
// compilers on the same source, keeping the handwritten-Go gate separate.
func compareMacros(e *evidence, samples, rounds, procs int, write func() error) error {
	programs := []struct{ name, source string }{
		{"fib", "fib"}, {"loop", "loop"}, {"match", "match"}, {"sum", "sum"},
		{"mapfilter", "mapfilter"}, {"branchcons", "branchcons"}, {"tree", "tree"},
		{"strcat", "strcat"}, {"state", "stateops"}, {"bracket", "bracket"},
	}
	e.MacroObservations = nil
	e.MacroComparisons = nil
	binaries := make([][2]string, len(programs))
	checksums := make([]string, len(programs))
	for i, p := range programs {
		var expected []byte
		for j, b := range e.Builds {
			dir := filepath.Dir(b.Dir)
			dest := filepath.Join(dir, "macros", p.name)
			if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
				return err
			}
			env := []string{"GOPROXY=off", "GOTOOLCHAIN=local", "FANGO_ROOT=" + dir, "FANGO_BUILD_DIR=" + b.Dir, fmt.Sprintf("GOMAXPROCS=%d", procs)}
			if _, err := command(dir, env, filepath.Join(dir, "compiler"), "build", "-o", dest, "benchmarks/perf/"+p.source+".fango"); err != nil {
				return err
			}
			got, err := command(dir, env, dest)
			if err != nil {
				return err
			}
			if j == 0 {
				expected = got
			} else if !bytes.Equal(expected, got) {
				return fmt.Errorf("macro %s checksum differs: %q vs %q", p.name, expected, got)
			}
			binaries[i][j] = dest
		}
		checksums[i] = string(expected)
	}
	for round := 1; round <= rounds; round++ {
		for pi, p := range programs {
			var values [2][]float64
			for i := range samples {
				for k := range 2 {
					j := (i + round + k) % 2
					start := time.Now()
					got, err := command(filepath.Dir(binaries[pi][j]), []string{fmt.Sprintf("GOMAXPROCS=%d", procs)}, binaries[pi][j])
					elapsed := uint64(time.Since(start))
					if err != nil {
						return err
					}
					if string(got) != checksums[pi] {
						return fmt.Errorf("macro %s checksum changed: %q", p.name, got)
					}
					e.MacroObservations = append(e.MacroObservations, macroObservation{round, p.name, e.Builds[j].Name, elapsed, string(got)})
					values[j] = append(values[j], float64(elapsed))
				}
			}
			low, high := interval(values[0], values[1])
			comparison := macroComparison{round, p.name, median(values[0]), median(values[1]), median(values[1]) / median(values[0]), low, high}
			e.MacroComparisons = append(e.MacroComparisons, comparison)
			fmt.Printf("round %d macro %-10s ratio %.3f [%.3f, %.3f]\n", round, p.name, comparison.Ratio, low, high)
			if err := write(); err != nil {
				return err
			}
		}
	}
	return nil
}
