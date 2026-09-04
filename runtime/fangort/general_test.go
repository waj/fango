package fangort

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
)

func TestRunGeneralNormalReturn(t *testing.T) {
	var returns atomic.Int32
	got, err := RunGeneral(
		func(Prompt) (int, error) { return 20, nil },
		func(Request[string]) (string, error) { return "", errors.New("unexpected operation") },
		func(value int) (string, error) {
			returns.Add(1)
			return fmt.Sprintf("%d!", value), nil
		},
	)
	if err != nil || got != "20!" {
		t.Fatalf("RunGeneral = %q, %v; want %q, nil", got, err, "20!")
	}
	if returns.Load() != 1 {
		t.Fatalf("return clause called %d times, want 1", returns.Load())
	}
}

func TestRunGeneralOneOperationAndNonTailResume(t *testing.T) {
	got, err := RunGeneral(
		func(prompt Prompt) (int, error) {
			value, err := Perform[int](prompt, "ask", 20)
			return value + 1, err
		},
		func(request Request[int]) (int, error) {
			if request.Operation != "ask" || request.Payload != 20 {
				t.Fatalf("request = %#v", request)
			}
			resumed, err := request.Continuation.Resume(21)
			return resumed * 2, err
		},
		func(value int) (int, error) { return value + 3, nil },
	)
	if err != nil || got != 50 {
		t.Fatalf("RunGeneral = %d, %v; want 50, nil", got, err)
	}
}

func TestRunGeneralSequentialOperations(t *testing.T) {
	var handled []string
	got, err := RunGeneral(
		func(prompt Prompt) (int, error) {
			a, err := Perform[int](prompt, "first", 1)
			if err != nil {
				return 0, err
			}
			b, err := Perform[int](prompt, "second", a)
			return a + b, err
		},
		func(request Request[int]) (int, error) {
			handled = append(handled, request.Operation.(string))
			switch request.Operation {
			case "first":
				return request.Continuation.Resume(10)
			case "second":
				return request.Continuation.Resume(20)
			default:
				return 0, fmt.Errorf("unexpected operation %v", request.Operation)
			}
		},
		func(value int) (int, error) { return value, nil },
	)
	if err != nil || got != 30 {
		t.Fatalf("RunGeneral = %d, %v; want 30, nil", got, err)
	}
	if fmt.Sprint(handled) != "[first second]" {
		t.Fatalf("handled %v", handled)
	}
}

func TestContinuationDiscardRunsDeferredCleanup(t *testing.T) {
	cleaned := make(chan struct{})
	got, err := RunGeneral(
		func(prompt Prompt) (int, error) {
			defer close(cleaned)
			return Perform[int](prompt, "stop", nil)
		},
		func(request Request[string]) (string, error) {
			if err := request.Continuation.Discard(); err != nil {
				return "", err
			}
			select {
			case <-cleaned:
			default:
				t.Fatal("Discard returned before body cleanup")
			}
			return "discarded", nil
		},
		func(int) (string, error) {
			t.Fatal("return clause called after discard")
			return "", nil
		},
	)
	if err != nil || got != "discarded" {
		t.Fatalf("RunGeneral = %q, %v; want discarded, nil", got, err)
	}
}

func TestContinuationEscapesThenResumes(t *testing.T) {
	requests := make(chan Continuation[int], 1)
	got, err := RunGeneral(
		func(prompt Prompt) (int, error) { return Perform[int](prompt, "escape", nil) },
		func(request Request[int]) (int, error) {
			requests <- request.Continuation
			return 7, nil
		},
		func(value int) (int, error) { return value + 1, nil },
	)
	if err != nil || got != 7 {
		t.Fatalf("initial RunGeneral = %d, %v; want 7, nil", got, err)
	}
	resumed, err := (<-requests).Resume(40)
	if err != nil || resumed != 41 {
		t.Fatalf("escaped Resume = %d, %v; want 41, nil", resumed, err)
	}
}

func TestContinuationEscapesThenDiscards(t *testing.T) {
	cleaned := make(chan struct{})
	requests := make(chan Continuation[int], 1)
	got, err := RunGeneral(
		func(prompt Prompt) (int, error) {
			defer close(cleaned)
			return Perform[int](prompt, "escape", nil)
		},
		func(request Request[int]) (int, error) {
			requests <- request.Continuation
			return 9, nil
		},
		func(value int) (int, error) { return value, nil },
	)
	if err != nil || got != 9 {
		t.Fatalf("initial RunGeneral = %d, %v; want 9, nil", got, err)
	}
	if err := (<-requests).Discard(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cleaned:
	default:
		t.Fatal("escaped Discard returned before cleanup")
	}
}

