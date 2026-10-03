package repl

import (
	"bufio"
	"errors"
	"io"
)

var errInterruptedInput = errors.New("interrupted")

// errAbortedInput is a Ctrl-C the line editor read as a key: the editor has
// already echoed it, unlike a signal that arrives between reads.
var errAbortedInput error = abortedInput{}

type abortedInput struct{}

func (abortedInput) Error() string        { return errInterruptedInput.Error() }
func (abortedInput) Is(target error) bool { return target == errInterruptedInput }

type inputLine struct {
	data []byte
	err  error
	// last marks an error after which the source yields no more lines.
	last bool
}

// lineRequest asks the source for one line. A plain source ignores the
// prompt fields: the session prints its own prompt before the request.
type lineRequest struct {
	prompt string
	// suggestion prefills the edited line (continuation indentation).
	suggestion string
	// program marks a read by the evaluated program rather than the prompt.
	program bool
}

// A lineSource is the pump's one reader of the session's input.
type lineSource interface {
	readLine(req lineRequest) inputLine
}

type plainSource struct{ reader *bufio.Reader }

func (s plainSource) readLine(lineRequest) inputLine {
	data, err := s.reader.ReadBytes('\n')
	return inputLine{data: data, err: err, last: err != nil}
}

// A single reader owns prompt and program input. A cancelled host read leaves
// the pending read with this pump, so the next prompt receives the next line.
type linePump struct {
	requests   chan lineRequest
	lines      <-chan inputLine
	stop       chan struct{}
	interrupts <-chan struct{}
	pending    *inputLine
	reading    bool
	eof        bool
}

func newLinePump(source lineSource, interrupts <-chan struct{}) *linePump {
	requests := make(chan lineRequest)
	lines := make(chan inputLine)
	stop := make(chan struct{})
	go func() {
		defer close(lines)
		for {
			var req lineRequest
			select {
			case req = <-requests:
			case <-stop:
				return
			}
			line := source.readLine(req)
			select {
			case lines <- line:
			case <-stop:
				return
			}
			if line.last {
				return
			}
		}
	}()
	return &linePump{requests: requests, lines: lines, stop: stop, interrupts: interrupts}
}

func (p *linePump) Close() { close(p.stop) }

func (p *linePump) next(req lineRequest) ([]byte, error) {
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
		case p.requests <- req:
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
		if line.last && line.err == io.EOF {
			p.eof = true
		}
		return line.data, line.err
	}
}

func (p *linePump) HasInput() (bool, error) {
	if p.pending != nil {
		return len(p.pending.data) != 0, nil
	}
	data, err := p.next(lineRequest{program: true})
	if len(data) != 0 {
		p.pending = &inputLine{data: data, err: err}
		return true, nil
	}
	if err == io.EOF {
		return false, nil
	}
	return false, err
}

func (p *linePump) ReadInputLine() ([]byte, error) { return p.next(lineRequest{program: true}) }

// Byte reads borrow the same line pump as prompt and program line reads.
// Keep the unread suffix (and its terminal error) for the next consumer.
func (p *linePump) ReadInputBytes(count int64) ([]byte, error) {
	if count <= 0 {
		return nil, nil
	}
	if count > 65536 {
		count = 65536
	}
	data, err := p.next(lineRequest{program: true})
	if int64(len(data)) > count {
		p.pending = &inputLine{data: data[count:], err: err}
		return data[:count:count], nil
	}
	if len(data) > 0 && err == io.EOF {
		err = nil
	}
	return data, err
}
