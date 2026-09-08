// Package nativehost executes Go sidecars for the Core interpreter in a
// persistent helper process. Sidecars are trusted code; the process boundary
// protects the interpreter lifecycle and supplies its ambient host.
package nativehost

import (
	"context"
	"crypto/sha256"
	"encoding/gob"
	"errors"
	"fmt"
	goast "go/ast"
	goparser "go/parser"
	gotoken "go/token"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	fango "github.com/waj/fango"
	"github.com/waj/fango/internal/codegen"
)

type Source struct {
	Module  string
	Content []byte
}

// Host is the interpreter session observed by a native call.
type Host interface {
	HasInput() (bool, error)
	ReadInputLine() ([]byte, error)
	WriteOutput([]byte) error
	Arguments() []string
	WorkingDirectory() string
}

type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("program exited with status %d", e.Code) }

type wireValue struct {
	Kind string
	I    int64
	F    float64
	S    string
	R    int32
	B    bool
}

type message struct {
	Kind   string
	Name   string
	Auth   string
	Error  string
	Panic  string
	Code   int
	Args   []wireValue
	Value  wireValue
	Data   []byte
	Values []string
	Bool   bool
}

type Executor struct {
	sources []Source
	names   map[string]bool
	digest  string

	mu   sync.Mutex
	cmd  *exec.Cmd
	conn net.Conn
	enc  *gob.Encoder
	dec  *gob.Decoder
}

func New(sources []Source) (*Executor, error) {
	copySources := append([]Source(nil), sources...)
	sort.Slice(copySources, func(i, j int) bool { return copySources[i].Module < copySources[j].Module })
	names := map[string]bool{}
	h := sha256.New()
	io.WriteString(h, "native-worker-v1\x00")
	for _, source := range copySources {
		io.WriteString(h, source.Module)
		h.Write([]byte{0})
		h.Write(source.Content)
		f, err := goparser.ParseFile(gotoken.NewFileSet(), source.Module+".native.go", source.Content, 0)
		if err != nil {
			return nil, err
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*goast.FuncDecl)
			if !ok || fn.Recv != nil || !goast.IsExported(fn.Name.Name) {
				continue
			}
			name := lowerFirst(fn.Name.Name)
			if source.Module != "" {
				name = source.Module + "." + name
			}
			names[name] = true
		}
	}
	probe := &Executor{sources: copySources, names: names}
	generated, err := probe.workerSource()
	if err != nil {
		return nil, err
	}
	h.Write(generated)
	h.Write(codegen.NativeHostSource())
	runtimeFiles, err := fs.Glob(fango.FangortFS, "runtime/fangort/*.go")
	if err != nil {
		return nil, err
	}
	for _, path := range runtimeFiles {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		data, err := fs.ReadFile(fango.FangortFS, path)
		if err != nil {
			return nil, err
		}
		h.Write(data)
	}
	probe.digest = fmt.Sprintf("%x", h.Sum(nil))
	return probe, nil
}

func (e *Executor) Has(name string) bool { return e != nil && e.names[name] }

var (
	bundledOnce sync.Once
	bundledExec *Executor
	bundledErr  error
)

func Bundled() (*Executor, error) {
	bundledOnce.Do(func() {
		paths, err := fs.Glob(fango.StdlibFS, "stdlib/*.native.go")
		if err != nil {
			bundledErr = err
			return
		}
		var sources []Source
		for _, path := range paths {
			data, err := fs.ReadFile(fango.StdlibFS, path)
			if err != nil {
				bundledErr = err
				return
			}
			module := strings.TrimSuffix(filepath.Base(path), ".native.go")
			sources = append(sources, Source{Module: module, Content: data})
		}
		bundledExec, bundledErr = New(sources)
	})
	return bundledExec, bundledErr
}

func (e *Executor) Call(ctx context.Context, host Host, name string, args []any) (any, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.Has(name) {
		return nil, fmt.Errorf("native worker has no function %s", name)
	}
	if err := e.start(); err != nil {
		return nil, err
	}
	values := make([]wireValue, 0, len(args))
	for _, arg := range args {
		if _, unit := arg.(struct{}); unit {
			continue
		}
		v, err := encodeValue(arg)
		if err != nil {
			return nil, fmt.Errorf("native %s: %w", name, err)
		}
		values = append(values, v)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = e.conn.SetDeadline(deadline)
	} else {
		_ = e.conn.SetDeadline(time.Time{})
	}
	callDone := make(chan struct{})
	var watcherDone chan struct{}
	if ctx.Done() != nil {
		watcherDone = make(chan struct{})
		conn := e.conn
		go func() {
			defer close(watcherDone)
			select {
			case <-ctx.Done():
				_ = conn.SetDeadline(time.Now())
			case <-callDone:
			}
		}()
	}
	defer func() {
		close(callDone)
		if watcherDone != nil {
			<-watcherDone
		}
	}()
	if err := e.enc.Encode(message{Kind: "call", Name: name, Args: values}); err != nil {
		e.stop()
		return nil, fmt.Errorf("native worker: %w", err)
	}
	for {
		var m message
		if err := e.dec.Decode(&m); err != nil {
			e.stop()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("native worker: %w", err)
		}
		switch m.Kind {
		case "host_has_input":
			ok, err := host.HasInput()
			e.reply(okMessage("host_reply", ok, err))
		case "host_read_line":
			data, err := host.ReadInputLine()
			e.reply(dataMessage("host_reply", data, err))
		case "host_write":
			e.reply(errorMessage("host_reply", host.WriteOutput(m.Data)))
		case "host_args":
			e.reply(message{Kind: "host_reply", Values: append([]string(nil), host.Arguments()...)})
		case "host_dir":
			e.reply(message{Kind: "host_reply", Data: []byte(host.WorkingDirectory())})
		case "host_exit":
			e.reply(message{Kind: "host_reply"})
		case "result":
			if m.Error != "" {
				return nil, errors.New(m.Error)
			}
			if m.Panic != "" {
				panic(fmt.Sprintf("native %s panicked: %s", name, m.Panic))
			}
			if m.Code != 0 || m.Value.Kind == "exit" {
				return nil, &ExitError{Code: m.Code}
			}
			return decodeValue(m.Value)
		default:
			e.stop()
			return nil, fmt.Errorf("native worker sent unknown message %q", m.Kind)
		}
	}
}

