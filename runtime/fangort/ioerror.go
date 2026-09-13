package fangort

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// IOFailure is the classified form of an operating-system failure returned by
// a fallible native. It is the compiler's contract with the bundled IO module:
// Kind indexes IO.Kind's constructors in declaration order, Path is the path
// the program supplied, and Message is a stable reason for a recognized Kind
// or the underlying text for Other. Generated code and the interpreter's
// native worker both build IO.Error from these scalars, so the classification
// happens in exactly one place.
type IOFailure struct {
	Kind    int64
	Path    string
	Message string
}

// IO.Kind constructors, in declaration order.
const (
	IOErrorNotFound int64 = iota
	IOErrorPermissionDenied
	IOErrorAlreadyExists
	IOErrorIsDirectory
	IOErrorNotDirectory
	IOErrorOther
)

// ClassifyIOError maps a Go error to an IOFailure. The kinds a fango program
// can act on are recognized through the portable sentinels and the two errno
// values that portable sentinels do not cover; everything else is Other with
// its underlying error text preserved.
func ClassifyIOError(err error) IOFailure {
	failure := IOFailure{Kind: IOErrorOther, Message: err.Error()}
	var pathErr *fs.PathError
	var linkErr *os.LinkError
	switch {
	case errors.As(err, &pathErr):
		failure.Path = pathErr.Path
		failure.Message = pathErr.Err.Error()
	case errors.As(err, &linkErr):
		failure.Path = linkErr.Old
		failure.Message = linkErr.Err.Error()
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		failure.Kind = IOErrorNotFound
	case errors.Is(err, fs.ErrPermission):
		failure.Kind = IOErrorPermissionDenied
	case errors.Is(err, fs.ErrExist):
		failure.Kind = IOErrorAlreadyExists
	case errors.Is(err, syscall.EISDIR):
		failure.Kind = IOErrorIsDirectory
	case errors.Is(err, syscall.ENOTDIR):
		failure.Kind = IOErrorNotDirectory
	}
	if failure.Kind != IOErrorOther {
		failure.Message = ioErrorMessage(failure.Kind)
	}
	return failure
}

func ioErrorMessage(kind int64) string {
	switch kind {
	case IOErrorNotFound:
		return "no such file or directory"
	case IOErrorPermissionDenied:
		return "permission denied"
	case IOErrorAlreadyExists:
		return "file exists"
	case IOErrorIsDirectory:
		return "is a directory"
	case IOErrorNotDirectory:
		return "not a directory"
	}
	return ""
}
