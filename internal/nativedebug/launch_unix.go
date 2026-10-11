//go:build darwin || linux

package nativedebug

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/rpc/jsonrpc"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// Run owns one Delve child and its debuggee. Target stdin is deliberately EOF;
// the terminal input belongs to the debugger, and program output is forwarded.
func Run(binary string, args []string, input io.Reader, output, stderr io.Writer) error {
	var mu sync.Mutex
	output = lockedWriter{output, &mu}
	stderr = lockedWriter{stderr, &mu}
	binary, err := filepath.Abs(binary)
	if err != nil {
		return err
	}
	manifest, err := ReadManifest(binary)
	if err != nil {
		return err
	}
	dlv, err := exec.LookPath("dlv")
	if err != nil {
		return fmt.Errorf("Delve is required; enter nix develop or install dlv")
	}
	dir, err := os.MkdirTemp("/tmp", "fango-dlv-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "rpc")
	argv := []string{"exec", binary, "--headless", "--api-version=2", "--listen=unix:" + socket}
	if len(args) > 0 {
		argv = append(argv, "--")
		argv = append(argv, args...)
	}
	cmd := exec.Command(dlv, argv...)
	cmd.Stdout, cmd.Stderr = output, stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	defer signal.Stop(signals)
	var conn net.Conn
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
connecting:
	for {
		select {
		case <-signals:
			return fmt.Errorf("debugger launch interrupted")
		case err := <-done:
			done <- err
			return fmt.Errorf("Delve exited before connecting: %v", err)
		case <-deadline.C:
			return fmt.Errorf("Delve launch timed out; check host debugger permissions and its diagnostics above")
		case <-tick.C:
			conn, err = net.DialTimeout("unix", socket, 50*time.Millisecond)
			if err == nil {
				break connecting
			}
		}
	}
	client := jsonrpc.NewClient(conn)
	defer client.Close()
	c := &Controller{RPC: client, Manifest: manifest}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-signals:
				// Halt is safe concurrently with a pending Continue RPC. Do not consume
				// this stop in the source-stepping controller.
				_ = c.Interrupt()
			}
		}
	}()
	if err := c.Start(); err != nil {
		return err
	}
	return c.Prompt(input, output)
}

type lockedWriter struct {
	writer io.Writer
	mu     *sync.Mutex
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(p)
}
