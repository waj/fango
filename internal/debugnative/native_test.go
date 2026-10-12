//go:build darwin || linux

// Package debugnative runs an opt-in, raw-Go Delve feasibility experiment.
package debugnative

import (
	"bytes"
	"context"
	"debug/dwarf"
	"debug/elf"
	"debug/macho"
	"github.com/waj/fango/internal/nativedebug"
	"net"
	"net/rpc"
	"net/rpc/jsonrpc"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Keep the probe independent of Delve's Go module. Its public JSON-RPC v2
// protocol also makes the result usable when evaluating a future adapter.
func TestNativeDebugging(t *testing.T) {
	if os.Getenv("FANGO_TEST_DEBUG_NATIVE") != "1" {
		t.Skip("opt in with make test-debug-native inside nix develop")
	}
	dlv, err := exec.LookPath("dlv")
	if err != nil {
		t.Fatal("Delve is required: nix develop")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dir := t.TempDir()
	run := func(cwd string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Dir = cwd
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	t.Logf("platform: %s/%s; %s; %s", runtime.GOOS, runtime.GOARCH, run(root, "go", "version"), run(root, dlv, "version"))
	compiler := filepath.Join(dir, "fango")
	run(root, "go", "build", "-o", compiler, "./cmd/fango")
	binary := filepath.Join(dir, "debug-main")
	cmd := exec.CommandContext(ctx, compiler, "build", "--debug", "-o", binary, filepath.Join(root, "testdata/debug-native/Main.fango"))
	cmd.Env = append(os.Environ(), "FANGO_ROOT="+root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("debug build: %v\n%s", err, out)
	}
	if out := run(root, binary); strings.TrimSpace(out) != "94" {
		t.Fatalf("fixture outcome: %s", out)
	}
	inspectDWARF(t, binary)
	if os.Getenv("FANGO_DEBUG_NATIVE_DWARF_ONLY") == "1" {
		t.Log("DWARF-only run: live Delve operations not tested")
		return
	}
	// A short Unix socket path works on macOS too (its socket path limit is 104).
	sockDir, err := os.MkdirTemp("", "fdlv-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockDir)
	socket := filepath.Join(sockDir, "rpc")
	log, err := os.Create(filepath.Join(dir, "delve.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	defer func() {
		if t.Failed() {
			out, _ := os.ReadFile(log.Name())
			if len(out) > 16000 {
				out = out[len(out)-16000:]
			}
			t.Logf("Delve log (last 16 KB):\n%s", out)
		}
	}()
	cmd = exec.CommandContext(ctx, dlv, "exec", binary, "--headless", "--api-version=2", "--log", "--log-output=debugger,rpc,lldbout", "--listen=unix:"+socket)
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() }()
	var conn net.Conn
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		conn, err = net.DialTimeout("unix", socket, time.Second)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		out, _ := os.ReadFile(log.Name())
		t.Fatalf("connect Delve: %v\n%s", err, out)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	client := jsonrpc.NewClient(conn)
	probeSourceStepping(t, client, binary)
}

func inspectDWARF(t *testing.T, path string) {
	t.Helper()
	var data *dwarf.Data
	var err error
	if runtime.GOOS == "darwin" {
		f, e := macho.Open(path)
		if e != nil {
			t.Fatal(e)
		}
		defer f.Close()
		data, err = f.DWARF()
	} else {
		f, e := elf.Open(path)
		if e != nil {
			t.Fatal(e)
		}
		defer f.Close()
		data, err = f.DWARF()
	}
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]bool{}
	var bindings []string
	reader := data.Reader()
	function := ""
	depth, functionDepth := 0, 0
	for {
		e, err := reader.Next()
		if err != nil {
			t.Fatal(err)
		}
		if e == nil {
			break
		}
		if e.Tag == 0 {
			depth--
			if depth < functionDepth {
				function = ""
			}
			continue
		}
		if e.Children {
			depth++
		}
		switch e.Tag {
		case dwarf.TagCompileUnit:
			lines, err := data.LineReader(e)
			if err != nil {
				t.Fatal(err)
			}
			if lines != nil {
				for _, f := range lines.Files() {
					if f != nil && strings.HasSuffix(f.Name, ".fango") {
						files[f.Name] = true
					}
				}
			}
		case dwarf.TagSubprogram:
			function, _ = e.Val(dwarf.AttrName).(string)
			functionDepth = depth
		case dwarf.TagVariable, dwarf.TagFormalParameter:
			if strings.HasPrefix(function, "fangobuild/") && strings.Contains(function, "Probe") {
				name, _ := e.Val(dwarf.AttrName).(string)
				bindings = append(bindings, function+": "+name)
			}
		}
	}
	var names []string
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	sort.Strings(bindings)
	t.Logf("DWARF Fango paths: %v", names)
	t.Logf("DWARF Probe bindings: %v", bindings)
	if len(names) == 0 || len(bindings) == 0 {
		t.Fatal("missing Fango paths or generated Go bindings in DWARF")
	}
}

func probeSourceStepping(t *testing.T, client *rpc.Client, binary string) {
	t.Helper()
	manifest, err := nativedebug.ReadManifest(binary)
	if err != nil {
		t.Fatal(err)
	}
	c := &nativedebug.Controller{RPC: client, Manifest: manifest}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	move := func(mode string) {
		t.Helper()
		if err := c.Move(mode); err != nil {
			t.Fatal(err)
		}
		if c.State.Exited {
			return
		}
		loc := c.State.CurrentThread
		if _, ok := manifest.Sources[loc.File]; !ok {
			t.Fatalf("stopped in generated code: %+v", loc)
		}
		t.Logf("%s: %s:%d", mode, loc.File, loc.Line)
	}
	bp, err := c.Break("Probe.fango:23")
	if err != nil {
		t.Fatal(err)
	}
	move("continue")
	if c.State.CurrentThread.File != "Probe.fango" || c.State.CurrentThread.Line != 23 {
		t.Fatalf("wrong entry stop: %+v", c.State)
	}
	if err := c.Delete(bp.ID); err != nil {
		t.Fatal(err)
	}
	move("next")
	if c.State.CurrentThread.File != "Probe.fango" || c.State.CurrentThread.Line != 25 {
		t.Fatalf("next did not advance over binding: %+v", c.State.CurrentThread)
	}
	inspect := func(commands string, expected ...string) {
		t.Helper()
		var out bytes.Buffer
		if err := c.Prompt(strings.NewReader(commands), &out); err != nil {
			t.Fatal(err)
		}
		for _, want := range expected {
			if !strings.Contains(out.String(), want) {
				t.Fatalf("inspection missing %q:\n%s", want, out.String())
			}
		}
	}
	inspect("args\nlocals\nprint offset\nraw locals\n", "seed : Int = 10", "offset : Int = 11", "v_offset")
	// Enter the stdlib traversal rather than skipping through its callback.
	sawList := false
	for i := 0; i < 12 && !sawList; i++ {
		move("step")
		sawList = c.State.CurrentThread.File == "<stdlib>/List.fango"
	}
	if !sawList {
		t.Fatal("step did not enter stdlib List.map")
	}
	// An explicit breakpoint in a callback takes precedence over next/finish.
	callback, err := c.Break("Probe.fango:18")
	if err != nil {
		t.Fatal(err)
	}
	move("finish")
	if c.State.CurrentThread.File != "Probe.fango" || c.State.CurrentThread.Line != 18 {
		t.Fatalf("finish swallowed callback breakpoint: %+v", c.State.CurrentThread)
	}
	if err := c.Delete(callback.ID); err != nil {
		t.Fatal(err)
	}
	inspect("locals\nprint number\n", "number : Int = 12")
	// The Go sidecar boundary returns to Fango without stopping inside Go.
	move("next")
	loop, err := c.Break("Probe.fango:13")
	if err != nil {
		t.Fatal(err)
	}
	move("continue")
	if err := c.Delete(loop.ID); err != nil {
		t.Fatal(err)
	}
	before, err := c.Frames()
	if err != nil {
		t.Fatal(err)
	}
	move("next")
	after, err := c.Frames()
	if err != nil {
		t.Fatal(err)
	}
	if c.State.CurrentThread.Line != 13 || before[0].FrameOffset != after[0].FrameOffset {
		t.Fatal("tail loop lost its activation")
	}
	move("finish")
	capture, err := c.Break("Probe.fango:36")
	if err != nil {
		t.Fatal(err)
	}
	move("continue")
	inspect("print outer\nprint value\nprint seed\n", "outer : a = 20", "value : a = 2", "seed : a = <unavailable in this native frame>")
	if err := c.Delete(capture.ID); err != nil {
		t.Fatal(err)
	}
	pattern, err := c.Break("Probe.fango:42")
	if err != nil {
		t.Fatal(err)
	}
	move("continue")
	inspect("print number\nprint seed\n", "number : a = 22", "seed : a = 10")
	if err := c.Delete(pattern.ID); err != nil {
		t.Fatal(err)
	}
	if !c.State.Exited {
		move("continue")
	}
	if !c.State.Exited || c.State.ExitStatus != 0 {
		t.Fatalf("program did not finish: %+v", c.State)
	}
}
