package fangort

import (
	"errors"
	"net"
	"syscall"
)

// Net.Kind constructors, in declaration order.
const (
	NetErrorConnectionRefused int64 = iota
	NetErrorConnectionReset
	NetErrorAddressInUse
	NetErrorTimedOut
	NetErrorOther
)

// ClassifyNetError maps socket failures to Net.Error's stable kind and keeps
// the endpoint involved in the operation when net exposes it.
func ClassifyNetError(err error) IOFailure {
	failure := IOFailure{Kind: NetErrorOther, Message: err.Error()}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if opErr.Addr != nil {
			failure.Path = opErr.Addr.String()
		} else if opErr.Source != nil {
			failure.Path = opErr.Source.String()
		}
	}
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		failure.Kind, failure.Message = NetErrorConnectionRefused, "connection refused"
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE):
		failure.Kind, failure.Message = NetErrorConnectionReset, "connection reset"
	case errors.Is(err, syscall.EADDRINUSE):
		failure.Kind, failure.Message = NetErrorAddressInUse, "address already in use"
	case errors.Is(err, syscall.ETIMEDOUT), isTimeout(err):
		failure.Kind, failure.Message = NetErrorTimedOut, "timed out"
	}
	return failure
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
