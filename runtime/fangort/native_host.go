package fangort

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sync"
)

type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("program exited with status %d", e.Code) }

// SessionHost is the ambient state an interpreter session exposes to its
// worker. Exit remains worker-only because it is reported back as control.
type SessionHost interface {
	HasInput() (bool, error)
	ReadInputLine() ([]byte, error)
	WriteOutput([]byte) error
	Arguments() []string
	WorkingDirectory() string
}

// NativeHost is made available to Go native sidecars.
type NativeHost interface {
	SessionHost
	Exit(int)
	// ExecutionContext is cancelled when the current host evaluation is interrupted.
	ExecutionContext() context.Context
}

type systemNativeHost struct {
	in       *bufio.Reader
	inputMu  sync.Mutex
	outputMu sync.Mutex
}

func (h *systemNativeHost) HasInput() (bool, error) {
	h.inputMu.Lock()
	defer h.inputMu.Unlock()
	_, err := h.in.Peek(1)
	if err == io.EOF {
		return false, nil
	}
	return err == nil, err
}

func (h *systemNativeHost) ReadInputLine() ([]byte, error) {
	h.inputMu.Lock()
	defer h.inputMu.Unlock()
	b, err := h.in.ReadBytes('\n')
	if err == io.EOF && len(b) > 0 {
		err = nil
	}
	return b, err
}

func (h *systemNativeHost) WriteOutput(b []byte) error {
	h.outputMu.Lock()
	defer h.outputMu.Unlock()
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

func (*systemNativeHost) ExecutionContext() context.Context { return context.Background() }

func (*systemNativeHost) Exit(code int) { os.Exit(code) }

// SystemNativeHost is shared by all sidecar packages in a generated program,
// so input buffering remains coherent across native calls.
var SystemNativeHost NativeHost = &systemNativeHost{in: bufio.NewReader(os.Stdin)}
