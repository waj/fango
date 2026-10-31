package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

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

func compilerEnv(b compiler, procs int) []string {
	return []string{"GOPROXY=off", "GOTOOLCHAIN=local", "FANGO_ROOT=" + b.Root, fmt.Sprintf("GOMAXPROCS=%d", procs)}
}

func prepare(b compiler, s shape, procs int) (program, error) {
	return prepareIn(b, s, procs, filepath.Join(filepath.Dir(b.Root), fmt.Sprintf("h%d-m%d", s.Handlers, s.Members)))
}

func prepareIn(b compiler, s shape, procs int, dir string) (program, error) {
	p := program{Shape: s, Root: dir}
	p.Generated = filepath.Join(p.Root, "generated")
	p.Binary = filepath.Join(p.Root, "measure")
	if err := writeWorkload(p.Root, s, 3); err != nil {
		return p, err
	}
	env := append(compilerEnv(b, procs), "FANGO_BUILD_DIR="+p.Generated)
	if _, err := command(p.Root, env, b.Binary, "build", "--emit-go", "-o", p.Generated, "probe.fango"); err != nil {
		return p, err
	}
	entry := filepath.Join(p.Generated, "entries", "probe")
	if err := os.WriteFile(filepath.Join(entry, "measure_test.go"), []byte(harness(s)), 0644); err != nil {
		return p, err
	}
	if _, err := command(p.Generated, env, "go", "test", "-c", "-o", p.Binary, "./entries/probe"); err != nil {
		return p, err
	}
	info, err := os.Stat(p.Binary)
	if err != nil {
		return p, err
	}
	p.BinaryBytes = info.Size()
	err = filepath.WalkDir(p.Generated, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(p.Generated, path)
		if !strings.HasPrefix(rel, "entries/") && !strings.HasPrefix(rel, "modules/") && !strings.HasPrefix(rel, "natives/") {
			return nil
		}
		info, err := d.Info()
		if err == nil {
			p.SourceBytes += info.Size()
		}
		return err
	})
	if err != nil {
		return p, err
	}
	// Keep optimizer decisions for attribution, independently of timing.
	diagnostics, err := command(p.Generated, env, "go", "test", "-c", "-gcflags=fangobuild/...=-m=2", "-o", filepath.Join(p.Root, "diagnostic"), "./entries/probe")
	if err != nil {
		return p, err
	}
	err = os.WriteFile(filepath.Join(p.Root, "go-diagnostics.txt"), diagnostics, 0644)
	return p, err
}

func writeWorkload(dir string, s shape, literal int) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "probe.fango"), []byte(workload(s, literal)), 0644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "probe.native.go"), []byte(sidecar), 0644)
}

func measure(p program, c scenario, steps int64, procs int) (sample, error) {
	out, err := command(p.Root, []string{fmt.Sprintf("GOMAXPROCS=%d", procs), fmt.Sprintf("STEPS=%d", steps), "CASE=" + c.ID}, p.Binary, "-test.run=^TestMeasure$", "-test.v")
	if err != nil {
		return sample{}, err
	}
	return parseMeasurement(out, c, steps)
}

func parseMeasurement(out []byte, c scenario, steps int64) (sample, error) {
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "MEASURE ") {
			continue
		}
		var v sample
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "MEASURE ")), &v); err != nil {
			return v, err
		}
		want := steps
		if checksummed(c.Kind) {
			want = steps * (steps + 1) / 2
		}
		if v.NS <= 0 || v.Bytes < 0 || v.Allocs < 0 || v.Steps != steps || v.Checksum != want {
			return v, fmt.Errorf("invalid measurement/checksum for %s: %+v", c.ID, v)
		}
		return v, nil
	}
	return sample{}, fmt.Errorf("missing measurement: %s", out)
}

func measureBuild(b compiler, p program, index, procs int) ([]buildObservation, error) {
	var out []buildObservation
	build := func(dir, mode string) error {
		env := append(compilerEnv(b, procs), "FANGO_BUILD_DIR="+filepath.Join(dir, "build-generated"))
		start := time.Now()
		data, err := command(dir, env, b.Binary, "build", "-timings", "json", "-o", filepath.Join(dir, "program"), "probe.fango")
		wall := time.Since(start).Nanoseconds()
		if err != nil {
			return err
		}
		if !json.Valid(data) {
			return fmt.Errorf("compiler must support -timings json: %s", data)
		}
		if mode != "prime" {
			out = append(out, buildObservation{b.Name, p.Shape, index + 1, mode, wall, json.RawMessage(data)})
		}
		return nil
	}
	coldDir := filepath.Join(p.Root, fmt.Sprintf("cold-%d", index+1))
	if err := writeWorkload(coldDir, p.Shape, 3); err != nil {
		return nil, err
	}
	if err := build(coldDir, "cold"); err != nil {
		return nil, err
	}
	warmDir := filepath.Join(p.Root, "warm-build")
	if err := writeWorkload(warmDir, p.Shape, 3); err != nil {
		return nil, err
	}
	if err := build(warmDir, "prime"); err != nil {
		return nil, err
	}
	if err := build(warmDir, "warm"); err != nil {
		return nil, err
	}
	if err := writeWorkload(warmDir, p.Shape, index+4); err != nil {
		return nil, err
	}
	if err := build(warmDir, "changed"); err != nil {
		return nil, err
	}
	return out, nil
}

func snapshot(root, dest string) error {
	paths, err := command(root, nil, "git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return err
	}
	for _, p := range strings.Split(string(paths), "\x00") {
		if p == "" {
			continue
		}
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
		if h.Typeflag != tar.TypeReg {
			continue
		}
		if filepath.IsAbs(h.Name) || strings.HasPrefix(filepath.Clean(h.Name), "..") {
			return fmt.Errorf("unsafe archive path %q", h.Name)
		}
		target := filepath.Join(dest, h.Name)
		if err = os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		body, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		if err = os.WriteFile(target, body, 0644); err != nil {
			return err
		}
	}
}

func treeHash(root string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), len(body))
		_, err = h.Write(body)
		return err
	})
	return hex.EncodeToString(h.Sum(nil)), err
}

func hostEnvironment() environment {
	host, _ := os.Hostname()
	e := environment{Go: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH, Host: host}
	if data, err := command(".", nil, "uptime"); err == nil {
		e.Load = strings.TrimSpace(string(data))
	}
	if runtime.GOOS == "darwin" {
		if data, err := command(".", nil, "sysctl", "-n", "machdep.cpu.brand_string"); err == nil {
			e.CPU = strings.TrimSpace(string(data))
		}
	} else if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "model name") {
				e.CPU = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
				break
			}
		}
	}
	return e
}

func writeEvidence(dest string, e evidence) error {
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	// Atomic checkpoints remain parseable if an opt-in run is interrupted.
	tmp := filepath.Join(dest, "results.json.tmp")
	if err = os.WriteFile(tmp, append(data, '\n'), 0644); err != nil {
		return err
	}
	if err = os.Rename(tmp, filepath.Join(dest, "results.json")); err != nil {
		return err
	}
	return writeReport(dest, e)
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	a := append([]float64(nil), v...)
	sort.Float64s(a)
	if len(a)%2 == 1 {
		return a[len(a)/2]
	}
	return (a[len(a)/2-1] + a[len(a)/2]) / 2
}
