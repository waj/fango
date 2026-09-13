package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fixtureInputs are the optional files beside a fixture's source: X.stdin is
// scripted standard input, X.args lists one program argument per line,
// X.status is the exit status the program must end with (0 when absent), and
// X.files/ is a seed directory copied into a fresh working directory for each
// leg, so a fixture can read, write, and fail on real files without touching
// the repository or the other backend's run.
type fixtureInputs struct {
	stdin  string
	args   []string
	status int
	files  string
}

func readFixtureInputs(t *testing.T, base string) fixtureInputs {
	t.Helper()
	var in fixtureInputs
	if data, err := os.ReadFile(base + ".stdin"); err == nil {
		in.stdin = string(data)
	}
	if data, err := os.ReadFile(base + ".args"); err == nil {
		in.args = strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	}
	if data, err := os.ReadFile(base + ".status"); err == nil {
		status, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			t.Fatalf("%s.status: %v", base, err)
		}
		in.status = status
	}
	if info, err := os.Stat(base + ".files"); err == nil && info.IsDir() {
		in.files = base + ".files"
	}
	return in
}

// seedDir gives one leg its own copy of the fixture's seed directory, or ""
// when the fixture has none and the leg keeps the default working directory.
func seedDir(t *testing.T, in fixtureInputs) string {
	t.Helper()
	if in.files == "" {
		return ""
	}
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(in.files)); err != nil {
		t.Fatal(err)
	}
	return dir
}

// compiledRunner produces the compiled backend's stdout and exit status for
// one case, run in dir when dir is not empty.
type compiledRunner func(t *testing.T, in fixtureInputs, dir string) (string, int)

func batchRunner(path string) compiledRunner {
	return func(t *testing.T, in fixtureInputs, dir string) (string, int) {
		return runCompiled(t, exec.Command(runFixtureBatch(t).binary(path), in.args...), in.stdin, dir)
	}
}

// cliRunner compiles and runs through the real CLI, in a private build dir.
func cliRunner(path string) compiledRunner {
	return func(t *testing.T, in fixtureInputs, dir string) (string, int) {
		source, err := filepath.Abs(path)
		if err != nil {
			t.Fatal(err)
		}
		args := []string{"run", source}
		if len(in.args) > 0 {
			args = append(append(args, "--"), in.args...)
		}
		cmd := exec.Command(cliBinary(t), args...)
		cmd.Env = append(os.Environ(),
			"FANGO_INTERNAL_PRINT_MAIN=1",
			"FANGO_BUILD_DIR="+t.TempDir())
		return runCompiled(t, cmd, in.stdin, dir)
	}
}

func runCompiled(t *testing.T, cmd *exec.Cmd, stdin, dir string) (string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Dir = dir
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.String(), exitErr.ExitCode()
	}
	if err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(cmd.Args, " "), err, stderr.String())
	}
	return stdout.String(), 0
}
