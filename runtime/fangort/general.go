package fangort

import (
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
)

// Stable errors reported for invalid general-handler lifetime transitions.
var (
	ErrContinuationConsumed = errors.New("fangort: continuation already consumed")
	ErrContinuationExpired  = errors.New("fangort: continuation expired")
	ErrHandlerClosed        = errors.New("fangort: handler closed")
)

// PanicError reports a panic recovered at a general-handler runtime boundary.
// Value is the original panic value and Stack is captured at the recovery site.
type PanicError struct {
	Phase string
	Value any
	Stack []byte
}

func (e *PanicError) Error() string {
	return fmt.Sprintf("fangort: panic in general handler %s: %v", e.Phase, e.Value)
}

// Unwrap preserves errors.Is/errors.As behavior when the panic value is an error.
func (e *PanicError) Unwrap() error {
	err, _ := e.Value.(error)
	return err
}

// Prompt is an opaque handle identifying a running general handler.
type Prompt struct {
	runtime *generalRuntime
}

// Request describes one suspended operation. Payload is deliberately opaque:
// generated handlers know the operation's concrete argument representation.
type Request[R any] struct {
	Operation    any
	Payload      any
	Continuation Continuation[R]
}

// Continuation is a concurrency-safe, strictly one-shot suspended computation.
// Copies share the same state.
type Continuation[R any] struct {
	state *continuationState[R]
}

type continuationStatus uint8

const (
	continuationPending continuationStatus = iota
	continuationResumed
	continuationDiscarded
	continuationExpired
)

type continuationState[R any] struct {
	mu       sync.Mutex
	status   continuationStatus
	runtime  *generalRuntime
	response chan continuationResponse
	handle   func(Request[R]) (R, error)
	ret      func(any) (R, error)
}

type continuationResponse struct {
	resume bool
	value  any
}

type generalEvent struct {
	operation any
	payload   any
	response  chan continuationResponse
	terminal  *generalTerminal
}

type generalTerminal struct {
	value    any
	err      error
	panicErr *PanicError
}

type generalRuntime struct {
	mu       sync.Mutex
	closed   bool
	events   chan generalEvent
	bodyDone chan struct{}
	terminal generalTerminal
	pending  expirableContinuation
}

type unwindGeneral struct{}

type expirableContinuation interface {
	expire()
}

// RunGeneral runs body under a general one-shot handler. Each operation is
// dispatched to handle. Normal body completion passes through returnClause
// exactly once; body errors and panics bypass it.
func RunGeneral[B, R any](
	body func(Prompt) (B, error),
	handle func(Request[R]) (R, error),
	returnClause func(B) (R, error),
) (R, error) {
	rt := &generalRuntime{
		events:   make(chan generalEvent, 1),
		bodyDone: make(chan struct{}),
	}
	prompt := Prompt{runtime: rt}
	go runGeneralBody(rt, prompt, body)
	return driveGeneral(rt, handle, func(value any) (R, error) {
		return returnClause(value.(B))
	})
}

func runGeneralBody[B any](rt *generalRuntime, prompt Prompt, body func(Prompt) (B, error)) {
	defer close(rt.bodyDone)
	defer func() {
		if recovered := recover(); recovered != nil {
			if _, unwinding := recovered.(unwindGeneral); unwinding {
				rt.finish(generalTerminal{}, false)
				return
			}
			rt.finish(generalTerminal{panicErr: newPanicError("body", recovered)}, true)
		}
	}()
	value, err := body(prompt)
	rt.finish(generalTerminal{value: value, err: err}, true)
}

// Perform suspends the handled body and publishes an operation request. It is
// intended to be called by generated code running in RunGeneral's body.
func Perform[A any](prompt Prompt, operation, payload any) (A, error) {
	var zero A
	if prompt.runtime == nil {
		return zero, ErrHandlerClosed
	}
	response := make(chan continuationResponse, 1)
	if err := prompt.runtime.submit(generalEvent{
		operation: operation,
		payload:   payload,
		response:  response,
	}); err != nil {
		return zero, err
	}
	reply := <-response
	if !reply.resume {
		panic(unwindGeneral{})
	}
	value, ok := reply.value.(A)
	if !ok {
		return zero, fmt.Errorf("fangort: continuation resumed with %T, want operation result type", reply.value)
	}
	return value, nil
}

