package native

import (
	"bytes"
	"compress/gzip"
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
