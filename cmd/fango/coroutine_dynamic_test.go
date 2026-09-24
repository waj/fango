package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/build"
)

// Pin every allocation, so collection cannot mask retained execution or registry
// links. Observe the live list at every publication and again after scope exit.
func c4Instrument(t *testing.T, iterator, coroutine string, interpreter bool) (string, string) {
	t.Helper()
	cursor, session, machine := "MachineIterator", "machine", "Machine"
	if interpreter {
		cursor, session, machine = "MachineIteratorSession", "session", "MachineSession"
	}
	anchor := "scope.last = child"
	if strings.Count(iterator, anchor) != 1 {
		t.Fatal("missing registration anchor")
	}
	iterator = strings.Replace(iterator, anchor, anchor+"\n c4Scopes[scope]=true; c4Children[child]=true; C4Snapshot()", 1)
	iterator = strings.Replace(iterator, "func (it *"+cursor+") unlink() {", "func (it *"+cursor+") unlink() {\n if it.registered && it."+session+"!=nil {c4Sessions[it."+session+"]=true}", 1)
	iterator += fmt.Sprintf(`
var c4Sessions=map[*%s]bool{}
var c4Scopes=map[*%s]bool{}
var c4Children=map[*%s]bool{}
var c4Peak int
func C4Snapshot() [3]int {
 live:=0
 for scope:=range c4Scopes {
  var next *%s
  for child:=scope.last;child!=nil;child=child.previous {
   if child.done || child.parent!=scope || child.next!=next {panic("dead or corrupt coroutine registry entry")}
   next=child
   live++
  }
 }
 c4Peak=max(c4Peak,live)
 return [3]int{c4Peak,live,len(c4Children)}
}
func C4Check() [3]int {
 counts:=C4Snapshot()
 if counts[0]!=3 || counts[1]!=0 {panic(fmt.Sprintf("unbounded or retained registry: %%v",counts))}
 for child:=range c4Children {
  if !child.done || child.busy || child.start!=nil || child.parent!=nil || child.previous!=nil || child.next!=nil {panic("terminal child retained execution or membership")}
  if child.%s!=nil {panic("terminal child retained session")}
 }
 for s:=range c4Sessions {if len(s.frames)+len(s.cleanups)+len(s.handlers)+len(s.states)!=0 || s.traversal!=nil {panic("terminal child retained execution storage")}}
 for scope:=range c4Scopes {if !scope.done || !scope.work.closed {panic("scope left open")}}
 return counts
}
`, machine, cursor, cursor, cursor, session)
	return iterator, coroutine
}

func TestCoroutineDynamicStorage(t *testing.T) {
	if testing.Short() {
		t.Skip("instrumented backend subprocesses")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	source := c3Read(t, filepath.Join(root, "testdata/run/coroutine_dynamic_lifecycle.fango"))
	cases := []struct {
		name    string
		program string
	}{
		{"2", strings.ReplaceAll(source, "repeat scope 100", "repeat scope 2")},
		{"200", strings.ReplaceAll(source, "repeat scope 100", "repeat scope 200")},
		{"async_a1", c3Read(t, filepath.Join(root, "testdata/run/async_a1_cooperative.fango"))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			fixture := filepath.Join(dir, "Dynamic.fango")
			c3Write(t, fixture, tc.program)
			if tc.name != "async_a1" {
				t.Run("interpreter", func(t *testing.T) {
					machinePath := filepath.Join(root, "internal/eval/iterator.go")
					coroutinePath := filepath.Join(root, "internal/eval/coroutine.go")
					machine, coroutine := c4Instrument(t, c3Read(t, machinePath), c3Read(t, coroutinePath), true)
					c3Write(t, filepath.Join(dir, "machine.go"), machine)
					c3Write(t, filepath.Join(dir, "coroutine.go"), coroutine)
					// The overlay test uses the same checked, optimization-disabled
					// interpreter path as the ordinary differential suite.
					child := `package main
import (
    "bytes"
    "context"
    "io"
    "strings"
    "testing"
    "github.com/waj/fango/internal/eval"
    machineir "github.com/waj/fango/internal/machine"
)
func TestC4StorageOverlay(t *testing.T) {
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
    t.Logf("peak registry entries, terminal registry entries, allocations: %v", eval.C4Check())
}
`
					c3Write(t, filepath.Join(dir, "overlay_test.go"), child)
					overlay, err := json.Marshal(map[string]any{"Replace": map[string]string{
						machinePath:   filepath.Join(dir, "machine.go"),
						coroutinePath: filepath.Join(dir, "coroutine.go"),
						filepath.Join(root, "cmd/fango/c4_storage_overlay_test.go"): filepath.Join(dir, "overlay_test.go"),
					}})
					if err != nil {
						t.Fatal(err)
					}
					c3Write(t, filepath.Join(dir, "overlay.json"), string(overlay))
					cmd := exec.Command("go", "test", "-overlay", filepath.Join(dir, "overlay.json"), "-run", "^TestC4StorageOverlay$", "-v", "./cmd/fango")
					cmd.Dir = root
					out, err := cmd.CombinedOutput()
					if err != nil {
						t.Fatalf("interpreter probe: %v\n%s", err, out)
					}
					t.Log(string(out))
				})
			}
			t.Run("compiled", func(t *testing.T) {
				project := filepath.Join(dir, "compiled")
				files := emittedProject(t, fixture)
				fixed, err := build.RuntimeFiles()
				if err != nil {
					t.Fatal(err)
				}
				files = append(files, fixed...)
				machine, coroutine := c4Instrument(t, string(generatedFile(t, files, "fangort/iterator.go")), string(generatedFile(t, files, "fangort/coroutine.go")), false)
				for _, file := range files {
					data := string(file.Data)
					if file.Path == "fangort/iterator.go" {
						data = machine
					}
					if file.Path == "fangort/coroutine.go" {
						data = coroutine
					}
					c3Write(t, filepath.Join(project, file.Path), data)
					if strings.HasPrefix(file.Path, "entries/") && strings.HasSuffix(file.Path, "/main.go") {
						c3Write(t, filepath.Join(project, filepath.Dir(file.Path), "storage_test.go"), `package main
import ("testing"; "fangobuild/fangort")
func TestStorage(t *testing.T) { main(); t.Logf("peak registry entries, terminal registry entries, allocations: %v", fangort.C4Check()) }
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
					if strings.Contains(line, "peak registry entries") {
						t.Log(line)
					}
				}
			})
		})
	}
}

func TestDynamicCoroutineModuleDifferential(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "modules", "dynamic_coroutines", "Main.fango")
	runDifferentialCase(t, path, cliRunner(path))
}
