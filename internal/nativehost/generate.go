package nativehost

import (
	"fmt"
	goast "go/ast"
	"go/format"
	goparser "go/parser"
	gotoken "go/token"
	"strings"

	"github.com/waj/fango/internal/codegen"
)

func (e *Executor) workerSource() ([]byte, error) {
	var imports, installs, entries strings.Builder
	for i, source := range e.sources {
		alias := fmt.Sprintf("native%d", i)
		fmt.Fprintf(&imports, "\t%s %q\n", alias, "fangobuild/native/"+codegen.NativeLinkName(source.Module))
		fmt.Fprintf(&installs, "\t%s.FangoHost = host\n", alias)
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
			fmt.Fprintf(&entries, "\t%q: reflect.ValueOf(%s.%s),\n", name, alias, fn.Name.Name)
		}
	}

	source := `package main

import (
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"reflect"
` + imports.String() + `)

type wireValue struct {
	Kind string
	I int64
	F float64
	S string
	R int32
	B bool
}

type message struct {
	Kind string
	Name string
	Auth string
	Error string
	Panic string
	Code int
	Args []wireValue
	Value wireValue
	Data []byte
	Values []string
	Bool bool
}

type proxy struct {
	enc *gob.Encoder
	dec *gob.Decoder
}

func (p *proxy) request(m message) message {
	if err := p.enc.Encode(m); err != nil { panic(err) }
	var response message
	if err := p.dec.Decode(&response); err != nil { panic(err) }
	return response
}

func responseError(m message) error {
	if m.Error == "" { return nil }
	if m.Error == io.EOF.Error() { return io.EOF }
	return errors.New(m.Error)
}

func (p *proxy) HasInput() (bool, error) {
	m := p.request(message{Kind: "host_has_input"})
	return m.Bool, responseError(m)
}

func (p *proxy) ReadInputLine() ([]byte, error) {
	m := p.request(message{Kind: "host_read_line"})
	return m.Data, responseError(m)
}

func (p *proxy) WriteOutput(data []byte) error {
	return responseError(p.request(message{Kind: "host_write", Data: data}))
}

func (p *proxy) Arguments() []string {
	return p.request(message{Kind: "host_args"}).Values
}

func (p *proxy) WorkingDirectory() string {
	return string(p.request(message{Kind: "host_dir"}).Data)
}

type exitSignal struct{ code int }

func (p *proxy) Exit(code int) {
	p.request(message{Kind: "host_exit", Code: code})
	panic(exitSignal{code: code})
}

var functions = map[string]reflect.Value{
` + entries.String() + `}

func decode(v wireValue) reflect.Value {
	switch v.Kind {
	case "int": return reflect.ValueOf(v.I)
	case "float": return reflect.ValueOf(v.F)
	case "string": return reflect.ValueOf(v.S)
	case "char": return reflect.ValueOf(rune(v.R))
	case "bool": return reflect.ValueOf(v.B)
	default: panic("unknown native argument kind " + v.Kind)
	}
}

func encode(v reflect.Value) wireValue {
	switch v.Kind() {
	case reflect.Int64: return wireValue{Kind: "int", I: v.Int()}
	case reflect.Float64: return wireValue{Kind: "float", F: v.Float()}
	case reflect.String: return wireValue{Kind: "string", S: v.String()}
	case reflect.Int32: return wireValue{Kind: "char", R: int32(v.Int())}
	case reflect.Bool: return wireValue{Kind: "bool", B: v.Bool()}
	default: panic(fmt.Sprintf("unsupported native result type %s", v.Type()))
	}
}

func invoke(name string, args []wireValue) (result message) {
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
	if !ok { panic("unknown native function " + name) }
	in := make([]reflect.Value, len(args))
	for i, arg := range args { in[i] = decode(arg) }
	out := fn.Call(in)
	if len(out) == 1 { result.Value = encode(out[0]) }
	return result
}

func main() {
	conn, err := net.Dial("tcp", os.Getenv("FANGO_NATIVE_ADDR"))
	if err != nil { panic(err) }
	defer conn.Close()
	enc, dec := gob.NewEncoder(conn), gob.NewDecoder(conn)
	if err := enc.Encode(message{Kind: "hello", Auth: os.Getenv("FANGO_NATIVE_TOKEN")}); err != nil { panic(err) }
	host := &proxy{enc: enc, dec: dec}
` + installs.String() + `	for {
		var request message
		if err := dec.Decode(&request); err != nil { return }
		if request.Kind != "call" { panic("unknown request " + request.Kind) }
		if err := enc.Encode(invoke(request.Name, request.Args)); err != nil { return }
	}
}
`
	formatted, err := format.Source([]byte(source))
	if err != nil {
		return nil, fmt.Errorf("format native worker: %w\n%s", err, source)
	}
	return formatted, nil
}
