// Command llvmcompare compares the two backends on the existing whole-document
// typed JSON workload. Run deliberately on an idle macOS ARM64 host in Nix.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type observation struct {
	Round     int    `json:"round"`
	Backend   string `json:"backend"`
	ElapsedNS int64  `json:"elapsed_ns"`
	RSSBytes  int64  `json:"rss_bytes"`
}
type report struct {
	Host           string        `json:"host"`
	Clang          string        `json:"clang"`
	Go             string        `json:"go"`
	CompilerSHA256 string        `json:"compiler_sha256"`
	SourceSHA256   string        `json:"source_sha256"`
	InputSHA256    string        `json:"input_sha256"`
	Output         string        `json:"output"`
	Observations   []observation `json:"observations"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func command(dir, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	data, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s: %w\n%s", name, err, data)
	}
	return data, nil
}
func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func run() error {
	output := flag.String("out", "", "new evidence directory (required)")
	input := flag.String("input", "", "existing JSON input; otherwise generate")
	size := flag.Int("bytes", 10000000, "approximate generated input size")
	rounds := flag.Int("rounds", 3, "paired fresh-process runs, alternating backend order")
	flag.Parse()
	if *output == "" || *rounds < 1 || *size < 1000 {
		return fmt.Errorf("require -out, -rounds >= 1, and -bytes >= 1000")
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return fmt.Errorf("LLVM comparison currently requires macOS ARM64")
	}
	repoData, err := command("", "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	repo := strings.TrimSpace(string(repoData))
	dir, err := filepath.Abs(*output)
	if err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0755); err != nil {
		return err
	}
	source := filepath.Join(repo, "benchmarks", "jsoncompare", "workload.fango")
	compiler := filepath.Join(dir, "compiler")
	if _, err := command(repo, "go", "build", "-o", compiler, "./cmd/fango"); err != nil {
		return err
	}
	fixture := *input
	if fixture == "" {
		fixture = filepath.Join(dir, "input.json")
		if _, err := command(repo, "python3", filepath.Join(repo, "benchmarks", "jsoncompare", "generate.py"), "--bytes", fmt.Sprint(*size), "--output", fixture); err != nil {
			return err
		}
	} else {
		fixture, err = filepath.Abs(fixture)
		if err != nil {
			return err
		}
	}
	var binaries [2]string
	for i, backend := range []string{"go", "llvm"} {
		binaries[i] = filepath.Join(dir, backend)
		cmd := exec.Command(compiler, "build", "--backend", backend, "-o", binaries[i], source)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "FANGO_ROOT="+repo, "FANGO_BUILD_DIR="+filepath.Join(dir, "build-"+backend))
		if data, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s build: %w\n%s", backend, err, data)
		}
	}
	var expected []byte
	for i, binary := range binaries {
		data, err := command(repo, binary, fixture)
		if err != nil {
			return err
		}
		if i == 0 {
			expected = data
		} else if !bytes.Equal(expected, data) {
			return fmt.Errorf("backend outputs differ: Go %q; LLVM %q", expected, data)
		}
	}
	evidence := report{Host: runtime.GOOS + "/" + runtime.GOARCH, Output: string(expected)}
	clang, err := command("", "clang", "--version")
	if err != nil {
		return err
	}
	evidence.Clang = string(clang)
	goVersion, err := command("", "go", "version")
	if err != nil {
		return err
	}
	evidence.Go = string(goVersion)
	if evidence.CompilerSHA256, err = hashFile(compiler); err != nil {
		return err
	}
	if evidence.SourceSHA256, err = hashFile(source); err != nil {
		return err
	}
	if evidence.InputSHA256, err = hashFile(fixture); err != nil {
		return err
	}
	for round := 1; round <= *rounds; round++ {
		for step := range 2 {
			i := (step + round - 1) % 2
			cmd := exec.Command("/usr/bin/time", "-l", binaries[i], fixture)
			cmd.Dir = repo
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			start := time.Now()
			err := cmd.Run()
			elapsed := time.Since(start)
			if err != nil {
				return fmt.Errorf("measured run: %w\n%s", err, stderr.String())
			}
			if !bytes.Equal(stdout.Bytes(), expected) {
				return fmt.Errorf("measured run checksum differs: %q", stdout.String())
			}
			var rss int64
			for _, line := range strings.Split(stderr.String(), "\n") {
				if strings.Contains(line, "maximum resident set size") {
					if _, err := fmt.Sscanf(strings.TrimSpace(line), "%d", &rss); err != nil {
						return err
					}
				}
			}
			if rss == 0 {
				return fmt.Errorf("/usr/bin/time did not report resident memory: %s", stderr.String())
			}
			backend := []string{"go", "llvm"}[i]
			evidence.Observations = append(evidence.Observations, observation{round, backend, elapsed.Nanoseconds(), rss})
			fmt.Printf("round %d %-4s %s RSS %d bytes\n", round, backend, elapsed, rss)
		}
	}
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "evidence.json"), append(data, '\n'), 0644)
}
