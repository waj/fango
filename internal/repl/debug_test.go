package repl

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/libroot"
)

func TestDebugBreakpointInActivePromptExpression(t *testing.T) {
	text := "40 + 2"
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
	name := "<repl:" + hash[:12] + ">"
	got := debugTranscript(t, ":step "+text+"\n:break "+name+":1\n:breaks\n:delete 1\n:continue\n:quit\n", Options{})
	if !strings.Contains(got, name+":1 (verified)") || !strings.Contains(got, "42 :") {
		t.Fatalf("active expression breakpoint did not resolve:\n%s", got)
	}
}

func TestDebugAbsoluteLibraryPathInsideSourceRoot(t *testing.T) {
	root, err := libroot.Root()
	if err != nil {
		t.Fatal(err)
	}
	s := &Session{options: Options{Root: root}, debug: newReplDebug()}
	if got := s.debugFile(filepath.Join(root, "stdlib", "Basics.fango")); got != "<stdlib>/Basics.fango" {
		t.Fatal(got)
	}
	if got := s.debugFile(filepath.Join(root, "Probe.fango")); got != "Probe.fango" {
		t.Fatal(got)
	}
}

func debugTranscript(t *testing.T, script string, opts Options) string {
	t.Helper()
	opts.Debug = true
	var out strings.Builder
	RunWith(strings.NewReader(script), &out, opts)
	got := out.String()
	for _, bad := range []string{"INTERNAL", "panic:", "NAMING ERROR", "TYPE MISMATCH", "SYNTAX PROBLEM", "SHADOWING", "UNHANDLED EFFECT"} {
		if strings.Contains(got, bad) {
			t.Fatalf("debug session failed:\n%s", got)
		}
	}
	return got
}

func TestDebugInteractiveCancelKeepsHandlerLevel(t *testing.T) {
	for _, interrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(interrupt), func(t *testing.T) {
			ed := &scriptedEditor{t: t, lines: []func(*scriptedEditor) inputLine{
				typed("import State"), typed("use State.run 10"), typed(":step State.get()"),
				func(e *scriptedEditor) inputLine {
					if interrupt {
						e.cancel()
						return inputLine{err: errAbortedInput}
					}
					return inputLine{err: io.EOF}
				},
				typed("State.get()"), typed(":end"), typed("7"), typed(":quit"),
			}}
			var out strings.Builder
			RunWith(strings.NewReader(""), &out, Options{Debug: true, Interactive: true, lineEditor: func(tail *tailWriter, cancel func()) lineEditor { ed.out, ed.cancel = tail, cancel; return ed }})
			got := out.String()
			if !strings.Contains(got, "10 : Int") || !strings.Contains(got, "7 :") || !strings.Contains(got, "stopped: step") {
				t.Fatalf("handler level lost on cancellation:\n%s", got)
			}
		})
	}
}

func TestDebugProgramInputAndQuitInLevel(t *testing.T) {
	got := debugTranscript(t, ":step readLine ()\n:continue\nhello\nimport State\nuse State.run 0\n:step State.get()\n:quit\n", Options{})
	if !strings.Contains(got, `text = "hello"`) {
		t.Fatalf("debugger consumed program input:\n%s", got)
	}
}

func TestDebugReloadInvalidatesSnapshot(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "Probe.fango")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	text := "module Probe exposing (run)\nrun : Int -> Int\nrun x = x + 1\n"
	write(text)
	var out strings.Builder
	s := NewSessionWith(&out, Options{Root: root, Debug: true})
	defer s.Close()
	s.submit("import Probe")
	s.submit("old x = Probe.run x")
	s.command(":break Probe.fango:3")
	if len(s.debug.breakpoints) != 1 || s.debug.breakpoints[0].Hash == "" {
		t.Fatal(out.String())
	}
	old := s.debug.breakpoints[0].Hash
	write(strings.Replace(text, "x + 1", "x + 2", 1))
	s.command(":breaks")
	if !s.debug.breakpoints[0].invalid {
		t.Fatal("disk edit did not invalidate breakpoint")
	}
	s.command(":reload")
	s.command(":break Probe.fango:3")
	bp := s.debug.breakpoints[0]
	if bp.invalid || bp.Hash == old || bp.Hash == "" {
		t.Fatalf("breakpoint did not rebind: %+v\n%s", bp, out.String())
	}
	s.command(":delete 1")
	s.submit("Probe.run 10")
	s.submit("old 10")
	if !strings.Contains(out.String(), "12 : Int") || !strings.Contains(out.String(), "11 : Int") {
		t.Fatalf("debug profile lost generations:\n%s", out.String())
	}
}

func TestDebugPromptDefinitionsHaveDistinctSnapshots(t *testing.T) {
	var out strings.Builder
	s := NewSessionWith(&out, Options{Debug: true})
	defer s.Close()
	s.submit("first x = x + 1")
	s.debugRefresh(nil)
	var first string
	for name := range s.debug.sources {
		if strings.HasPrefix(name, "<repl:") {
			first = name
		}
	}
	if first == "" {
		t.Fatal("missing prompt source")
	}
	s.command(":break " + first + ":1")
	s.submit("second x = x + 2")
	s.debugRefresh(nil)
	if s.debug.sources[first] == nil || s.debug.breakpoints[0].invalid {
		t.Fatal("new input invalidated existing prompt definition")
	}
	s.submit("first x = x + 3")
	s.debugRefresh(nil)
	// An old snapshot is no longer in the current definition map, but it must
	// never bind this breakpoint to the replacement input.
	for _, bp := range s.debugBreakpoints() {
		if bp.File != first {
			t.Fatal("silently rebound prompt breakpoint")
		}
	}
}

