package native

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
)

type listener struct {
	value  net.Listener
	closed atomic.Bool
}

type connection struct {
	value   net.Conn
	reader  *bufio.Reader
	readMu  sync.Mutex
	writeMu sync.Mutex
	closed  atomic.Bool
}

func listenerValue(value any) (*listener, error) {
	l, ok := value.(*listener)
	if !ok || l == nil || l.closed.Load() {
		return nil, errors.New("closed listener")
	}
	return l, nil
}

func connectionValue(value any) (*connection, error) {
	c, ok := value.(*connection)
	if !ok || c == nil || c.closed.Load() {
		return nil, errors.New("closed connection")
	}
	return c, nil
}

func Listen(port int64) (any, error) {
	l, err := net.Listen("tcp", net.JoinHostPort("", strconv.FormatInt(port, 10)))
	if err != nil {
		return nil, err
	}
	return &listener{value: l}, nil
}

func CloseListener(value any) error {
	l, err := listenerValue(value)
	if err != nil {
		return err
	}
	if l.closed.Swap(true) {
		return errors.New("closed listener")
	}
	return l.value.Close()
}

func AcceptConnection(value any) (any, error) {
	l, err := listenerValue(value)
	if err != nil {
		return nil, err
	}
	c, err := l.value.Accept()
	if err != nil {
		return nil, err
	}
	return &connection{value: c, reader: bufio.NewReader(c)}, nil
}

func Dial(host string, port int64) (any, error) {
	c, err := net.Dial("tcp", net.JoinHostPort(host, strconv.FormatInt(port, 10)))
	if err != nil {
		return nil, err
	}
	return &connection{value: c, reader: bufio.NewReader(c)}, nil
}

func CloseConnection(value any) error {
	c, err := connectionValue(value)
	if err != nil {
		return err
	}
	if c.closed.Swap(true) {
		return errors.New("closed connection")
	}
	return c.value.Close()
}

// ConnectionHasInput blocks until one byte is available or the peer closes.
func ConnectionHasInput(value any) (bool, error) {
	c, err := connectionValue(value)
	if err != nil {
		return false, err
	}
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if _, err := connectionValue(c); err != nil {
		return false, err
	}
	if _, err := c.reader.Peek(1); err != nil {
		if err == io.EOF {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

const maxSocketRead = 1 << 16

func ReadConnectionBytes(value any, max int64) ([]byte, error) {
	c, err := connectionValue(value)
	if err != nil {
		return nil, err
	}
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if _, err := connectionValue(c); err != nil {
		return nil, err
	}
	if max <= 0 {
		return nil, nil
	}
	if max > maxSocketRead {
		max = maxSocketRead
	}
	buf := make([]byte, max)
	n, err := c.reader.Read(buf)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return buf[:n:n], nil
}

func WriteConnectionBytes(value any, data []byte) error {
	c, err := connectionValue(value)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := connectionValue(c); err != nil {
		return err
	}
	for len(data) > 0 {
		n, err := c.value.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("socket write made no progress")
		}
		data = data[n:]
	}
	return nil
}
