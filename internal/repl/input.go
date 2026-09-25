package repl

import (
	"bufio"
	"errors"
	"io"
)

var errInterruptedInput = errors.New("interrupted")

type inputLine struct {
	data []byte
	err  error
}

// A single reader owns prompt and program input. A cancelled host read leaves
// the pending read with this pump, so the next prompt receives the next line.
type linePump struct {
	requests   chan struct{}
	lines      <-chan inputLine
	stop       chan struct{}
	interrupts <-chan struct{}
	pending    *inputLine
	reading    bool
	eof        bool
}

func newLinePump(reader *bufio.Reader, interrupts <-chan struct{}) *linePump {
	requests := make(chan struct{})
	lines := make(chan inputLine)
	stop := make(chan struct{})
	go func() {
		defer close(lines)
		for {
			select {
			case <-requests:
			case <-stop:
				return
			}
			data, err := reader.ReadBytes('\n')
			select {
			case lines <- inputLine{data: data, err: err}:
			case <-stop:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return &linePump{requests: requests, lines: lines, stop: stop, interrupts: interrupts}
}

func (p *linePump) Close() { close(p.stop) }

func (p *linePump) next() ([]byte, error) {
	if p.pending != nil {
		line := *p.pending
		p.pending = nil
		return line.data, line.err
	}
	if p.eof {
		return nil, io.EOF
	}
	select {
	case <-p.interrupts:
		return nil, errInterruptedInput
	default:
	}
	if !p.reading {
		select {
		case <-p.interrupts:
			return nil, errInterruptedInput
		case p.requests <- struct{}{}:
			p.reading = true
		}
	}
	select {
	case <-p.interrupts:
		return nil, errInterruptedInput
	case line, ok := <-p.lines:
		if !ok {
			p.eof = true
			return nil, io.EOF
		}
		p.reading = false
		if line.err == io.EOF {
			p.eof = true
		}
		return line.data, line.err
	}
}

func (p *linePump) HasInput() (bool, error) {
	if p.pending != nil {
		return len(p.pending.data) != 0, nil
	}
	data, err := p.next()
	if len(data) != 0 {
		p.pending = &inputLine{data: data, err: err}
		return true, nil
	}
	if err == io.EOF {
		return false, nil
	}
	return false, err
}

func (p *linePump) ReadInputLine() ([]byte, error) { return p.next() }
