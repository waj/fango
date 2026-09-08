package fangort

import (
	"bufio"
	"io"
	"os"
)

// NativeHost is the ambient process interface made available to Go native
// sidecars. Generated programs install SystemNativeHost; the interpreter's
// native worker replaces it with a proxy to the active interpreter session.
type NativeHost interface {
	HasInput() (bool, error)
	ReadInputLine() ([]byte, error)
	WriteOutput([]byte) error
	Arguments() []string
	WorkingDirectory() string
	Exit(int)
}

type systemNativeHost struct{ in *bufio.Reader }

func (h *systemNativeHost) HasInput() (bool, error) {
	_, err := h.in.Peek(1)
	if err == io.EOF {
		return false, nil
	}
	return err == nil, err
}

func (h *systemNativeHost) ReadInputLine() ([]byte, error) {
	b, err := h.in.ReadBytes('\n')
	if err == io.EOF && len(b) > 0 {
		err = nil
	}
	return b, err
}

func (*systemNativeHost) WriteOutput(b []byte) error {
	_, err := os.Stdout.Write(b)
	return err
}

func (*systemNativeHost) Arguments() []string { return os.Args[1:] }

func (*systemNativeHost) WorkingDirectory() string {
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return dir
}

func (*systemNativeHost) Exit(code int) { os.Exit(code) }

// SystemNativeHost is shared by all sidecar packages in a generated program,
// so input buffering remains coherent across native calls.
var SystemNativeHost NativeHost = &systemNativeHost{in: bufio.NewReader(os.Stdin)}
