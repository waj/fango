package native

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/waj/fango/runtime/fangort"
)

func PrintTo(w io.Writer, text string) error {
	_, err := fmt.Fprintln(w, text)
	return err
}

func ReadLineFrom(r *bufio.Reader) (string, error) { return fangort.ReadLineFrom(r) }

func HasInputFrom(r *bufio.Reader) (bool, error) { return fangort.HasInputFrom(r) }

func ReadRawLineFrom(r *bufio.Reader) (string, error) { return fangort.ReadRawLineFrom(r) }

func LineText(text string) string { return fangort.LineText(text) }

func LineEnding(text string) string { return fangort.LineEnding(text) }

func WriteTo(w io.Writer, text string) error { return fangort.WriteStringTo(w, text) }

func ArgAt(args []string, index int64) (string, error) {
	if index < 0 || index >= int64(len(args)) {
		return "", fmt.Errorf("argument index %d is out of range", index)
	}
	return args[index], nil
}

func PathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

func ReadFileText(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.ToValidUTF8(string(data), "\uFFFD"), nil
}

func WriteFileText(path, text string) error { return os.WriteFile(path, []byte(text), 0o644) }
