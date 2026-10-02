// Command jsoncompare measures whole-document typed JSON decoding explicitly,
// outside the ordinary correctness suite. Run from the Nix development shell.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

//go:embed workload.fango
var workload string

//go:embed workload.hs
var haskellWorkload string

//go:embed controls.txt
var controls string

//go:embed generate.py
var generator string

//go:embed profile.txt
var profile string

//go:embed state.fango
var stateProbe string

//go:embed products.fango
var productProbe string

//go:embed probe.txt
var probeTest string

type observation struct {
	Program       string  `json:"program"`
	WallSeconds   float64 `json:"wall_seconds"`
	UserSeconds   float64 `json:"user_seconds"`
	SystemSeconds float64 `json:"system_seconds"`
	PeakRSSBytes  int64   `json:"peak_rss_bytes"`
	Verified      bool    `json:"verified"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func command(dir string, env []string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	c := exec.CommandContext(ctx, name, args...)
	c.Dir, c.Env = dir, env
	out, err := c.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s %v: %w\n%s", name, args, err, out)
	}
	return out, nil
}

func run() error {
	out := flag.String("out", "", "new evidence directory (required)")
	input := flag.String("input", "", "existing JSON fixture with adjacent .meta.json; otherwise generate")
	size := flag.Int("bytes", 10000000, "approximate generated input size")
	runs := flag.Int("runs", 3, "fresh processes per program")
	warmups := flag.Int("warmups", 1, "unmeasured fresh processes per program")
	compiler := flag.String("compiler", "", "existing compiler binary; otherwise build working tree")

	aeson := flag.Bool("aeson", false, "also compare Haskell/Aeson (requires the jsoncompare Nix shell)")
	noScan := flag.Bool("no-scan", false, "obsolete: the separate streaming decoder has been removed")
	fieldOrder := flag.String("field-order", "declaration", "generated fixture's record keys: declaration or reverse")
	diagnostics := flag.Bool("diagnostics", false, "also isolate reader layers and generated state/evidence dispatch on this fixture")
	prof := flag.Bool("profile", false, "also build and run a separate instrumented Fango binary")
	probes := flag.Bool("probes", false, "also check steady-state state/product allocations")
	flag.Parse()
	if *out == "" || *runs < 1 || *warmups < 0 || *size < 1000 {
		return fmt.Errorf("require -out, -runs >= 1, -warmups >= 0 and -bytes >= 1000")
	}
	if *noScan {
		return fmt.Errorf("-no-scan is unavailable: JSON uses a single resumable parser; compare saved baseline binaries instead")
	}
	if *fieldOrder != "declaration" && *fieldOrder != "reverse" {
		return fmt.Errorf("require -field-order declaration or reverse")
	}
	if *input != "" && *fieldOrder != "declaration" {
		return fmt.Errorf("-field-order applies only to generated fixtures; omit it with -input")
	}
	repoBytes, err := command("", os.Environ(), "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	repo := strings.TrimSpace(string(repoBytes))
	dest, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	if err = os.Mkdir(dest, 0755); err != nil {
		return err
	}
	libraryRoot := repo
	env := append(os.Environ(), "FANGO_ROOT="+libraryRoot)
	for name, data := range map[string]string{"main.fango": workload, "control.go": controls, "generate.py": generator} {
		if err = os.WriteFile(filepath.Join(dest, name), []byte(data), 0644); err != nil {
			return err
		}
	}
	fixture := *input
	if fixture == "" {
		fixture = filepath.Join(dest, "input.json")
		if _, err = command(repo, env, "python3", filepath.Join(dest, "generate.py"), "--bytes", fmt.Sprint(*size), "--output", fixture, "--field-order", *fieldOrder); err != nil {
			return err
		}
	} else {
		fixture, err = filepath.Abs(fixture)
		if err != nil {
			return err
		}
	}
	metaBytes, err := os.ReadFile(strings.TrimSuffix(fixture, filepath.Ext(fixture)) + ".meta.json")
	if err != nil {
		return err
	}
	var meta struct {
		Expected map[string]int64 `json:"expected"`
		SHA256   string           `json:"sha256"`
	}
	if err = json.Unmarshal(metaBytes, &meta); err != nil {
		return err
	}
	file, err := os.Open(fixture)
	if err != nil {
		return err
	}
	digest := sha256.New()
	_, copyErr := io.Copy(digest, file)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if fmt.Sprintf("%x", digest.Sum(nil)) != meta.SHA256 {
		return fmt.Errorf("fixture SHA-256 does not match metadata")
	}
	expected := fmt.Sprintf("%d %d %d %d %d", meta.Expected["records"], meta.Expected["ids"], meta.Expected["cents"], meta.Expected["units"], meta.Expected["text_bytes"])
	compilerPath := *compiler
	if compilerPath == "" {
		compilerPath = filepath.Join(dest, "fango-compiler")
		if _, err = command(repo, env, "go", "build", "-o", compilerPath, "./cmd/fango"); err != nil {
			return err
		}
	} else {
		compilerPath, err = filepath.Abs(compilerPath)
		if err != nil {
			return err
		}
	}
	project := filepath.Join(dest, "project")
	if _, err = command(repo, env, compilerPath, "build", "--emit-go", "-o", project, filepath.Join(dest, "main.fango")); err != nil {
		return err
	}
	if _, err = command(project, env, "go", "build", "-o", filepath.Join(dest, "fango"), "./entries/main"); err != nil {
		return err
	}
	if _, err = command(repo, env, "go", "build", "-o", filepath.Join(dest, "go"), filepath.Join(dest, "control.go")); err != nil {
		return err
	}
	programs := []string{"go", "fango"}
	toolchain := map[string]string{}
	if *aeson {
		source := filepath.Join(dest, "main.hs")
		if err = os.WriteFile(source, []byte(haskellWorkload), 0644); err != nil {
			return err
		}
		if _, err = command(repo, env, "ghc", "-O2", "-Wall", "-rtsopts", "-package", "aeson", "-outputdir", filepath.Join(dest, "haskell-build"), "-o", filepath.Join(dest, "haskell-aeson"), source); err != nil {
			return err
		}
		version, e := command(repo, env, "ghc", "--numeric-version")
		if e != nil {
			return e
		}
		toolchain["ghc"] = strings.TrimSpace(string(version))
		for _, pkg := range []string{"aeson", "text", "bytestring"} {
			version, e = command(repo, env, "ghc-pkg", "field", pkg, "version", "--simple-output")
			if e != nil {
				return e
			}
			toolchain[pkg] = strings.TrimSpace(string(version))
		}
		toolchain["ghc_flags"] = "-O2 -Wall -rtsopts -package aeson"
		programs = append(programs, "haskell-aeson")
	}
	revision, _ := command(repo, env, "git", "rev-parse", "HEAD")
	diff, _ := command(repo, env, "git", "diff", "--binary", "HEAD")
	if err = os.WriteFile(filepath.Join(dest, "working.patch"), diff, 0644); err != nil {
		return err
	}
	// The patch records tracked edits; preserve new implementation files too.
	newFiles, err := command(repo, env, "git", "ls-files", "--others", "--exclude-standard", "-z", "internal", "runtime", "stdlib", "benchmarks/jsoncompare", "cmd/fango/json_codegen_test.go", "testdata/run/value_representation.*", "testdata/run/json_*", "testdata/run/meta_local_function.*", "testdata/run/tail_loop_exit.*")
	if err != nil {
		return err
	}
	for _, name := range strings.Split(string(newFiles), "\x00") {
		if name == "" {
			continue
		}
		data, e := os.ReadFile(filepath.Join(repo, name))
		if e != nil {
			return e
		}
		path := filepath.Join(dest, "new-sources", name)
		if e = os.MkdirAll(filepath.Dir(path), 0755); e != nil {
			return e
		}
		if e = os.WriteFile(path, data, 0644); e != nil {
			return e
		}
	}
	settings := map[string]string{}
	for _, key := range []string{"GOGC", "GOMEMLIMIT", "GOMAXPROCS"} {
		settings[key] = os.Getenv(key)
	}
	if *aeson {
		settings["GHCRTS"] = os.Getenv("GHCRTS")
	}
	binaryHashes := map[string]string{}
	for _, program := range programs {
		binary, e := os.ReadFile(filepath.Join(dest, program))
		if e != nil {
			return e
		}
		binaryHashes[program] = fmt.Sprintf("%x", sha256.Sum256(binary))
		for warmup := 0; warmup < *warmups; warmup++ {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			c := exec.CommandContext(ctx, filepath.Join(dest, program), fixture)
			c.Dir, c.Env = repo, env
			var stderr bytes.Buffer
			c.Stderr = &stderr
			output, e := c.Output()
			cancel()
			if e != nil {
				return fmt.Errorf("%s warmup: %w\n%s", program, e, &stderr)
			}
			if strings.TrimSpace(string(output)) != expected {
				return fmt.Errorf("%s warmup checksum mismatch: %s", program, output)
			}
		}
	}
	var samples []observation
	save := func() error {
		data, e := json.MarshalIndent(map[string]any{"revision": strings.TrimSpace(string(revision)), "go_version": runtime.Version(), "toolchain": toolchain, "platform": runtime.GOOS + "/" + runtime.GOARCH, "environment": settings, "fixture": fixture, "dataset": json.RawMessage(metaBytes), "binary_sha256": binaryHashes, "scan_enabled": true, "parser": "resumable", "library_root": libraryRoot, "warmups": *warmups, "samples": samples}, "", "  ")
		if e != nil {
			return e
		}
		return os.WriteFile(filepath.Join(dest, "results.json"), append(data, '\n'), 0644)
	}
	for round := 0; round < *runs; round++ {
		order := append([]string(nil), programs[round%len(programs):]...)
		order = append(order, programs[:round%len(programs)]...)
		for _, program := range order {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			c := exec.CommandContext(ctx, filepath.Join(dest, program), fixture)
			c.Env = env
			var stdout, stderr bytes.Buffer
			c.Stdout, c.Stderr = &stdout, &stderr
			start := time.Now()
			e := c.Run()
			cancel()
			elapsed := time.Since(start)
			stem := fmt.Sprintf("%s-%d", program, round+1)
			if err = os.WriteFile(filepath.Join(dest, stem+".stdout"), stdout.Bytes(), 0644); err != nil {
				return err
			}
			if err = os.WriteFile(filepath.Join(dest, stem+".stderr"), stderr.Bytes(), 0644); err != nil {
				return err
			}
			s := observation{Program: program, WallSeconds: elapsed.Seconds(), Verified: e == nil && strings.TrimSpace(stdout.String()) == expected}
			if c.ProcessState != nil {
				s.UserSeconds, s.SystemSeconds = c.ProcessState.UserTime().Seconds(), c.ProcessState.SystemTime().Seconds()
				if usage, ok := c.ProcessState.SysUsage().(*syscall.Rusage); ok {
					s.PeakRSSBytes = usage.Maxrss
					if runtime.GOOS != "darwin" {
						s.PeakRSSBytes *= 1024
					}
				}
			}
			samples = append(samples, s)
			if err = save(); err != nil {
				return err
			}
			fmt.Printf("%s: %.3fs, verified=%v\n", stem, s.WallSeconds, s.Verified)
			if !s.Verified {
				return fmt.Errorf("%s failed: %v; see preserved output", stem, e)
			}
		}
	}
	if *diagnostics {
		if err = runDiagnostics(repo, env, dest, compilerPath, fixture, *runs, expected); err != nil {
			return err
		}
	}
	if *prof {
		if err = instrument(project); err != nil {
			return err
		}
		if _, err = command(project, env, "go", "build", "-o", filepath.Join(dest, "profile"), "./entries/main"); err != nil {
			return err
		}
		output, e := command(dest, env, filepath.Join(dest, "profile"), fixture)
		if e != nil {
			return e
		}
		if strings.TrimSpace(string(output)) != expected {
			return fmt.Errorf("profile checksum mismatch: %s", output)
		}
	}
	if *probes {
		for _, probe := range []struct{ name, source string }{{"state", stateProbe}, {"products", productProbe}} {
			source := filepath.Join(dest, probe.name+".fango")
			if err = os.WriteFile(source, []byte(probe.source), 0644); err != nil {
				return err
			}
			project := filepath.Join(dest, probe.name+".out")
			if _, err = command(repo, env, compilerPath, "build", "--emit-go", "-o", project, source); err != nil {
				return err
			}
			if err = os.WriteFile(filepath.Join(project, "entries", probe.name, "allocation_test.go"), []byte(probeTest), 0644); err != nil {
				return err
			}
			output, e := command(project, env, "go", "test", "-v", "./entries/"+probe.name)
			if e != nil {
				return e
			}
			if err = os.WriteFile(filepath.Join(dest, probe.name+"-allocations.txt"), output, 0644); err != nil {
				return err
			}
			fmt.Printf("%s allocation probe passed\n", probe.name)
		}
	}
	return nil
}

// Instrument only an exported copy, after building the timing executable.
func instrument(project string) error {
	path := filepath.Join(project, "entries", "main", "main.go")
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, path, nil, parser.ParseComments)
	if err != nil {
		return err
	}
	mainFound, snapshotFound := false, false
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Name.Name == "main" {
			fn.Name.Name = "benchmarkMain"
			mainFound = true
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			var list []ast.Stmt
			var set func([]ast.Stmt)
			switch n := n.(type) {
			case *ast.BlockStmt:
				list, set = n.List, func(s []ast.Stmt) { n.List = s }
			case *ast.CaseClause:
				list, set = n.Body, func(s []ast.Stmt) { n.Body = s }
			default:
				return true
			}
			for i, s := range list {
				decl, ok := s.(*ast.DeclStmt)
				if !ok {
					continue
				}
				gen, ok := decl.Decl.(*ast.GenDecl)
				if !ok {
					continue
				}
				for _, spec := range gen.Specs {
					v, ok := spec.(*ast.ValueSpec)
					if !ok || len(v.Names) != 1 || !strings.HasPrefix(v.Names[0].Name, "v_total") {
						continue
					}
					prefix := append([]ast.Stmt(nil), list[:i]...)
					prefix = append(prefix, &ast.ExprStmt{X: &ast.CallExpr{Fun: ast.NewIdent("snapshotDecodedOutput")}})
					set(append(prefix, list[i:]...))
					snapshotFound = true
					return false
				}
			}
			return true
		})
	}
	if !mainFound || !snapshotFound {
		return fmt.Errorf("profile instrumentation could not find main and pre-verification boundary")
	}
	var data bytes.Buffer
	if err = format.Node(&data, fs, f); err != nil {
		return err
	}
	if err = os.WriteFile(path, data.Bytes(), 0644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(project, "entries", "main", "profile.go"), []byte(profile), 0644)
}
