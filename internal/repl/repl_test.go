package repl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/waj/fango/internal/testutil"
)

// Transcript goldens: each testdata/repl/*.in is a scripted session; the
// golden *.txt is the complete output stream, prompts included.
func TestTranscripts(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "testdata", "repl", "*.in"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no testdata/repl/*.in files")
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			script, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var out strings.Builder
			Run(strings.NewReader(string(script)), &out)
			testutil.Golden(t, strings.TrimSuffix(path, ".in")+".txt", out.String())
		})
	}
}

// Session resilience: errors mid-session must not kill it or corrupt state.
func TestErrorRecovery(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader("x = 40\nnope\nx = )\nx + 2\n:quit\n"), &out)
	got := out.String()
	if !strings.Contains(got, "NAMING ERROR") {
		t.Errorf("expected a NAMING ERROR for `nope`:\n%s", got)
	}
	if !strings.Contains(got, "42 : Int") {
		t.Errorf("x should still evaluate after mid-session errors:\n%s", got)
	}
}

func TestRedefinition(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader("x = 1\ny = x + 1\ny\nx = 10\nx + 1\ny\n:quit\n"), &out)
	got := out.String()
	// y was forced at 2 before x's redefinition; the memoized cell stays.
	if strings.Count(got, "2 : Int") < 2 {
		t.Errorf("expected memoized y = 2 before and after redefinition:\n%s", got)
	}
	if !strings.Contains(got, "11 : Int") {
		t.Errorf("expected x + 1 = 11 after redefinition:\n%s", got)
	}
}

func TestPromptAndReadLineShareReader(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader("readLine ()\nhello\n:quit\n"), &out)
	if !strings.Contains(out.String(), `"hello" : String`) {
		t.Fatalf("readLine did not consume the line following the prompt expression:\n%s", out.String())
	}
}

func TestEffectfulPromptDeclarationRejected(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader("x = print 1\n40 + 2\n:quit\n"), &out)
	if !strings.Contains(out.String(), "EFFECTFUL PROMPT DECLARATION") {
		t.Fatalf("effectful declaration was installed:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "42 : number") {
		t.Fatalf("session did not recover after rejecting declaration:\n%s", out.String())
	}
}

func TestPromptNullaryFunctionRunsOnlyWhenCalled(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader("say : () ->{IO} ()\nsay() = print \"ok\"\n:type say\nsay()\nsay()\n:quit\n"), &out)
	if strings.Count(out.String(), "ok\n") != 2 {
		t.Fatalf("nullary function should run only on its two explicit calls:\n%s", out.String())
	}
}
