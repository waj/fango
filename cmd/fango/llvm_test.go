package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/waj/fango/internal/llvmbuild"
	"github.com/waj/fango/internal/llvmgen"
	"github.com/waj/fango/internal/testutil"
)

// Opt in because this gate requires LLVM and compiles each accepted fixture.
func TestLLVMDifferential(t *testing.T) {
	if os.Getenv("FANGO_TEST_LLVM") != "1" {
		t.Skip("use make test-llvm in the Nix development shell")
	}
	t.Setenv("FANGO_BUILD_DIR", t.TempDir())
	files := testutil.GlobFango(t, filepath.Join("..", "..", "testdata", "run"))
	files = append(files, testutil.GlobFango(t, filepath.Join("..", "..", "testdata", "llvm"))...)
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			if _, err := os.Stat(strings.TrimSuffix(path, ".fango") + ".error"); err == nil {
				t.Skip("front-end diagnostic fixture")
			}
			var diagnostics bytes.Buffer
			result, ok := checkGraph(path, &diagnostics, &compilationSession{nativeC: true})
			if !ok {
				t.Fatal(diagnostics.String())
			}
			if _, _, err := llvmgen.Emit(result.Program, result.Checker.B, true); err != nil {
				if strings.Contains(err.Error(), "UNSUPPORTED LLVM FEATURE: Async") {
					t.Skip(err)
				}
				t.Fatal(err)
			}
			binary, err := llvmbuild.Build(path, result, true)
			if err != nil {
				t.Fatal(err)
			}
			runDifferentialCase(t, path, func(t *testing.T, in fixtureInputs, dir string) (string, int) {
				cmd := exec.Command(binary, in.args...)
				if filepath.Base(path) == "json_gc_resume.fango" {
					// Frequent collection exposes captured windows that an
					// optimized allocation could otherwise leave only in SIMD
					// registers outside BDWGC's conservative root scan.
					cmd.Env = append(os.Environ(), "GC_FREE_SPACE_DIVISOR=100")
				}
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				cmd.Stdin = strings.NewReader(in.stdin)
				cmd.Dir = dir
				output, err := cmd.Output()
				if err == nil {
					if filepath.Base(path) == "http_api.fango" {
						// zlib and Go emit different valid deflate streams. Compare
						// HTTP headers and the CRC-validated decompressed body.
						expected, err := os.ReadFile(strings.TrimSuffix(path, ".fango") + ".expected")
						if err != nil {
							t.Fatal(err)
						}
						if canonicalGZipHTTP(t, string(output)) != canonicalGZipHTTP(t, string(expected)) {
							t.Fatalf("decoded LLVM HTTP response differs from expected: %q", output)
						}
						return string(expected), 0
					}
					return string(output), 0
				}
				if exit, ok := err.(*exec.ExitError); ok {
					t.Logf("LLVM stderr: %s", stderr.String())
					return string(output), exit.ExitCode()
				}
				t.Fatal(err)
				return "", 1
			})
		})
	}
}

// Emission needs no LLVM toolchain. A clause resuming with its own snapshot
// stores nothing back; one resuming with a new value still commits it.
func TestLLVMUnchangedStateCommitsNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "counter.fango")
	source := `effect Counter
    get : () -> Int
    put : Int -> ()

bump : () ->{Counter} Int
bump () =
    put (get() + 1)
    get()

main() =
    print (handle bump() with current = 41 of
        get () -> resume current with current
        put next -> resume () with next)
`
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	result, ok := checkGraph(path, &diagnostics, &compilationSession{nativeC: true})
	if !ok {
		t.Fatal(diagnostics.String())
	}
	emitted, _, err := llvmgen.Emit(result.Program, result.Checker.B, true)
	if err != nil {
		t.Fatal(err)
	}
	clause := func(slot string) []string {
		var bodies []string
		for _, part := range strings.Split(string(emitted), "->"+slot+"=")[1:] {
			bodies = append(bodies, part[:strings.Index(part, "fg_op_exit")])
		}
		if len(bodies) == 0 {
			t.Fatalf("no %s clause emitted", slot)
		}
		return bodies
	}
	store := regexp.MustCompile(`(?m)^\*fg_t\d+=`)
	for _, body := range clause("op0") {
		if store.MatchString(body) {
			t.Errorf("get commits its unchanged snapshot:\n%s", body)
		}
	}
	for _, body := range clause("op1") {
		if !store.MatchString(body) {
			t.Errorf("put lost its commit:\n%s", body)
		}
	}
}

