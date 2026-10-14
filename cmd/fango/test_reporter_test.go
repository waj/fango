package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/waj/fango/internal/core"
	"github.com/waj/fango/internal/eval"
	"github.com/waj/fango/internal/nativehost"
	"github.com/waj/fango/internal/natives"
)

// Exercise Test.run's actual process arguments, output, exit policy, and
// filesystem report under both backends. Each run has an isolated directory.
func TestReporterCommandLine(t *testing.T) {
	source := `import Expect
import File
import IO exposing (IO)
import Test exposing (Test, describe, skip, test, todo)

suite : Test IO
suite = describe "suite"
    [ test "passes" { File.writeAll "body-ran" "yes" |> Expect.ok }
    , describe "nested"
        [ test "fails \"é\"" { Expect.failWith "line one\nline \"two\"" }
        , skip (test "skip" { Expect.failWith "not run" })
        , todo "todo"
        ]
    , describe "empty" []
    ]

main() = Test.run suite
`
	path := filepath.Join(t.TempDir(), "tests.fango")
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	prog, executor := compileInterpreterProgram(t, path)
	const usage = "Usage: tests [--format console|progress|documentation|silent] [--json PATH] [--help]\n"
	const failures = "FAIL  suite > nested > fails \"é\"\n\n    tests.fango:10\n    line one\n    line \"two\"\n\nFAIL  suite > empty\n\n    The group has no tests.\n\n"
	const summary = "TEST RUN FAILED\n\nPassed:  1\nFailed:  2\nSkipped: 1\nTodo:    1\n"
	const documentation = "suite\n  passes (passed)\n  nested\n    fails \"é\" (failed)\n    skip (skipped)\n    todo (todo)\n  empty\n\n"
	cases := []struct {
		name        string
		args        []string
		output      string
		status      int
		ran, report bool
	}{
		{"default", nil, failures + summary, 1, true, false},
		{"console", []string{"--format", "console"}, failures + summary, 1, true, false},
		{"progress", []string{"--format", "progress"}, ".F**F\n\n" + failures + summary, 1, true, false},
		{"documentation", []string{"--format", "documentation"}, documentation + failures + summary, 1, true, false},
		{"silent", []string{"--format", "silent"}, "", 1, true, false},
		{"json", []string{"--json", "report.json"}, failures + summary, 1, true, true},
		{"progress with json", []string{"--format", "progress", "--json", "report.json"}, ".F**F\n\n" + failures + summary, 1, true, true},
		{"documentation with json", []string{"--json", "report.json", "--format", "documentation"}, documentation + failures + summary, 1, true, true},
		{"silent json", []string{"--format", "silent", "--json", "report.json"}, "", 1, true, true},
		{"last values win", []string{"--format", "progress", "--format", "silent", "--json", "ignored.json", "--json", "report.json"}, "", 1, true, true},
		{"help", []string{"--help"}, usage, 0, false, false},
		{"invalid format", []string{"--format", "invalid"}, "Test.run: unknown format \"invalid\".\n" + usage, 2, false, false},
		{"missing format", []string{"--format"}, "Test.run: --format requires a value.\n" + usage, 2, false, false},
		{"unknown option", []string{"--unknown"}, "Test.run: unknown argument \"--unknown\".\n" + usage, 2, false, false},
		{"positional argument", []string{"extra"}, "Test.run: unknown argument \"extra\".\n" + usage, 2, false, false},
		{"missing path", []string{"--json"}, "Test.run: --json requires a path.\n" + usage, 2, false, false},
		{"empty path", []string{"--json", ""}, "Test.run: --json requires a path.\n" + usage, 2, false, false},
		{"option as path", []string{"--json", "--format", "silent"}, "Test.run: --json requires a path.\n" + usage, 2, false, false},
		{"write failure", []string{"--format", "silent", "--json", "missing/report.json"}, "Test.run: JSON report: missing/report.json: no such file or directory\n", 2, true, false},
	}
	for _, tc := range cases {
		for _, backend := range executionTestBackends() {
			t.Run(tc.name+"/"+backend, func(t *testing.T) {
				if backend == "compiled" && testing.Short() {
					t.Skip("compiled leg")
				}
				dir := t.TempDir()
				if tc.report {
					if err := os.WriteFile(filepath.Join(dir, "report.json"), []byte("stale"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				output, status := runReporterProgram(t, backend, path, prog, executor, tc.args, dir)
				if status != tc.status || output != tc.output {
					t.Fatalf("status %d, stdout %q; want status %d, stdout %q", status, output, tc.status, tc.output)
				}
				_, err := os.Stat(filepath.Join(dir, "body-ran"))
				if (err == nil) != tc.ran {
					t.Fatalf("body ran: %v, want %v", err == nil, tc.ran)
				}
				data, err := os.ReadFile(filepath.Join(dir, "report.json"))
				if tc.report {
					if err != nil {
						t.Fatal(err)
					}
					const wantJSON = `{"schemaVersion":2,"cases":[{"path":["suite","passes"],"outcome":"passed","message":null,"location":null},{"path":["suite","nested","fails \"é\""],"outcome":"failed","message":"line one\nline \"two\"","location":{"file":"tests.fango","line":10}},{"path":["suite","nested","skip"],"outcome":"skipped","message":null,"location":null},{"path":["suite","nested","todo"],"outcome":"todo","message":null,"location":null}],"groupFailures":[{"path":["suite","empty"],"message":"The group has no tests."}],"summary":{"passed":1,"failed":2,"skipped":1,"todo":1,"verdict":"failed"}}`
					var got, want any
					if err := json.Unmarshal(data, &got); err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal([]byte(wantJSON), &want); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("JSON = %s; want %s", data, wantJSON)
					}
					if !bytes.HasSuffix(data, []byte("\n")) {
						t.Fatal("JSON lacks final newline")
					}
				} else if !os.IsNotExist(err) {
					t.Fatalf("unexpected report: %s, error %v", data, err)
				}
				if _, err := os.Stat(filepath.Join(dir, "ignored.json")); !os.IsNotExist(err) {
					t.Fatalf("superseded JSON path was written: %v", err)
				}
			})
		}
	}
}

func runReporterProgram(t *testing.T, backend, path string, prog *core.Prog, executor *nativehost.Executor, args []string, dir string) (string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	status := 0
	if backend == "interpreter" {
		env := eval.NewEnv()
		env.DefineProg(prog)
		ioctx := eval.NewIOContext(strings.NewReader(""), &stdout)
		ioctx.Args, ioctx.Dir, ioctx.Natives = args, dir, executor
		err := withInterpreter(func() error {
			_, err := eval.ForceIO(context.Background(), prog.Entry, env, ioctx)
			return err
		})
		var exit *natives.ExitError
		if errors.As(err, &exit) {
			status = exit.Code
		} else if err != nil {
			t.Fatal(err)
		}
	} else {
		cmd := exec.Command(cliCompiledBinary(t, path), args...)
		cmd.Dir, cmd.Stdout, cmd.Stderr = dir, &stdout, &stderr
		err := cmd.Run()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			status = exit.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		if stderr.Len() != 0 {
			t.Fatalf("stderr: %s", stderr.String())
		}
	}
	return stdout.String(), status
}

func TestReporterJSONVerdicts(t *testing.T) {
	cases := []struct {
		name, suite, main string
		status            int
		verdict           string
		outcomes          []string
	}{
		{"passed", `Test.test "pass" { () }`, `Test.run suite`, 0, "passed", []string{"passed"}},
		{"incomplete", `Test.describe "unfinished" [Test.skip (Test.test "skip" { Expect.failWith "not run" }), Test.todo "later"]`, `Test.run suite`, 1, "incomplete", []string{"skipped", "todo"}},
		{"focused", `Test.describe "focus" [Test.test "skip" { Expect.failWith "not run" }, Test.only (Test.test "run" { () }), Test.todo "later"]`, `Test.run suite`, 1, "incomplete", []string{"skipped", "passed", "todo"}},
		{"abort", `Test.test "abort" { Fail.fail "aborted" }`, `ignore (Fail.attempt { Test.run suite })`, 0, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tests.fango")
			source := "import Expect\nimport Fail\nimport Test\nsuite = " + tc.suite + "\nmain() = " + tc.main + "\n"
			if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
				t.Fatal(err)
			}
			prog, executor := compileInterpreterProgram(t, path)
			for _, backend := range executionTestBackends() {
				t.Run(backend, func(t *testing.T) {
					if backend == "compiled" && testing.Short() {
						t.Skip("compiled leg")
					}
					dir := t.TempDir()
					output, status := runReporterProgram(t, backend, path, prog, executor, []string{"--format", "silent", "--json", "report.json"}, dir)
					if output != "" || status != tc.status {
						t.Fatalf("output %q, status %d; want status %d", output, status, tc.status)
					}
					data, err := os.ReadFile(filepath.Join(dir, "report.json"))
					if tc.verdict == "" {
						if !os.IsNotExist(err) {
							t.Fatalf("aborted run wrote report: %s, %v", data, err)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					var report struct {
						SchemaVersion int
						Cases         []struct{ Outcome string }
						GroupFailures []any
						Summary       struct{ Verdict string }
					}
					if err := json.Unmarshal(data, &report); err != nil {
						t.Fatal(err)
					}
					var outcomes []string
					for _, item := range report.Cases {
						outcomes = append(outcomes, item.Outcome)
					}
					if report.SchemaVersion != 2 || report.Summary.Verdict != tc.verdict || len(report.GroupFailures) != 0 || !reflect.DeepEqual(outcomes, tc.outcomes) {
						t.Fatalf("report = %s; want verdict %s, outcomes %v", data, tc.verdict, tc.outcomes)
					}
				})
			}
		})
	}
}