func TestContinuationInvalidTransitions(t *testing.T) {
	tests := []struct {
		name   string
		first  func(Continuation[int]) error
		second func(Continuation[int]) error
	}{
		{"resume twice", resumeForTransition, resumeForTransition},
		{"discard twice", discardForTransition, discardForTransition},
		{"resume after discard", discardForTransition, resumeForTransition},
		{"discard after resume", resumeForTransition, discardForTransition},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			continuations := make(chan Continuation[int], 1)
			_, err := RunGeneral(
				func(prompt Prompt) (int, error) { return Perform[int](prompt, "op", nil) },
				func(request Request[int]) (int, error) {
					continuations <- request.Continuation
					return 0, nil
				},
				func(value int) (int, error) { return value, nil },
			)
			if err != nil {
				t.Fatal(err)
			}
			continuation := <-continuations
			if err := tc.first(continuation); err != nil {
				t.Fatalf("first transition: %v", err)
			}
			if err := tc.second(continuation); !errors.Is(err, ErrContinuationConsumed) {
				t.Fatalf("second transition = %v; want ErrContinuationConsumed", err)
			}
		})
	}
}

func resumeForTransition(c Continuation[int]) error {
	_, err := c.Resume(1)
	return err
}

func discardForTransition(c Continuation[int]) error { return c.Discard() }

func TestStaleContinuationAfterHandlerError(t *testing.T) {
	want := errors.New("handler failed")
	var stale Continuation[int]
	_, err := RunGeneral(
		func(prompt Prompt) (int, error) { return Perform[int](prompt, "op", nil) },
		func(request Request[int]) (int, error) {
			stale = request.Continuation
			return 0, want
		},
		func(value int) (int, error) { return value, nil },
	)
	if !errors.Is(err, want) {
		t.Fatalf("RunGeneral error = %v; want %v", err, want)
	}
	if _, err := stale.Resume(1); !errors.Is(err, ErrContinuationExpired) {
		t.Fatalf("stale Resume = %v; want ErrContinuationExpired", err)
	}
	if err := stale.Discard(); !errors.Is(err, ErrContinuationExpired) {
		t.Fatalf("stale Discard = %v; want ErrContinuationExpired", err)
	}
}

func TestOuterHandlerErrorExpiresNestedPendingContinuation(t *testing.T) {
	want := errors.New("outer handler failed")
	cleaned := make(chan struct{})
	var nested Continuation[int]
	_, err := RunGeneral(
		func(prompt Prompt) (int, error) {
			defer close(cleaned)
			if _, err := Perform[int](prompt, "outer", nil); err != nil {
				return 0, err
			}
			return Perform[int](prompt, "nested", nil)
		},
		func(request Request[int]) (int, error) {
			if request.Operation == "nested" {
				nested = request.Continuation
				return 0, nil
			}
			if _, err := request.Continuation.Resume(1); err != nil {
				return 0, err
			}
			return 0, want
		},
		func(value int) (int, error) { return value, nil },
	)
	if !errors.Is(err, want) {
		t.Fatalf("RunGeneral error = %v; want %v", err, want)
	}
	select {
	case <-cleaned:
	default:
		t.Fatal("outer handler error returned before nested body cleanup")
	}
	if _, err := nested.Resume(1); !errors.Is(err, ErrContinuationExpired) {
		t.Fatalf("nested Resume = %v; want ErrContinuationExpired", err)
	}
}

