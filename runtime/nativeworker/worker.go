// Package nativeworker runs the fixed half of the interpreter's native
// sidecar worker. A generated main package supplies only its sidecar bindings.
package nativeworker

import (
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"

	"github.com/waj/fango/runtime/fangort"
	"github.com/waj/fango/runtime/nativewire"
)

type proxy struct {
	enc *gob.Encoder
	dec *gob.Decoder
}

func (p *proxy) request(m nativewire.Message) nativewire.Message {
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
	default:
		panic("unknown native argument kind " + v.Kind)
	}
}

func encode(v reflect.Value) nativewire.Value {
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
	default:
		panic(fmt.Sprintf("unsupported native result type %s", v.Type()))
	}
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
			failure := fangort.ClassifyIOError(out[n-1].Interface().(error))
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

var errorType = reflect.TypeFor[error]()

// Run connects to the interpreter, installs its host in every linked sidecar,
// and serves native calls until the interpreter closes the connection.
func Run(functions map[string]any, installHost func(fangort.NativeHost)) {
	conn, err := net.Dial("tcp", os.Getenv("FANGO_NATIVE_ADDR"))
	if err != nil {
		panic(err)
	}
	defer conn.Close()
	enc, dec := gob.NewEncoder(conn), gob.NewDecoder(conn)
	if err := enc.Encode(nativewire.Message{Kind: "hello", Auth: os.Getenv("FANGO_NATIVE_TOKEN")}); err != nil {
		panic(err)
	}
	installHost(&proxy{enc: enc, dec: dec})
	for {
		var request nativewire.Message
		if err := dec.Decode(&request); err != nil {
			return
		}
		if request.Kind != "call" {
			panic("unknown request " + request.Kind)
		}
		if err := enc.Encode(invoke(functions, request.Name, request.Args)); err != nil {
			return
		}
	}
}
