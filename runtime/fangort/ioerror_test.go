package fangort

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestClassifyIOErrorKinds(t *testing.T) {
	cases := []struct {
		name         string
		err          error
		want         IOFailure
		checkMessage bool
	}{
		{"not found", &fs.PathError{Op: "open", Path: "in.txt", Err: syscall.ENOENT},
			IOFailure{Kind: IOErrorNotFound, Path: "in.txt", Message: "no such file or directory"}, true},
		{"permission", &fs.PathError{Op: "open", Path: "secret", Err: syscall.EACCES},
			IOFailure{Kind: IOErrorPermissionDenied, Path: "secret", Message: "permission denied"}, true},
		{"exists", &fs.PathError{Op: "open", Path: "out", Err: syscall.EEXIST},
			IOFailure{Kind: IOErrorAlreadyExists, Path: "out", Message: "file exists"}, true},
		{"is directory", &fs.PathError{Op: "read", Path: "dir", Err: syscall.EISDIR},
			IOFailure{Kind: IOErrorIsDirectory, Path: "dir", Message: "is a directory"}, true},
		{"not directory", &fs.PathError{Op: "open", Path: "a.txt/b", Err: syscall.ENOTDIR},
			IOFailure{Kind: IOErrorNotDirectory, Path: "a.txt/b", Message: "not a directory"}, true},
		{"sentinel without path", fs.ErrNotExist,
			IOFailure{Kind: IOErrorNotFound, Message: "no such file or directory"}, true},
		{"link error", &os.LinkError{Op: "rename", Old: "a", New: "b", Err: syscall.EXDEV},
			IOFailure{Kind: IOErrorOther, Path: "a"}, false},
		{"other", &fs.PathError{Op: "read", Path: "x", Err: errors.New("boom")},
			IOFailure{Kind: IOErrorOther, Path: "x", Message: "boom"}, true},
	}
	for _, tc := range cases {
		got := ClassifyIOError(tc.err)
		if got.Kind != tc.want.Kind || got.Path != tc.want.Path || tc.checkMessage && got.Message != tc.want.Message {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// Real operating-system failures classify the same way as the synthetic ones,
// and the two directory cases the fixtures rely on hold on this host.
func TestClassifyIOErrorFromOS(t *testing.T) {
	dir := t.TempDir()
	if _, err := os.Open(filepath.Join(dir, "missing.txt")); err == nil {
		t.Fatal("expected open of a missing file to fail")
	} else if got := ClassifyIOError(err); got.Kind != IOErrorNotFound {
		t.Errorf("missing file: got %+v", got)
	}
	f, err := os.Open(dir)
	if err != nil {
		t.Fatalf("open directory: %v", err)
	}
	defer f.Close()
	if _, err := f.Read(make([]byte, 1)); err == nil {
		t.Fatal("expected reading a directory to fail")
	} else if got := ClassifyIOError(err); got.Kind != IOErrorIsDirectory {
		t.Errorf("read directory: got %+v", got)
	}
	regular := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(regular, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Open(filepath.Join(regular, "child")); err == nil {
		t.Fatal("expected a path through a regular file to fail")
	} else if got := ClassifyIOError(err); got.Kind != IOErrorNotDirectory {
		t.Errorf("path through file: got %+v", got)
	}
}