func (e *Executor) reply(m message) {
	if err := e.enc.Encode(m); err != nil {
		e.stop()
	}
}

func okMessage(kind string, value bool, err error) message {
	m := errorMessage(kind, err)
	m.Bool = value
	return m
}

func dataMessage(kind string, data []byte, err error) message {
	m := errorMessage(kind, err)
	m.Data = data
	return m
}

func errorMessage(kind string, err error) message {
	m := message{Kind: kind}
	if err != nil {
		m.Error = err.Error()
	}
	return m
}

func encodeValue(v any) (wireValue, error) {
	switch v := v.(type) {
	case int64:
		return wireValue{Kind: "int", I: v}, nil
	case float64:
		return wireValue{Kind: "float", F: v}, nil
	case string:
		return wireValue{Kind: "string", S: v}, nil
	case rune:
		return wireValue{Kind: "char", R: int32(v)}, nil
	case bool:
		return wireValue{Kind: "bool", B: v}, nil
	default:
		return wireValue{}, fmt.Errorf("unsupported argument type %T", v)
	}
}

func decodeValue(v wireValue) (any, error) {
	switch v.Kind {
	case "":
		return struct{}{}, nil
	case "int":
		return v.I, nil
	case "float":
		return v.F, nil
	case "string":
		if !utf8.ValidString(v.S) {
			panic("native returned invalid UTF-8")
		}
		return v.S, nil
	case "char":
		r := rune(v.R)
		if !utf8.ValidRune(r) {
			panic("native returned invalid Char")
		}
		return r, nil
	case "bool":
		return v.B, nil
	default:
		return nil, fmt.Errorf("native worker returned unknown value kind %q", v.Kind)
	}
}

func (e *Executor) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stop()
}

func (e *Executor) stop() error {
	if e.conn != nil {
		_ = e.conn.Close()
	}
	var err error
	if e.cmd != nil && e.cmd.Process != nil {
		_ = e.cmd.Process.Kill()
		err = e.cmd.Wait()
	}
	e.conn, e.enc, e.dec, e.cmd = nil, nil, nil, nil
	return err
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func (e *Executor) start() error {
	if e.conn != nil {
		return nil
	}
	binary, err := e.build()
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	token := e.digest[:24]
	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(), "FANGO_NATIVE_ADDR="+listener.Addr().String(), "FANGO_NATIVE_TOKEN="+token)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	if tcp, ok := listener.(*net.TCPListener); ok {
		_ = tcp.SetDeadline(time.Now().Add(10 * time.Second))
	}
	conn, err := listener.Accept()
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("start native worker: %w", err)
	}
	enc, dec := gob.NewEncoder(conn), gob.NewDecoder(conn)
	var hello message
	if err := dec.Decode(&hello); err != nil || hello.Kind != "hello" || hello.Auth != token {
		_ = conn.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("native worker authentication failed")
	}
	e.cmd, e.conn, e.enc, e.dec = cmd, conn, enc, dec
	return nil
}

func (e *Executor) build() (string, error) {
	buildMu.Lock()
	defer buildMu.Unlock()
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	dir := filepath.Join(cache, "fango", "native-host", e.digest)
	binary := filepath.Join(dir, "native-worker")
	if _, err := os.Stat(binary); err == nil {
		return binary, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		dir = filepath.Join(os.TempDir(), "fango-native-host", e.digest)
		binary = filepath.Join(dir, "native-worker")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		if _, err := os.Stat(binary); err == nil {
			return binary, nil
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fangobuild\n\ngo 1.26\n"), 0o644); err != nil {
		return "", err
	}
	entries, err := fs.ReadDir(fango.FangortFS, "runtime/fangort")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(dir, "fangort"), 0o755); err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		data, err := fs.ReadFile(fango.FangortFS, "runtime/fangort/"+entry.Name())
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, "fangort", entry.Name()), data, 0o644); err != nil {
			return "", err
		}
	}
	for _, source := range e.sources {
		pkg := filepath.Join(dir, "native", codegen.NativeLinkName(source.Module))
		if err := os.MkdirAll(pkg, 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(pkg, "native.go"), source.Content, 0o644); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(pkg, "host.go"), codegen.NativeHostSource(), 0o644); err != nil {
			return "", err
		}
	}
	mainSource, err := e.workerSource()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), mainSource, 0o644); err != nil {
		return "", err
	}
	cmd := exec.Command("go", "build", "-o", binary, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOTOOLCHAIN=local")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("build native worker: %w\n%s", err, out)
	}
	return binary, nil
}

var buildMu sync.Mutex