func TestPerformAfterHandlerClosure(t *testing.T) {
	var prompt Prompt
	_, err := RunGeneral(
		func(p Prompt) (int, error) {
			prompt = p
			return 1, nil
		},
		func(Request[int]) (int, error) { return 0, nil },
		func(value int) (int, error) { return value, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Perform[int](prompt, "late", nil); !errors.Is(err, ErrHandlerClosed) {
		t.Fatalf("Perform after closure = %v; want ErrHandlerClosed", err)
	}
	if _, err := Perform[int](Prompt{}, "zero", nil); !errors.Is(err, ErrHandlerClosed) {
		t.Fatalf("Perform with zero prompt = %v; want ErrHandlerClosed", err)
	}
}

func TestBodyAndHandlerErrorsRunCleanup(t *testing.T) {
	t.Run("body", func(t *testing.T) {
		cleaned := make(chan struct{})
		want := errors.New("body failed")
		_, err := RunGeneral(
			func(Prompt) (int, error) {
				defer close(cleaned)
				return 0, want
			},
			func(Request[int]) (int, error) { return 0, nil },
			func(value int) (int, error) { return value, nil },
		)
		if !errors.Is(err, want) {
			t.Fatalf("error = %v; want %v", err, want)
		}
		select {
		case <-cleaned:
		default:
			t.Fatal("body cleanup incomplete")
		}
	})

	t.Run("handler", func(t *testing.T) {
		cleaned := make(chan struct{})
		want := errors.New("handler failed")
		_, err := RunGeneral(
			func(prompt Prompt) (int, error) {
				defer close(cleaned)
				return Perform[int](prompt, "op", nil)
			},
			func(Request[int]) (int, error) { return 0, want },
			func(value int) (int, error) { return value, nil },
		)
		if !errors.Is(err, want) {
			t.Fatalf("error = %v; want %v", err, want)
		}
		select {
		case <-cleaned:
		default:
			t.Fatal("handler error returned before body cleanup")
		}
	})

	t.Run("return clause", func(t *testing.T) {
		want := errors.New("return failed")
		_, err := RunGeneral(
			func(Prompt) (int, error) { return 1, nil },
			func(Request[int]) (int, error) { return 0, nil },
			func(int) (int, error) { return 0, want },
		)
		if !errors.Is(err, want) {
			t.Fatalf("error = %v; want %v", err, want)
		}
	})
}

func TestPanicsBecomePanicErrorsAfterCleanup(t *testing.T) {
	for _, phase := range []string{"body", "handler", "return clause"} {
		t.Run(phase, func(t *testing.T) {
			cleaned := make(chan struct{})
			_, err := RunGeneral(
				func(prompt Prompt) (int, error) {
					defer close(cleaned)
					if phase == "body" {
						panic("body boom")
					}
					if phase == "handler" {
						return Perform[int](prompt, "op", nil)
					}
					return 1, nil
				},
				func(Request[int]) (int, error) { panic("handler boom") },
				func(value int) (int, error) {
					if phase == "return clause" {
						panic("return boom")
					}
					return value, nil
				},
			)
			var panicErr *PanicError
			if !errors.As(err, &panicErr) || panicErr.Phase != phase || len(panicErr.Stack) == 0 {
				t.Fatalf("error = %#v; want populated PanicError for %q", err, phase)
			}
			select {
			case <-cleaned:
			default:
				t.Fatal("panic reported before cleanup")
			}
		})
	}
}

func TestContinuationConcurrentConsumption(t *testing.T) {
	continuations := make(chan Continuation[int], 1)
	_, err := RunGeneral(
		func(prompt Prompt) (int, error) { return Perform[int](prompt, "op", nil) },
		func(request Request[int]) (int, error) {
			continuations <- request.Continuation
			return 0, nil
		},
		func(value int) (int, error) { return value, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	c := <-continuations
	results := make(chan error, 2)
	go func() { _, err := c.Resume(1); results <- err }()
	go func() { results <- c.Discard() }()
	a, b := <-results, <-results
	if (a == nil) == (b == nil) {
		t.Fatalf("concurrent results = %v, %v; want exactly one success", a, b)
	}
	if a != nil && !errors.Is(a, ErrContinuationConsumed) {
		t.Fatalf("first error = %v", a)
	}
	if b != nil && !errors.Is(b, ErrContinuationConsumed) {
		t.Fatalf("second error = %v", b)
	}
}

func TestDiscardReportsCleanupPanic(t *testing.T) {
	continuations := make(chan Continuation[int], 1)
	_, err := RunGeneral(
		func(prompt Prompt) (result int, err error) {
			defer func() { panic("cleanup boom") }()
			return Perform[int](prompt, "op", nil)
		},
		func(request Request[int]) (int, error) {
			continuations <- request.Continuation
			return 0, nil
		},
		func(value int) (int, error) { return value, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	err = (<-continuations).Discard()
	var panicErr *PanicError
	if !errors.As(err, &panicErr) || panicErr.Phase != "body" {
		t.Fatalf("Discard error = %#v; want body PanicError", err)
	}
}

func BenchmarkGeneralPerformResume(b *testing.B) {
	for range b.N {
		_, err := RunGeneral(
			func(prompt Prompt) (int, error) { return Perform[int](prompt, 0, 1) },
			func(request Request[int]) (int, error) { return request.Continuation.Resume(2) },
			func(value int) (int, error) { return value, nil },
		)
		if err != nil {
			b.Fatal(err)
		}
	}
}
