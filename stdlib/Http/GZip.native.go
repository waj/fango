package native

import (
	"bytes"
	"compress/gzip"
	"io"
	"runtime"
)

type compressor struct {
	buffer bytes.Buffer
	writer *gzip.Writer
	ended  bool
}

func NewCompressor() any {
	c := &compressor{}
	c.writer = gzip.NewWriter(&c.buffer)
	return c
}

func (c *compressor) take() []byte {
	result := append([]byte(nil), c.buffer.Bytes()...)
	c.buffer.Reset()
	return result
}

func Push(value any, data []byte) []byte {
	c := value.(*compressor)
	if c.ended {
		panic("gzip compressor already finished")
	}
	if _, err := c.writer.Write(data); err != nil {
		panic(err)
	}
	return c.take()
}

func Flush(value any) []byte {
	c := value.(*compressor)
	if c.ended {
		panic("gzip compressor already finished")
	}
	if err := c.writer.Flush(); err != nil {
		panic(err)
	}
	return c.take()
}

func Finish(value any) []byte {
	c := value.(*compressor)
	if c.ended {
		panic("gzip compressor already finished")
	}
	c.ended = true
	if err := c.writer.Close(); err != nil {
		panic(err)
	}
	return c.take()
}

// Decoder status codes, answered by DecoderPush and DecoderFinish.
const (
	decoderOK        int64 = 0
	decoderMalformed int64 = 1
	decoderTooLarge  int64 = 2
)

// inflater runs gzip.Reader in its own goroutine, because the reader pulls its
// input while Fango pushes it. Each push hands the goroutine one chunk and
// waits until it asks for more or stops, so the two never run at once and the
// output needs no lock.
type inflater struct {
	in     chan []byte
	need   chan struct{}
	done   chan struct{}
	out    []byte
	err    error
	closed bool
}

type inflaterSource struct {
	state   *inflater
	pending []byte
}

func (s *inflaterSource) Read(p []byte) (int, error) {
	if len(s.pending) == 0 {
		s.state.need <- struct{}{}
		chunk, ok := <-s.state.in
		if !ok {
			return 0, io.EOF
		}
		s.pending = chunk
	}
	n := copy(p, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}

func (f *inflater) run() {
	defer close(f.done)
	reader, err := gzip.NewReader(&inflaterSource{state: f})
	if err != nil {
		f.err = err
		return
	}
	buf := make([]byte, 32<<10)
	for {
		n, err := reader.Read(buf)
		f.out = append(f.out, buf[:n]...)
		if err == io.EOF {
			return
		}
		if err != nil {
			f.err = err
			return
		}
	}
}

// wait returns once the goroutine needs input or has stopped.
func (f *inflater) wait() {
	select {
	case <-f.need:
	case <-f.done:
	}
}

func (f *inflater) stop() {
	if !f.closed {
		f.closed = true
		close(f.in)
	}
}

// decoder is the handle Fango holds. Only the handle carries the finalizer,
// so an abandoned body still lets its goroutine finish.
type decoder struct {
	state    *inflater
	limit    int64
	total    int64
	pending  []byte
	finished bool
	status   int64
	message  string
}

func NewDecoder(limit int64) any {
	state := &inflater{in: make(chan []byte), need: make(chan struct{}, 1), done: make(chan struct{})}
	go state.run()
	state.wait()
	d := &decoder{state: state, limit: limit}
	runtime.SetFinalizer(d, func(d *decoder) { d.state.stop() })
	return d
}

// collect moves new output into pending. Pending is replaced, never written
// into, so a view answered earlier keeps its bytes.
func (d *decoder) collect() {
	out := d.state.out
	d.state.out = nil
	if len(out) == 0 {
		return
	}
	d.total += int64(len(out))
	if d.total > d.limit {
		d.status, d.message = decoderTooLarge, "decompressed body exceeds limit"
		return
	}
	next := make([]byte, 0, len(d.pending)+len(out))
	d.pending = append(append(next, d.pending...), out...)
}

func (d *decoder) failed() {
	if d.status == decoderOK && d.state.err != nil {
		d.status, d.message = decoderMalformed, d.state.err.Error()
	}
}

// DecoderPush feeds compressed bytes and answers a status code.
func DecoderPush(value any, data []byte) int64 {
	d := value.(*decoder)
	if d.status != decoderOK || d.finished || len(data) == 0 {
		return d.status
	}
	select {
	case <-d.state.done:
		d.failed()
		if d.status == decoderOK {
			d.status, d.message = decoderMalformed, "data after the end of the gzip stream"
		}
		return d.status
	default:
	}
	d.state.in <- append([]byte(nil), data...)
	d.state.wait()
	d.collect()
	d.failed()
	return d.status
}

// DecoderFinish ends the input and answers a status code; a stream that stops
// early is malformed.
func DecoderFinish(value any) int64 {
	d := value.(*decoder)
	if d.finished || d.status != decoderOK {
		return d.status
	}
	d.finished = true
	d.state.stop()
	<-d.state.done
	d.collect()
	if d.status == decoderOK && d.state.err != nil {
		d.status, d.message = decoderMalformed, d.state.err.Error()
	}
	return d.status
}

func DecoderPending(value any) []byte {
	return value.(*decoder).pending
}

func DecoderSkip(value any, count int64) int64 {
	d := value.(*decoder)
	if count > int64(len(d.pending)) {
		count = int64(len(d.pending))
	}
	if count > 0 {
		d.pending = d.pending[count:]
	}
	return count
}

func DecoderMessage(value any) string {
	return value.(*decoder).message
}
