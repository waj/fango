// Package nativeworker runs the fixed half of the interpreter's native
// sidecar worker. A generated main package supplies only its sidecar bindings.
package nativeworker

import (
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"reflect"
	"strings"
	"sync"

	"github.com/waj/fango/internal/eval"
	"github.com/waj/fango/internal/execcodec"
	"github.com/waj/fango/runtime/fangort"
	"github.com/waj/fango/runtime/nativewire"
)

type directCaller struct{ functions map[string]any }

func (d *directCaller) Has(name string) bool { return d.functions[name] != nil }

func (d *directCaller) Call(_ context.Context, _ fangort.SessionHost, name string, args []any) (value any, err error) {
	defer func() {
		if p := recover(); p != nil {
			if exit, ok := p.(exitSignal); ok {
				err = &fangort.ExitError{Code: exit.code}
				return
			}
			panic(p)
		}
	}()
	fn := reflect.ValueOf(d.functions[name])
	in := make([]reflect.Value, 0, len(args))
	for _, arg := range args {
		if _, unit := arg.(struct{}); unit {
			continue
		}
		in = append(in, reflect.ValueOf(arg))
	}
	out := fn.Call(in)
	if n := len(out); n > 0 && out[n-1].Type() == errorType {
		if !out[n-1].IsNil() {
			return classifyError(name, out[n-1].Interface().(error)), nil
		}
		out = out[:n-1]
	}
	if len(out) == 0 {
		return struct{}{}, nil
	}
	return out[0].Interface(), nil
}

type proxy struct {
	contextMu sync.RWMutex
	context   context.Context
	mu        sync.Mutex
	enc       *gob.Encoder
	dec       *gob.Decoder
}

func (p *proxy) ExecutionContext() context.Context {
	p.contextMu.RLock()
	defer p.contextMu.RUnlock()
	if p.context == nil {
		return context.Background()
	}
	return p.context
}
func (p *proxy) setExecutionContext(ctx context.Context) {
	p.contextMu.Lock()
	p.context = ctx
	p.contextMu.Unlock()
}

func (p *proxy) request(m nativewire.Message) nativewire.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.enc.Encode(m); err != nil {
		panic(err)
	}
	var response nativewire.Message
	if err := p.dec.Decode(&response); err != nil {
		panic(err)
	}
	return response
}

func responseError(m nativewire.Message) error {
	if m.Error == "" {
		return nil
	}
	if m.Error == io.EOF.Error() {
		return io.EOF
	}
	return errors.New(m.Error)
}

func (p *proxy) HasInput() (bool, error) {
	m := p.request(nativewire.Message{Kind: "host_has_input"})
	return m.Bool, responseError(m)
}

func (p *proxy) ReadInputLine() ([]byte, error) {
	m := p.request(nativewire.Message{Kind: "host_read_line"})
	return m.Data, responseError(m)
}

func (p *proxy) WriteOutput(data []byte) error {
	return responseError(p.request(nativewire.Message{Kind: "host_write", Data: data}))
}

func (p *proxy) Arguments() []string {
	return p.request(nativewire.Message{Kind: "host_args"}).Values
}

func (p *proxy) WorkingDirectory() string {
	return string(p.request(nativewire.Message{Kind: "host_dir"}).Data)
}

type exitSignal struct{ code int }

func (p *proxy) Exit(code int) {
	p.request(nativewire.Message{Kind: "host_exit", Code: code})
	panic(exitSignal{code: code})
}

func decode(v nativewire.Value) reflect.Value {
	switch v.Kind {
	case "int":
		return reflect.ValueOf(v.I)
	case "float":
		return reflect.ValueOf(v.F)
	case "string":
		return reflect.ValueOf(v.S)
	case "char":
		return reflect.ValueOf(rune(v.R))
	case "bool":
		return reflect.ValueOf(v.B)
	case "bytes":
		return reflect.ValueOf(fangort.Bytes(v.Bytes))
	default:
		panic("unknown native argument kind " + v.Kind)
	}
}

func encode(v reflect.Value) nativewire.Value {
	if v.Kind() == reflect.Struct && v.NumField() == 0 {
		return nativewire.Value{}
	}
	switch v.Kind() {
	case reflect.Int64:
		return nativewire.Value{Kind: "int", I: v.Int()}
	case reflect.Float64:
		return nativewire.Value{Kind: "float", F: v.Float()}
	case reflect.String:
		return nativewire.Value{Kind: "string", S: v.String()}
	case reflect.Int32:
		return nativewire.Value{Kind: "char", R: int32(v.Int())}
	case reflect.Bool:
		return nativewire.Value{Kind: "bool", B: v.Bool()}
	}
	// Bytes is the one non-scalar result. The exact type is checked rather
	// than the kind, because Value.Bytes panics on any other slice.
	if v.Type() == bytesType {
		return nativewire.Value{Kind: "bytes", Bytes: v.Bytes()}
	}
	panic(fmt.Sprintf("unsupported native result type %s", v.Type()))
}

