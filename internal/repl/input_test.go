package repl

import (
	"bufio"
	"io"
	"strings"
	"testing"
	"time"
)

func TestByteReadsSharePromptInput(t *testing.T) {
	p := newLinePump(plainSource{bufio.NewReader(strings.NewReader("alpha\r\nomega"))}, nil)
	defer p.Close()
	first, err := p.ReadInputBytes(2)
	if err != nil || string(first) != "al" {
		t.Fatalf("first bytes = %q, %v", first, err)
	}
	if ok, err := p.HasInput(); !ok || err != nil {
		t.Fatalf("peek = %v, %v", ok, err)
	}
	line, err := p.ReadInputLine()
	if err != nil || string(line) != "pha\r\n" {
		t.Fatalf("remaining line = %q, %v", line, err)
	}
	first, err = p.ReadInputBytes(1)
	if err != nil || string(first) != "o" {
		t.Fatalf("final prefix = %q, %v", first, err)
	}
	line, err = p.next(lineRequest{})
	if err != io.EOF || string(line) != "mega" {
		t.Fatalf("prompt suffix = %q, %v", line, err)
	}
	if ok, err := p.HasInput(); ok || err != nil {
		t.Fatalf("EOF = %v, %v", ok, err)
	}
}

func TestInterruptedByteReadLeavesLineForPrompt(t *testing.T) {
	interrupts := make(chan struct{}, 1)
	interrupts <- struct{}{}
	p := newLinePump(plainSource{bufio.NewReader(strings.NewReader("next\n"))}, interrupts)
	defer p.Close()
	if _, err := p.ReadInputBytes(1); err != errInterruptedInput {
		t.Fatalf("interrupt = %v", err)
	}
	line, err := p.next(lineRequest{})
	if err != nil || string(line) != "next\n" {
		t.Fatalf("prompt after interrupt = %q, %v", line, err)
	}
}

type blockedLineSource struct {
	entered chan struct{}
	lines   chan inputLine
}

func (s blockedLineSource) readLine(lineRequest) inputLine {
	s.entered <- struct{}{}
	line, ok := <-s.lines
	if !ok {
		return inputLine{err: io.EOF, last: true}
	}
	return line
}

func TestInterruptedBlockedByteReadLeavesLineForPrompt(t *testing.T) {
	interrupts := make(chan struct{}, 1)
	source := blockedLineSource{entered: make(chan struct{}, 1), lines: make(chan inputLine, 1)}
	p := newLinePump(source, interrupts)
	defer p.Close()
	defer close(source.lines)
	done := make(chan error, 1)
	go func() { _, err := p.ReadInputBytes(1); done <- err }()
	select {
	case <-source.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("byte read did not request a line")
	}
	interrupts <- struct{}{}
	select {
	case err := <-done:
		if err != errInterruptedInput {
			t.Fatalf("interrupt = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("byte read did not interrupt")
	}
	source.lines <- inputLine{data: []byte("next\n")}
	line, err := p.next(lineRequest{})
	if err != nil || string(line) != "next\n" {
		t.Fatalf("prompt received = %q, %v", line, err)
	}
}

func TestEmptyEffectHeaderStillAcceptsOperations(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader("effect Marker a\n\neffect Ask\n    ask : () -> Int\n\nhandle ask() on\n    ask () -> resume 42\n\n:quit\n"), &out)
	text := out.String()
	for _, want := range []string{"Marker : effect", "Ask : effect", "42 : Int"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "SYNTAX PROBLEM") || strings.Contains(text, "UNKNOWN OPERATION") {
		t.Fatal(text)
	}
}

func TestREPLStandardErrorIsSeparate(t *testing.T) {
	var out, errorOutput strings.Builder
	RunWith(strings.NewReader("import Fail\nignore (Fail.attempt { IO.write stderr \"diagnostic\" })\nprint \"still running\"\n:quit\n"), &out, Options{ErrorWriter: &errorOutput})
	if errorOutput.String() != "diagnostic" || strings.Contains(out.String(), "diagnostic") || !strings.Contains(out.String(), "still running") {
		t.Fatalf("streams = %q / %q", out.String(), errorOutput.String())
	}
}