// Resume supplies the suspended operation's result and drives the body until
// its next handled result. A continuation can be resumed or discarded once.
func (c Continuation[R]) Resume(value any) (R, error) {
	var zero R
	state := c.state
	if state == nil {
		return zero, ErrContinuationExpired
	}
	state.mu.Lock()
	switch state.status {
	case continuationExpired:
		state.mu.Unlock()
		return zero, ErrContinuationExpired
	case continuationResumed, continuationDiscarded:
		state.mu.Unlock()
		return zero, ErrContinuationConsumed
	}
	state.status = continuationResumed
	state.runtime.clearPending(state)
	state.response <- continuationResponse{resume: true, value: value}
	state.mu.Unlock()
	return driveGeneral(state.runtime, state.handle, state.ret)
}

// Discard abandons the suspended body, waits for its deferred cleanup, and
// leaves the operation clause responsible for producing the handled result.
func (c Continuation[R]) Discard() error {
	state := c.state
	if state == nil {
		return ErrContinuationExpired
	}
	state.mu.Lock()
	switch state.status {
	case continuationExpired:
		state.mu.Unlock()
		return ErrContinuationExpired
	case continuationResumed, continuationDiscarded:
		state.mu.Unlock()
		return ErrContinuationConsumed
	}
	state.status = continuationDiscarded
	state.runtime.clearPending(state)
	state.response <- continuationResponse{}
	state.mu.Unlock()
	<-state.runtime.bodyDone
	state.runtime.mu.Lock()
	panicErr := state.runtime.terminal.panicErr
	state.runtime.mu.Unlock()
	if panicErr != nil {
		return panicErr
	}
	return nil
}

func driveGeneral[R any](
	rt *generalRuntime,
	handle func(Request[R]) (R, error),
	returnClause func(any) (R, error),
) (result R, err error) {
	event := <-rt.events
	if event.terminal != nil {
		terminal := event.terminal
		switch {
		case terminal.panicErr != nil:
			return result, terminal.panicErr
		case terminal.err != nil:
			return result, terminal.err
		default:
			return callReturn(returnClause, terminal.value)
		}
	}

	state := &continuationState[R]{
		status:   continuationPending,
		runtime:  rt,
		response: event.response,
		handle:   handle,
		ret:      returnClause,
	}
	rt.setPending(state)
	request := Request[R]{
		Operation:    event.operation,
		Payload:      event.payload,
		Continuation: Continuation[R]{state: state},
	}
	result, err, panicErr := callHandle(handle, request)
	if panicErr != nil || err != nil {
		rt.expirePending()
		<-rt.bodyDone
		if panicErr != nil {
			return result, panicErr
		}
		return result, err
	}
	return result, nil
}

func callHandle[R any](handle func(Request[R]) (R, error), request Request[R]) (result R, err error, panicErr *PanicError) {
	defer func() {
		if recovered := recover(); recovered != nil {
			panicErr = newPanicError("handler", recovered)
		}
	}()
	result, err = handle(request)
	return
}

func callReturn[R any](returnClause func(any) (R, error), value any) (result R, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = newPanicError("return clause", recovered)
		}
	}()
	return returnClause(value)
}

func (state *continuationState[R]) expire() {
	state.mu.Lock()
	if state.status == continuationPending {
		state.status = continuationExpired
		state.response <- continuationResponse{}
	}
	state.mu.Unlock()
}

func (rt *generalRuntime) setPending(state expirableContinuation) {
	rt.mu.Lock()
	if !rt.closed {
		rt.pending = state
	}
	rt.mu.Unlock()
}

func (rt *generalRuntime) clearPending(state expirableContinuation) {
	rt.mu.Lock()
	if rt.pending == state {
		rt.pending = nil
	}
	rt.mu.Unlock()
}

func (rt *generalRuntime) expirePending() {
	rt.mu.Lock()
	pending := rt.pending
	rt.pending = nil
	rt.mu.Unlock()
	if pending != nil {
		pending.expire()
	}
}

func (rt *generalRuntime) submit(event generalEvent) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.closed {
		return ErrHandlerClosed
	}
	rt.events <- event
	return nil
}

func (rt *generalRuntime) finish(terminal generalTerminal, publish bool) {
	rt.mu.Lock()
	if !rt.closed {
		rt.closed = true
		rt.terminal = terminal
		rt.pending = nil
		if publish {
			t := terminal
			rt.events <- generalEvent{terminal: &t}
		}
	}
	rt.mu.Unlock()
}

func newPanicError(phase string, value any) *PanicError {
	return &PanicError{Phase: phase, Value: value, Stack: debug.Stack()}
}
