package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/build"
	"github.com/waj/fango/internal/codegen"
)

// Instrument private execution storage only in temporary test artifacts. Pin
// every observed session deliberately: terminal clearing cannot hide behind GC.
// No observer or global registry is installed in the shipped runtimes.
func c3Instrument(t *testing.T, machine, coroutine string, interpreter bool) (string, string) {
	t.Helper()
	machineType, cursorType, receiver, session := "Machine", "MachineIterator", "m", "machine"
	if interpreter {
		machineType, cursorType, receiver, session = "MachineSession", "MachineIteratorSession", "s", "session"
	}
	replace := func(source, old, new string) string {
		if strings.Count(source, old) != 1 {
			t.Fatalf("instrumentation anchor %q must occur exactly once", old)
		}
		return strings.Replace(source, old, new, 1)
	}
	anchor := receiver + ".stats.Steps++"
	machine = replace(machine, anchor, anchor+"\n c3Machines["+receiver+"] = true\n C3Snapshot()")
	if interpreter {
		coroutine = replace(coroutine, "return it\n", "c3Owners[it] = true\n return it\n")
	} else {
		coroutine = replace(coroutine,
			"return &MachineIterator{owner: owner, evidence: evidence, start: start}",
			"it := &MachineIterator{owner: owner, evidence: evidence, start: start}\n c3Owners[it] = true\n return it")
	}
	machine += fmt.Sprintf(`
var c3Machines = map[*%s]bool{}
var c3Owners = map[*%s]bool{}
var c3PeakOwners, c3PeakFrames int
func C3Snapshot() [4]int {
    owners, frames := 0, 0
    for owner := range c3Owners { if !owner.done { owners++ } }
    for session := range c3Machines { frames += len(session.frames) }
    c3PeakOwners = max(c3PeakOwners, owners)
    c3PeakFrames = max(c3PeakFrames, frames)
    return [4]int{c3PeakOwners, c3PeakFrames, owners, frames}
}
func C3Check() [4]int {
    counts := C3Snapshot()
    if counts[0] != 3 || counts[1] > 32 || counts[2] != 0 || counts[3] != 0 {
        panic(fmt.Sprintf("unbounded or retained scheduler storage: %%v", counts))
    }
    for owner := range c3Owners {
        if owner.busy || owner.start != nil { panic("terminal owner retained execution authority") }
        if s := owner.%s; s != nil && s.traversal != nil { panic("terminal owner retained traversal") }
    }
    for s := range c3Machines {
        if len(s.cleanups) + len(s.handlers) + len(s.states) != 0 { panic("terminal machine retained scopes") }
    }
    return counts
}
`, machineType, cursorType, session)
	return machine, coroutine
}

func c3Read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func c3Write(t *testing.T, path, data string) {
	t.Helper()
	if _, err := build.WriteIfChanged(path, []byte(data)); err != nil {
		t.Fatal(err)
	}
}

