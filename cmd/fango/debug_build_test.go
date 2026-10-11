package main

import (
	"bytes"
	"debug/buildinfo"
	"github.com/waj/fango/internal/nativedebug"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDebugBuildFlagsAndCacheSwitching(t *testing.T) {
	entry := writeEntry(t, "main() = print 42\n")
	binary := filepath.Join(filepath.Dir(entry), "out")
	for _, tc := range []struct {
		name        string
		debug, link bool
	}{
		{"ordinary", false, true},
		{"debug", true, true},
		{"debug-warm", true, false},
		{"ordinary-again", false, false},
		{"ordinary-warm", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"-v"}
			if tc.debug {
				args = append(args, "--debug")
			}
			_, progress := buildVerbose(t, entry, args...)
			if linked := strings.Contains(progress, "Linking  go build"); linked != tc.link {
				t.Fatalf("linked = %v, want %v:\n%s", linked, tc.link, progress)
			}
			info, err := buildinfo.ReadFile(binary)
			if err != nil {
				t.Fatal(err)
			}
			flags := ""
			for _, setting := range info.Settings {
				if setting.Key == "-gcflags" {
					flags = setting.Value
				}
			}
			want := ""
			if tc.debug {
				want = "all=-N -l"
			}
			if flags != want {
				t.Fatalf("binary gcflags = %q, want %q", flags, want)
			}
			if out, err := exec.Command(binary).CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "42" {
				t.Fatalf("binary output = %q, err = %v", out, err)
			}
		})
	}
}

func TestDebugBuildRejectsExport(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"build", "--debug", "--emit-go", "missing.fango"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "--debug cannot be combined with --emit-go") {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
}

// Debug compilation must not replace ordinary generated files or their cache
// slots. Its complete line sites include literal returns, which stack-trace
// origins alone cannot represent.
func TestNativeDebugProfileIsolationAndSourceSnapshots(t *testing.T) {
	source := "choose n =\n    if n > 0 then\n        42\n    else\n        7\nmain() = print (choose 1)\n"
	entry := writeEntry(t, source)
	root := filepath.Dir(entry)
	buildVerbose(t, entry, "-v")
	generated := filepath.Join(root, ".fango", "build", "entries", "Main", "main.go")
	ordinary, err := os.ReadFile(generated)
	if err != nil {
		t.Fatal(err)
	}
	buildVerbose(t, entry, "--debug", "-v")
	m, err := nativedebug.ReadManifest(filepath.Join(root, "out"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []int{2, 3, 5, 6} {
		if !slices.Contains(m.Lines["Main.fango"], line) {
			t.Fatalf("missing debug statement %d: %v", line, m.Lines["Main.fango"])
		}
	}
	if m.Sources["Main.fango"] != source {
		t.Fatal("incorrect source snapshot")
	}
	debugSource, err := os.ReadFile(filepath.Join(root, ".fango", "build", "debug", "entries", "Main", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(debugSource, []byte("func V_choose")) || !bytes.Contains(debugSource, []byte("<fango-generated>")) {
		t.Fatalf("missing debug structure:\n%s", debugSource)
	}
	after, err := os.ReadFile(generated)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ordinary, after) {
		t.Fatal("debug build changed ordinary generated Go")
	}
	_, warm := buildVerbose(t, entry, "-v")
	if strings.Contains(warm, "Linking  go build") || strings.Contains(warm, " compiled)") && !strings.Contains(warm, "0 compiled)") {
		t.Fatalf("debug build evicted ordinary artifacts:\n%s", warm)
	}
	// Reusing the same destination for an ordinary binary must not permit the
	// older debug metadata to describe it.
	if _, err := nativedebug.ReadManifest(filepath.Join(root, "out")); err == nil {
		t.Fatal("accepted stale debug metadata")
	}
	if err := os.WriteFile(entry, []byte("\n"+source), 0644); err != nil {
		t.Fatal(err)
	}
	buildVerbose(t, entry, "--debug")
	moved, err := nativedebug.ReadManifest(filepath.Join(root, "out"))
	if err != nil {
		t.Fatal(err)
	}
	if moved.Sources["Main.fango"] != "\n"+source || !slices.Contains(moved.Lines["Main.fango"], 7) {
		t.Fatal("debug cache reused stale positions")
	}
}
