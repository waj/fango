package fangort

import (
	"fmt"
	"net"
	"syscall"
	"testing"
)

func TestClassifyNetError(t *testing.T) {
	cases := []struct {
		err  error
		kind int64
		msg  string
	}{
		{syscall.ECONNREFUSED, NetErrorConnectionRefused, "connection refused"},
		{syscall.ECONNRESET, NetErrorConnectionReset, "connection reset"},
		{syscall.EPIPE, NetErrorConnectionReset, "connection reset"},
		{syscall.EADDRINUSE, NetErrorAddressInUse, "address already in use"},
		{syscall.ETIMEDOUT, NetErrorTimedOut, "timed out"},
		{fmt.Errorf("other"), NetErrorOther, "other"},
	}
	for _, tc := range cases {
		got := ClassifyNetError(tc.err)
		if got.Kind != tc.kind || got.Message != tc.msg {
			t.Errorf("%v: got %+v", tc.err, got)
		}
	}
}

func TestClassifyNetErrorKeepsAddress(t *testing.T) {
	err := &net.OpError{Op: "dial", Net: "tcp", Addr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9000}, Err: syscall.ECONNREFUSED}
	got := ClassifyNetError(err)
	if got.Kind != NetErrorConnectionRefused || got.Path != "127.0.0.1:9000" {
		t.Fatalf("got %+v", got)
	}
}
