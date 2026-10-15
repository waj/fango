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

	"github.com/waj/fango/runtime/fangort"
)

// Sockets belong to the fangort resource provider of the Network runner that
// opened them: each system native validates the owner before projecting the
// native socket, and retirement closes what is still open. Virtual
// listeners, connections, and pools are Fango values and never reach Go.

func netProvider(value any) (*fangort.ResourceProvider, error) { return fangort.CheckedProvider(value) }

func NetFreshProvider() any  { return fangort.NewResourceProvider(false) }
func NetSystemProvider() any { return fangort.NewResourceProvider(true) }

func NetSameProvider(a, b any) bool { return a == b }

func NetProviderStatus(value any) error {
	p, err := netProvider(value)
	if err != nil {
		return err
	}
	return p.Status("")
}

func NetRetireProvider(value any) {
	if p, err := netProvider(value); err == nil {
		_ = p.Retire()
	}
}

// NetRetireSystem closes the runner's remaining sockets. Their close failures
// are ignored: a peer may already have reset them.
func NetRetireSystem(value any) {
	if p, err := netProvider(value); err == nil {
		_ = p.Retire()
	}
}

// TokenCancelled reports whether a task lifetime's context has ended.
func TokenCancelled(token any) bool {
	scope, ok := token.(*FangoAsyncScope)
	return ok && scope.Context().Err() != nil
}

func SystemListenerLabel(value any) string   { return fangort.ResourceLabel(value) }
func SystemConnectionLabel(value any) string { return fangort.ResourceLabel(value) }

// owned projects a system resource of the provider.
func owned(provider, value any, noun string, closing bool) (any, error) {
	p, err := netProvider(provider)
	if err != nil {
		return nil, err
	}
	r, err := p.Use(value, noun, closing)
	if err != nil {
		return nil, err
	}
	return r.Value, nil
}

func adoptConnection(provider any, label string, open func() (any, error)) (any, error) {
	p, err := netProvider(provider)
	if err != nil {
		return nil, err
	}
	return p.Adopt(label, func() (any, func() error, error) {
		value, err := open()
		if err != nil {
			return nil, nil, err
		}
		return value, func() error { return closeConnection(value) }, nil
	})
}

// withinTask closes the resource if the task lifetime ends during the call.
func withinTask(token any, close func()) func() bool {
	return context.AfterFunc(token.(*FangoAsyncScope).Context(), close)
}

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
	if l, ok := value.(*lease); ok {
		if l == nil || l.done.Load() {
			return nil, errors.New("closed connection")
		}
		value = l.conn
	}
	c, ok := value.(*connection)
	if !ok || c == nil || c.closed.Load() {
		return nil, errors.New("closed connection")
	}
	return c, nil
}

func listenSocket(port int64) (any, error) {
	l, err := net.Listen("tcp", net.JoinHostPort("", strconv.FormatInt(port, 10)))
	if err != nil {
		return nil, err
	}
	return &listener{value: l}, nil
}

func closeListener(value any) error {
	l, ok := value.(*listener)
	if !ok || l == nil {
		return errors.New("closed listener")
	}
	if l.closed.Swap(true) {
		return nil
	}
	return l.value.Close()
}

func listenerStopped(value any) bool {
	l, ok := value.(*listener)
	return !ok || l == nil || l.closed.Load()
}