func TestDebugStepAndInspect(t *testing.T) {
	got := debugTranscript(t, `f : Int -> Int
f x =
    y = x + 1
    y * 2

:step f 20
:step
:step
:locals
:stack
:frame 1
:locals
:frame 0
:print x
:print x + 1
z = 3
:reload
:continue
:quit
`, Options{})
	for _, want := range []string{"stopped: step", "#0 f at <repl:", ">:3:9", "#1 <expression>", "x : Int = 20", "expressions are not evaluated", "only debugger commands", "42 : Int"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
}

func TestDebugImportedBreakpointsAndWarmCache(t *testing.T) {
	root := t.TempDir()
	err := os.WriteFile(filepath.Join(root, "Probe.fango"), []byte(`module Probe exposing (run)

run : Int -> Int
run x =
    y = x + 1
    y * 2
`), 0644)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		got := debugTranscript(t, `:break Probe.fango:6
:break Probe.fango:2
import Probe
:breaks
Probe.run 20
:locals
:print y
:delete 1
:next
:continue
:quit
`, Options{Root: root})
		for _, want := range []string{"pending import", "Probe.fango:6 (verified)", "unresolved: no executable site", "stopped: breakpoint 1", "x : Int = 20", "y : Int = 21", "42 : Int"} {
			if !strings.Contains(got, want) {
				t.Errorf("pass %d missing %q:\n%s", i, want, got)
			}
		}
	}
}

func TestDebugStopAndEOF(t *testing.T) {
	for _, script := range []string{":step 40 + 2\n:stop\n7\n:quit\n", ":step 40 + 2\n"} {
		got := debugTranscript(t, script, Options{})
		if strings.Contains(got, "42 :") {
			t.Fatalf("cancelled evaluation completed:\n%s", got)
		}
		if strings.Contains(script, "7\n") && !strings.Contains(got, "7 :") {
			t.Fatalf("session did not recover:\n%s", got)
		}
	}
}

func TestDebugNativeSidecarAndOutcomeParity(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "debug-native")
	got := debugTranscript(t, ":break Probe.fango:18\nimport Probe\nProbe.run 10\n:locals\n:delete 1\n:continue\n:quit\n", Options{Root: root})
	if !strings.Contains(got, "number : Int = 12") || !strings.Contains(got, "94 : Int") {
		t.Fatalf("native call or pattern scope failed:\n%s", got)
	}
	var ordinary strings.Builder
	RunWith(strings.NewReader("import Probe\nProbe.run 10\n:quit\n"), &ordinary, Options{Root: root})
	if !strings.Contains(ordinary.String(), "94 : Int") {
		t.Fatalf("profile outcome mismatch:\n%s", ordinary.String())
	}
}

func TestDebugWorkerStopRunsCleanupOnce(t *testing.T) {
	root := t.TempDir()
	text := `module Probe exposing (run)
import Runtime.Scope
run : () ->{Console} Int
run() =
    Runtime.Scope.finally {
        print "once"
        42
    } { print "cleanup" }
`
	if err := os.WriteFile(filepath.Join(root, "Probe.fango"), []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
	got := debugTranscript(t, ":break Probe.fango:7\nimport Probe\nProbe.run()\n:stop\n7\n:quit\n", Options{Root: root})
	if strings.Count(got, "once\n") != 1 || strings.Count(got, "cleanup\n") != 1 || !strings.Contains(got, "7 :") || strings.Contains(got, "42 : Int") {
		t.Fatalf("stop replayed IO or skipped cleanup:\n%s", got)
	}
}

func TestDebugSharedCommandsAndPresentation(t *testing.T) {
	got := debugTranscript(t, "f : Int -> Int\nf x =\n    y = x + 1\n    y * 2\n\n:step f 20\ns\n:s\nprint\tx\nprint x + 1\n\nraw print x\nlocals extra\nh\nc\n:quit\n", Options{})
	if strings.Count(got, "x : Int = 20") != 2 {
		t.Fatalf("inspection or repeat failed:\n%s", got)
	}
	for _, want := range []string{
		"debugger: usage: print NAME (expressions are not evaluated)",
		"raw is not supported by the interpreter debugger",
		"locals takes no arguments while stopped",
		"blank input repeats the last valid command", "42 : Int",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
}

// One expected preview is shared with live Delve coverage. This exercises the
// interpreter worker transport and frontend, not just the pure formatter.
func TestDebugStructuralValuesMatchNative(t *testing.T) {
	root, err := filepath.Abs("../../testdata/debug-native")
	if err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile(filepath.Join(root, "Values.preview"))
	if err != nil {
		t.Fatal(err)
	}
	got := debugTranscript(t, "import Values\n:break Values.fango:28\nValues.run 10\nprint value\ncontinue\n:quit\n", Options{Root: root})
	if !strings.Contains(got, strings.TrimSpace(string(expected))) {
		t.Fatalf("shared value preview missing:\n%s", got)
	}
}