func TestLLVMNetwork(t *testing.T) {
	if os.Getenv("FANGO_TEST_LLVM") != "1" {
		t.Skip("use make test-llvm")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			server <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		var input [4]byte
		if _, err = io.ReadFull(conn, input[:]); err == nil && string(input[:]) != "ping" {
			err = fmt.Errorf("client wrote %q", input)
		}
		if err == nil {
			_, err = io.WriteString(conn, "pong")
		}
		if err == nil {
			_, err = io.Copy(io.Discard, conn)
		}
		server <- err
	}()
	dir := t.TempDir()
	t.Setenv("FANGO_BUILD_DIR", filepath.Join(dir, "build"))
	entry := filepath.Join(dir, "Main.fango")
	source := fmt.Sprintf(`import Net
import Bytes
import Reader
import Fail
import Result exposing (Result(..))

main() =
    result = Fail.attempt { Net.withClient "127.0.0.1" %d { connection ->
        sink = Net.sink connection
        sink.write (Bytes.fromString "ping")
        Reader.over (Net.source connection) { reader ->
            print (Reader.readExactly reader 4)
            Fail.fromResult (Net.setReadDeadline connection 20)
            timeout = Fail.attempt { Reader.readExactly reader 1 }
            case timeout of
                Err error -> print error.kind
                Ok _ -> print "unexpected input"
        }
    } }
    case result of
        Err error -> print error.message
        Ok _ -> ()
`, listener.Addr().(*net.TCPAddr).Port)
	if err := os.WriteFile(entry, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "client")
	if output, err := exec.Command(cliBinary(t), "build", "--backend", "llvm", "-o", binary, entry).CombinedOutput(); err != nil {
		t.Fatalf("network client build: %v\n%s", err, output)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, binary).CombinedOutput()
	if err != nil {
		t.Fatalf("network client: %v\n%s", err, output)
	}
	if string(output) != "Just \"pong\"\nTimedOut\n" {
		t.Fatalf("network output %q", output)
	}
	select {
	case err := <-server:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func canonicalGZipHTTP(t *testing.T, output string) string {
	t.Helper()
	wire, err := strconv.Unquote(strings.TrimSpace(output))
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(strings.NewReader(wire)), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	compressed, err := gzip.NewReader(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	defer compressed.Close()
	body, err := io.ReadAll(compressed)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(struct {
		Status   string
		Header   http.Header
		Transfer []string
		Body     []byte
	}{response.Status, response.Header, response.TransferEncoding, body})
	if err != nil {
		t.Fatal(err)
	}
	return string(canonical)
}

func TestLLVMCommands(t *testing.T) {
	if os.Getenv("FANGO_TEST_LLVM") != "1" {
		t.Skip("use make test-llvm")
	}
	dir := t.TempDir()
	t.Setenv("FANGO_BUILD_DIR", filepath.Join(dir, "build"))
	entry := filepath.Join(dir, "Main.fango")
	sidecar := filepath.Join(dir, "Main.native.c")
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(entry, "tag : Int -> Int\ntag = native\nmain() = print (tag 42)\n")
	write(sidecar, "#include \"fango_native.h\"\nint64_t FANGO_NATIVE(Tag)(int64_t x) { return x + 1; }\n")
	invoke := func(args ...string) (string, error) {
		t.Helper()
		out, err := exec.Command(cliBinary(t), args...).CombinedOutput()
		return string(out), err
	}
	for _, args := range [][]string{{"check", "--backend", "llvm", entry}, {"run", "--backend", "llvm", entry, "--", "argument"}} {
		out, err := invoke(args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		if args[0] == "run" && out != "43\n" {
			t.Fatalf("run output %q", out)
		}
	}
	binaries, err := filepath.Glob(filepath.Join(dir, "build", "llvm", "entries", "*", "program"))
	if err != nil || len(binaries) != 1 {
		t.Fatalf("LLVM artifacts: %v %v", binaries, err)
	}
	before, err := os.Stat(binaries[0])
	if err != nil {
		t.Fatal(err)
	}
	if out, err := invoke("run", "--backend", "llvm", entry); err != nil || out != "43\n" {
		t.Fatalf("warm run: %v %s", err, out)
	}
	after, err := os.Stat(binaries[0])
	if err != nil {
		t.Fatal(err)
	}
	if before.ModTime() != after.ModTime() {
		t.Fatal("unchanged LLVM build relinked")
	}
	write(sidecar, "#include \"fango_native.h\"\nint64_t FANGO_NATIVE(Tag)(int64_t x) { return x + 2; }\n")
	if out, err := invoke("run", "--backend", "llvm", entry); err != nil || out != "44\n" {
		t.Fatalf("edited sidecar: %v %s", err, out)
	}
	write(sidecar, "#include \"fango_native.h\"\n")
	if out, err := invoke("check", "--backend", "llvm", entry); err == nil || !strings.Contains(out, "MISSING C NATIVE FUNCTION") {
		t.Fatalf("missing C function: %v %s", err, out)
	}
	write(sidecar, "#include \"fango_native.h\"\nfango_string FANGO_NATIVE(Tag)(int64_t x) { return (fango_string){0}; }\n")
	if out, err := invoke("check", "--backend", "llvm", entry); err == nil || !strings.Contains(out, "conflicting types") {
		t.Fatalf("C ABI mismatch: %v %s", err, out)
	}
	if out, err := invoke("build", "--backend", "llvm", "--emit-go", entry); err == nil || !strings.Contains(out, "emit-go") {
		t.Fatalf("incompatible flags: %v %s", err, out)
	}
	write(entry, "import Async\nmain() =\n    result = Async.run { _ -> () }\n    ()\n")
	if err := os.Remove(sidecar); err != nil {
		t.Fatal(err)
	}
	if out, err := invoke("check", "--backend", "llvm", entry); err == nil || !strings.Contains(out, "UNSUPPORTED LLVM FEATURE: Async") {
		t.Fatalf("reachable Async: %v %s", err, out)
	}
}

// A single-constructor record has no runtime tag: projection binds its field
// without a switch, and its struct declares no tag member.
func TestLLVMProductsHaveNoTag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "point.fango")
	source := `type Point = { x : Int, y : Int }

sumPoint : Point -> Int
sumPoint point = point.x + point.y

main() = print (sumPoint (Point { x = 1, y = 2 }))
`
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	result, ok := checkGraph(path, &diagnostics, &compilationSession{nativeC: true})
	if !ok {
		t.Fatal(diagnostics.String())
	}
	emitted, _, err := llvmgen.Emit(result.Program, result.Checker.B, true)
	if err != nil {
		t.Fatal(err)
	}
	text := string(emitted)
	symbol := func(name string) string { return "fg_s" + hex.EncodeToString([]byte(name)) }
	definition := func(marker string) string {
		for _, line := range strings.SplitAfter(text, "\n") {
			if strings.Contains(line, marker) && strings.HasSuffix(strings.TrimSpace(line), "{") {
				start := strings.Index(text, line)
				end := strings.Index(text[start:], "\n}\n")
				return text[start : start+end]
			}
		}
		t.Fatalf("no definition containing %s emitted", marker)
		return ""
	}
	if body := definition(symbol("sumPoint") + "("); strings.Contains(body, "switch") || strings.Contains(body, "fango_panic") {
		t.Errorf("record projection switches on a tag:\n%s", body)
	}
	if layout := definition(symbol("Point") + "_ad {"); strings.Contains(layout, "tag") {
		t.Errorf("record struct declares a tag:\n%s", layout)
	}
}