func TestCoroutineSchedulerStorage(t *testing.T) {
	if testing.Short() {
		t.Skip("instrumented backend subprocesses")
	}
	t.Parallel()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	source := c3Read(t, filepath.Join(root, "testdata/run/coroutine_scheduler.fango"))
	// Run short and long schedules in one program. Both storage probes then
	// compile the expensive Stream dependency graph only once per backend.
	dir := t.TempDir()
	fixture := filepath.Join(dir, "Scheduler.fango")
	emission := filepath.Join(dir, "emission.json")
	if strings.Count(source, "run 2 100") != 1 || strings.Count(source, "run 2 5") != 1 {
		t.Fatal("scheduler fixture main changed")
	}
	program := strings.Replace(source, "run 2 100", "run 2 100\n    run 200 410", 1)
	program = strings.Replace(program, "run 2 5", "run 2 5\n    run 200 401", 1)
	c3Write(t, fixture, program)
	t.Run("interpreter", func(t *testing.T) {
		machinePath := filepath.Join(root, "internal/eval/machine.go")
		coroutinePath := filepath.Join(root, "internal/eval/coroutine.go")
		machine, coroutine := c3Instrument(t, c3Read(t, machinePath), c3Read(t, coroutinePath), true)
		c3Write(t, filepath.Join(dir, "machine.go"), machine)
		c3Write(t, filepath.Join(dir, "coroutine.go"), coroutine)
		// The overlay test uses the same checked, optimization-disabled
		// interpreter path as the ordinary differential suite.
		child := `package main
import (
    "bytes"
    "context"
    "encoding/json"
    "io"
    "os"
    "strings"
    "testing"
    "github.com/waj/fango/internal/eval"
    machineir "github.com/waj/fango/internal/machine"
)
func TestC3StorageOverlay(t *testing.T) {
    var diagnostics bytes.Buffer
    p, ck, ok := compileFile(` + fmt.Sprintf("%q", fixture) + `, &diagnostics)
    if !ok { t.Fatal(diagnostics.String()) }
    p.DisableOptimizations = true
    mp, errs := machineir.Lower(p, ck.B)
    if len(errs) != 0 { t.Fatal(errs) }
    env := eval.NewEnv()
    env.DefineProg(p)
    if err := env.DefineMachineProg(mp); err != nil { t.Fatal(err) }
    if _, err := eval.ForceIO(context.Background(), p.Entry, env, eval.NewIOContext(strings.NewReader(""), io.Discard)); err != nil { t.Fatal(err) }
    t.Logf("peak owners/frames, terminal owners/frames: %v", eval.C3Check())
    files, _, _, ok := emitProjectManifest(` + fmt.Sprintf("%q", fixture) + `, false, &diagnostics)
    if !ok { t.Fatal(diagnostics.String()) }
    encoded, err := json.Marshal(files)
    if err != nil { t.Fatal(err) }
    if err := os.WriteFile(` + fmt.Sprintf("%q", emission) + `, encoded, 0600); err != nil { t.Fatal(err) }
}
`
		c3Write(t, filepath.Join(dir, "overlay_test.go"), child)
		overlay, err := json.Marshal(map[string]any{"Replace": map[string]string{
			machinePath:   filepath.Join(dir, "machine.go"),
			coroutinePath: filepath.Join(dir, "coroutine.go"),
			filepath.Join(root, "cmd/fango/c3_storage_overlay_test.go"): filepath.Join(dir, "overlay_test.go"),
		}})
		if err != nil {
			t.Fatal(err)
		}
		c3Write(t, filepath.Join(dir, "overlay.json"), string(overlay))
		cmd := exec.Command("go", "test", "-overlay", filepath.Join(dir, "overlay.json"), "-run", "^TestC3StorageOverlay$", "-v", "./cmd/fango")
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("interpreter probe: %v\n%s", err, out)
		}
		t.Log(string(out))
	})
	t.Run("compiled", func(t *testing.T) {
		project := filepath.Join(dir, "compiled")
		data, err := os.ReadFile(emission)
		if err != nil {
			t.Fatal(err)
		}
		var files []codegen.File
		if err := json.Unmarshal(data, &files); err != nil {
			t.Fatal(err)
		}
		fixed, err := build.RuntimeFiles()
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, fixed...)
		machine, coroutine := c3Instrument(t, string(generatedFile(t, files, "fangort/machine.go")), string(generatedFile(t, files, "fangort/coroutine.go")), false)
		for _, file := range files {
			data := string(file.Data)
			if file.Path == "fangort/machine.go" {
				data = machine
			}
			if file.Path == "fangort/coroutine.go" {
				data = coroutine
			}
			c3Write(t, filepath.Join(project, file.Path), data)
			if strings.HasPrefix(file.Path, "entries/") && strings.HasSuffix(file.Path, "/main.go") {
				c3Write(t, filepath.Join(project, filepath.Dir(file.Path), "storage_test.go"), `package main
import ("testing"; "fangobuild/fangort")
func TestStorage(t *testing.T) { main(); t.Logf("peak owners/frames, terminal owners/frames: %v", fangort.C3Check()) }
`)
			}
		}
		cmd := exec.Command("go", "test", "-run", "TestStorage", "-v", "./entries/...")
		cmd.Dir = project
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("compiled probe: %v\n%s", err, out)
		}
		// Keep the long trace in failure output only.
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "peak owners/frames") {
				t.Log(line)
			}
		}
	})
}
