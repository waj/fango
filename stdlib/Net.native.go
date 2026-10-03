package native

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
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
	l, ok := value.(*listener)
	if !ok || l == nil {
		return errors.New("closed listener")
	}
	if l.closed.Swap(true) {
		return nil
	}
	return l.value.Close()
}

func ListenerStopped(value any) bool {
	l, ok := value.(*listener)
	return !ok || l == nil || l.closed.Load()
}

func AcceptConnectionAsync(token, value any) (any, error) {
	scope := token.(*FangoAsyncScope)
	ctx := scope.Context()
	stop := context.AfterFunc(ctx, func() { _ = CloseListener(value) })
	defer stop()
	return AcceptConnection(value)
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

// DialTimeout connects like Dial but gives up after millis; zero waits as long
// as the system does.
func DialTimeout(host string, port int64, millis int64) (any, error) {
	dialer := net.Dialer{Timeout: time.Duration(millis) * time.Millisecond}
	c, err := dialer.Dial("tcp", net.JoinHostPort(host, strconv.FormatInt(port, 10)))
	if err != nil {
		return nil, err
	}
	return &connection{value: c, reader: bufio.NewReader(c)}, nil
}

// DialTls connects and completes a TLS handshake within millis, verifying the
// server's certificate for host against the system roots plus any in the PEM
// file at rootsPath.
func DialTls(host string, port int64, millis int64, rootsPath string) (any, error) {
	config := &tls.Config{ServerName: host}
	if rootsPath != "" {
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		pem, err := os.ReadFile(rootsPath)
		if err != nil {
			return nil, err
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates in %s", rootsPath)
		}
		config.RootCAs = roots
	}
	ctx := context.Background()
	if millis > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(millis)*time.Millisecond)
		defer cancel()
	}
	dialer := tls.Dialer{Config: config}
	c, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.FormatInt(port, 10)))
	if err != nil {
		return nil, err
	}
	return &connection{value: c, reader: bufio.NewReader(c)}, nil
}

func CloseConnection(value any) error {
	c, ok := value.(*connection)
	if !ok || c == nil {
		return errors.New("closed connection")
	}
	if c.closed.Swap(true) {
		return nil
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
	// Peek fills bufio's reusable socket buffer. Allocate only the bytes that
	// arrived, since the returned Bytes must own immutable storage. Allocating
	// max for every short HTTP request wastes almost all of an 8192-byte slice.
	if _, err := c.reader.Peek(1); err != nil {
		if err == io.EOF {
			return nil, nil
		}
		return nil, err
	}
	available := int64(c.reader.Buffered())
	if available > max {
		available = max
	}
	buf := make([]byte, available)
	if _, err := io.ReadFull(c.reader, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func ReadConnectionBytesAsync(token, value any, max int64) ([]byte, error) {
	scope := token.(*FangoAsyncScope)
	stop := context.AfterFunc(scope.Context(), func() { _ = CloseConnection(value) })
	defer stop()
	return ReadConnectionBytes(value, max)
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

func WriteConnectionBytesAsync(token, value any, data []byte) error {
	scope := token.(*FangoAsyncScope)
	stop := context.AfterFunc(scope.Context(), func() { _ = CloseConnection(value) })
	defer stop()
	return WriteConnectionBytes(value, data)
}

func SetReadDeadline(value any, millis int64) error {
	c, err := connectionValue(value)
	if err != nil {
		return err
	}
	deadline := time.Time{}
	if millis > 0 {
		deadline = time.Now().Add(time.Duration(millis) * time.Millisecond)
	}
	return c.value.SetReadDeadline(deadline)
}

func SetWriteDeadline(value any, millis int64) error {
	c, err := connectionValue(value)
	if err != nil {
		return err
	}
	deadline := time.Time{}
	if millis > 0 {
		deadline = time.Now().Add(time.Duration(millis) * time.Millisecond)
	}
	return c.value.SetWriteDeadline(deadline)
}