func acceptConnection(value any) (any, error) {
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

// dialTimeout connects like a plain dial but gives up after millis; zero waits as long
// as the system does.
func dialTimeout(host string, port int64, millis int64) (any, error) {
	dialer := net.Dialer{Timeout: time.Duration(millis) * time.Millisecond}
	c, err := dialer.Dial("tcp", net.JoinHostPort(host, strconv.FormatInt(port, 10)))
	if err != nil {
		return nil, err
	}
	return &connection{value: c, reader: bufio.NewReader(c)}, nil
}

// dialTls connects and completes a TLS handshake within millis, verifying the
// server's certificate for host against the system roots plus any in the PEM
// file at rootsPath.
func dialTls(host string, port int64, millis int64, rootsPath string) (any, error) {
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

func closeConnection(value any) error {
	if l, ok := value.(*lease); ok && l != nil {
		if l.done.Swap(true) {
			return nil
		}
		value = l.conn
	}
	c, ok := value.(*connection)
	if !ok || c == nil {
		return errors.New("closed connection")
	}
	if c.closed.Swap(true) {
		return nil
	}
	return c.value.Close()
}

const maxSocketRead = 1 << 16

func readConnectionBytes(value any, max int64) ([]byte, error) {
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

func writeConnectionBytes(value any, data []byte) error {
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

func setReadDeadline(value any, millis int64) error {
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

func setWriteDeadline(value any, millis int64) error {
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

// A pool keeps idle client connections per endpoint. Each checkout is a fresh
// lease, so releasing a lease twice, or after its connection went back to the
// pool and out again, never touches the connection's next user.
type pool struct {
	mu      sync.Mutex
	idle    map[string][]idleConnection
	maxIdle int
	timeout time.Duration
	closed  bool
}

type idleConnection struct {
	conn  *connection
	since time.Time
}

type lease struct {
	conn   *connection
	pool   *pool
	key    string
	reused bool
	done   atomic.Bool
}

func newPool(maxIdle int64, idleMillis int64) any {
	return &pool{idle: map[string][]idleConnection{}, maxIdle: int(maxIdle), timeout: time.Duration(idleMillis) * time.Millisecond}
}

func poolKey(host string, port int64, secure bool, roots string) string {
	return fmt.Sprintf("%t|%s|%d|%s", secure, host, port, roots)
}

// alive reports whether an idle connection still looks open: the peer has
// not closed it and sent nothing unasked.
func alive(c *connection) bool {
	if err := c.value.SetReadDeadline(time.Now().Add(time.Millisecond)); err != nil {
		return false
	}
	_, err := c.reader.Peek(1)
	_ = c.value.SetReadDeadline(time.Time{})
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func (p *pool) take(key string) *connection {
	p.mu.Lock()
	defer p.mu.Unlock()
	for {
		list := p.idle[key]
		if len(list) == 0 {
			return nil
		}
		last := list[len(list)-1]
		p.idle[key] = list[:len(list)-1]
		if time.Since(last.since) < p.timeout && alive(last.conn) {
			return last.conn
		}
		_ = closeRaw(last.conn)
	}
}

func closeRaw(c *connection) error {
	if c.closed.Swap(true) {
		return nil
	}
	return c.value.Close()
}

// poolConnect leases an idle connection to the endpoint, unless fresh, or
// dials a new one as DialTimeout or DialTls would.
func poolConnect(value any, host string, port int64, millis int64, secure bool, roots string, fresh bool) (any, error) {
	p := value.(*pool)
	key := poolKey(host, port, secure, roots)
	if !fresh {
		if c := p.take(key); c != nil {
			return &lease{conn: c, pool: p, key: key, reused: true}, nil
		}
	}
	var dialed any
	var err error
	if secure {
		dialed, err = dialTls(host, port, millis, roots)
	} else {
		dialed, err = dialTimeout(host, port, millis)
	}
	if err != nil {
		return nil, err
	}
	return &lease{conn: dialed.(*connection), pool: p, key: key}, nil
}

// poolRelease ends a lease: a reusable connection goes back to the pool,
// anything else is closed. Later releases of the same lease do nothing.
func poolRelease(value any, reusable bool) error {
	l := value.(*lease)
	if l.done.Swap(true) {
		return nil
	}
	c := l.conn
	if reusable && !c.closed.Load() {
		p := l.pool
		p.mu.Lock()
		if !p.closed && len(p.idle[l.key]) < p.maxIdle {
			_ = c.value.SetDeadline(time.Time{})
			p.idle[l.key] = append(p.idle[l.key], idleConnection{conn: c, since: time.Now()})
			p.mu.Unlock()
			return nil
		}
		p.mu.Unlock()
	}
	return closeRaw(c)
}

func connectionReused(value any) bool {
	l, ok := value.(*lease)
	return ok && l.reused
}

// closePool closes every idle connection; leases still out close on release.
func closePool(value any) error {
	p := value.(*pool)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	for key, list := range p.idle {
		for _, idle := range list {
			_ = closeRaw(idle.conn)
		}
		delete(p.idle, key)
	}
	return nil
}

func SystemListen(provider any, port int64) (any, error) {
	p, err := netProvider(provider)
	if err != nil {
		return nil, err
	}
	return p.Adopt(":"+strconv.FormatInt(port, 10), func() (any, func() error, error) {
		l, err := listenSocket(port)
		if err != nil {
			return nil, nil, err
		}
		return l, func() error { return closeListener(l) }, nil
	})
}

func SystemStopListener(provider, value any) error {
	l, err := owned(provider, value, "listener", true)
	if err != nil {
		return err
	}
	return closeListener(l)
}

// SystemListenerStopped answers true for a listener this runner cannot use.
func SystemListenerStopped(provider, value any) bool {
	l, err := owned(provider, value, "listener", false)
	return err != nil || listenerStopped(l)
}

func SystemAccept(provider, value any) (any, error) {
	l, err := owned(provider, value, "listener", false)
	if err != nil {
		return nil, err
	}
	return adoptConnection(provider, fangort.ResourceLabel(value), func() (any, error) { return acceptConnection(l) })
}

// SystemAcceptWithin closes the listener if the task lifetime ends while it
// waits, as every waiting worker shares it.
func SystemAcceptWithin(token, provider, value any) (any, error) {
	l, err := owned(provider, value, "listener", false)
	if err != nil {
		return nil, err
	}
	stop := withinTask(token, func() { _ = closeListener(l) })
	defer stop()
	return adoptConnection(provider, fangort.ResourceLabel(value), func() (any, error) { return acceptConnection(l) })
}

func SystemConnect(provider any, host string, port int64, millis int64, secure bool, roots string) (any, error) {
	return adoptConnection(provider, net.JoinHostPort(host, strconv.FormatInt(port, 10)), func() (any, error) {
		if secure {
			return dialTls(host, port, millis, roots)
		}
		return dialTimeout(host, port, millis)
	})
}

func SystemCloseConnection(provider, value any) error {
	c, err := owned(provider, value, "connection", true)
	if err != nil {
		return err
	}
	return closeConnection(c)
}

func SystemRead(provider, value any, max int64) ([]byte, error) {
	c, err := owned(provider, value, "connection", false)
	if err != nil {
		return nil, err
	}
	return readConnectionBytes(c, max)
}

func SystemReadWithin(token, provider, value any, max int64) ([]byte, error) {
	c, err := owned(provider, value, "connection", false)
	if err != nil {
		return nil, err
	}
	stop := withinTask(token, func() { _ = closeConnection(c) })
	defer stop()
	return readConnectionBytes(c, max)
}

func SystemWrite(provider, value any, data []byte) error {
	c, err := owned(provider, value, "connection", false)
	if err != nil {
		return err
	}
	return writeConnectionBytes(c, data)
}

func SystemWriteWithin(token, provider, value any, data []byte) error {
	c, err := owned(provider, value, "connection", false)
	if err != nil {
		return err
	}
	stop := withinTask(token, func() { _ = closeConnection(c) })
	defer stop()
	return writeConnectionBytes(c, data)
}

func SystemSetReadDeadline(provider, value any, millis int64) error {
	c, err := owned(provider, value, "connection", false)
	if err != nil {
		return err
	}
	return setReadDeadline(c, millis)
}

func SystemSetWriteDeadline(provider, value any, millis int64) error {
	c, err := owned(provider, value, "connection", false)
	if err != nil {
		return err
	}
	return setWriteDeadline(c, millis)
}

// SystemNewPool answers a closed pool once the runner has retired, so its
// connects fail with the provider error rather than dialing.
func SystemNewPool(provider any, maxIdle int64, idleMillis int64) any {
	p, err := netProvider(provider)
	if err != nil {
		panic(err)
	}
	r, err := p.Adopt("", func() (any, func() error, error) {
		pl := newPool(maxIdle, idleMillis)
		return pl, func() error { return closePool(pl) }, nil
	})
	if err != nil {
		return p.Retired("")
	}
	return r
}

// SystemPoolConnect leases under the pool's own provider, so idle
// connections never cross runners.
func SystemPoolConnect(provider, value any, host string, port int64, millis int64, secure bool, roots string, fresh bool) (any, error) {
	pl, err := owned(provider, value, "pool", false)
	if err != nil {
		return nil, err
	}
	return adoptConnection(provider, net.JoinHostPort(host, strconv.FormatInt(port, 10)), func() (any, error) {
		return poolConnect(pl, host, port, millis, secure, roots, fresh)
	})
}

// SystemPoolRelease ends a lease, or closes a connection that was not leased.
func SystemPoolRelease(provider, value any, reusable bool) error {
	c, err := owned(provider, value, "connection", true)
	if err != nil {
		return err
	}
	if _, leased := c.(*lease); leased {
		return poolRelease(c, reusable)
	}
	return closeConnection(c)
}

func SystemConnectionReused(provider, value any) bool {
	c, err := owned(provider, value, "connection", false)
	return err == nil && connectionReused(c)
}

func SystemClosePool(provider, value any) error {
	pl, err := owned(provider, value, "pool", true)
	if err != nil {
		return err
	}
	return closePool(pl)
}