func invoke(functions map[string]any, name string, args []nativewire.Value) (result nativewire.Message) {
	result.Kind = "result"
	defer func() {
		if p := recover(); p != nil {
			if exit, ok := p.(exitSignal); ok {
				result.Value.Kind = "exit"
				result.Code = exit.code
			} else {
				result.Panic = fmt.Sprint(p)
			}
		}
	}()
	fn, ok := functions[name]
	if !ok {
		panic("unknown native function " + name)
	}
	call := reflect.ValueOf(fn)
	in := make([]reflect.Value, len(args))
	for i, arg := range args {
		in[i] = decode(arg)
	}
	out := call.Call(in)
	// A fallible native returns an error last. A non-nil error is classified
	// here, in the process that saw it, and travels as an ordinary value.
	if n := len(out); n > 0 && out[n-1].Type() == errorType {
		if !out[n-1].IsNil() {
			failure := classifyError(name, out[n-1].Interface().(error))
			result.Failure = &nativewire.Failure{Kind: failure.Kind, Path: failure.Path, Message: failure.Message}
			return result
		}
		out = out[:n-1]
	}
	if len(out) == 1 {
		result.Value = encode(out[0])
	}
	return result
}

func classifyError(name string, err error) fangort.IOFailure {
	if strings.HasPrefix(name, "Net.") {
		return fangort.ClassifyNetError(err)
	}
	return fangort.ClassifyIOError(err)
}

var (
	errorType = reflect.TypeFor[error]()
	bytesType = reflect.TypeFor[fangort.Bytes]()
)

// Run connects to the interpreter, installs its host in every linked sidecar,
// and serves native calls until the interpreter closes the connection.
func Run(functions map[string]any, installHost func(fangort.NativeHost)) {
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	stopped := make(chan struct{})
	defer close(stopped)
	active := struct {
		sync.Mutex
		cancel      context.CancelFunc
		interrupted bool
	}{}
	go func() {
		for {
			select {
			case <-interrupts:
				active.Lock()
				cancel, already := active.cancel, active.interrupted
				if cancel != nil {
					active.interrupted = true
				}
				active.Unlock()
				if cancel != nil && !already {
					cancel()
				}
			case <-stopped:
				return
			}
		}
	}()
	conn, err := net.Dial(os.Getenv("FANGO_NATIVE_NETWORK"), os.Getenv("FANGO_NATIVE_ADDR"))
	if err != nil {
		panic(err)
	}
	defer conn.Close()
	enc, dec := gob.NewEncoder(conn), gob.NewDecoder(conn)
	if err := enc.Encode(nativewire.Message{Kind: "hello", Auth: os.Getenv("FANGO_NATIVE_TOKEN")}); err != nil {
		panic(err)
	}
	host := &proxy{enc: enc, dec: dec}
	installHost(host)
	env := eval.NewEnv()
	caller := &directCaller{functions: functions}
	for {
		var request nativewire.Message
		if err := dec.Decode(&request); err != nil {
			return
		}
		var response nativewire.Message
		switch request.Kind {
		case "call":
			response = invoke(functions, request.Name, request.Args)
		case "execute":
			ctx, cancel := context.WithCancel(context.Background())
			active.Lock()
			active.cancel = cancel
			active.interrupted = false
			active.Unlock()
			response = execute(ctx, request.Data, env, caller, host)
			active.Lock()
			active.cancel = nil
			active.Unlock()
			cancel()
		default:
			panic("unknown request " + request.Kind)
		}
		if err := enc.Encode(response); err != nil {
			return
		}
	}
}

func execute(ctx context.Context, data []byte, env *eval.Env, caller *directCaller, host *proxy) (result nativewire.Message) {
	host.setExecutionContext(ctx)
	defer host.setExecutionContext(nil)
	result.Kind = "result"
	defer func() {
		if p := recover(); p != nil {
			result.Panic = fmt.Sprint(p)
		}
	}()
	payload, err := execcodec.Decode(data)
	if err != nil {
		result.Error = err.Error()
		return
	}
	if payload.Program != nil {
		env.DefineProg(payload.Program)
	}
	ioctx := eval.NewIOContext(strings.NewReader(""), io.Discard)
	ioctx.Natives = caller
	ioctx.Args = host.Arguments()
	ioctx.Dir = host.WorkingDirectory()
	if payload.Force != "" {
		_, err = eval.ForceIO(ctx, payload.Force, env, ioctx)
	} else if payload.Expr != nil {
		var value any
		value, err = eval.EvalIO(ctx, payload.Expr, env, ioctx)
		if err == nil {
			result.Value = encode(reflect.ValueOf(value))
		}
	}
	if exit, ok := err.(*fangort.ExitError); ok {
		result.Value.Kind = "exit"
		result.Code = exit.Code
	} else if err != nil {
		result.Error = err.Error()
	}
	return
}
