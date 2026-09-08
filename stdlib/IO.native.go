package native

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func HasInput() bool {
	ok, err := FangoHost.HasInput()
	if err != nil {
		panic(err)
	}
	return ok
}

func ReadRawLine() string {
	b, err := FangoHost.ReadInputLine()
	if err != nil && err != io.EOF {
		panic(err)
	}
	return strings.ToValidUTF8(string(b), "\uFFFD")
}

func Write(text string) {
	if err := FangoHost.WriteOutput([]byte(text)); err != nil {
		panic(err)
	}
}

func ArgCount() int64 { return int64(len(FangoHost.Arguments())) }

func ArgAt(index int64) string {
	args := FangoHost.Arguments()
	if index < 0 || index >= int64(len(args)) {
		panic(fmt.Sprintf("argument index %d is out of range", index))
	}
	return args[index]
}

func nativePath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(FangoHost.WorkingDirectory(), path)
}

func PathExists(path string) bool {
	_, err := os.Stat(nativePath(path))
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		panic(err)
	}
	return true
}

func ReadFileText(path string) string {
	data, err := os.ReadFile(nativePath(path))
	if err != nil {
		panic(err)
	}
	return strings.ToValidUTF8(string(data), "\uFFFD")
}

func WriteFile(path, text string) {
	if err := os.WriteFile(nativePath(path), []byte(text), 0o644); err != nil {
		panic(err)
	}
}

func Exit(code int64) { FangoHost.Exit(int(code)) }

func LineEnding(s string) string {
	if strings.HasSuffix(s, "\r\n") {
		return "\r\n"
	}
	if strings.HasSuffix(s, "\n") {
		return "\n"
	}
	return ""
}

func LineText(s string) string { return strings.TrimSuffix(s, LineEnding(s)) }
