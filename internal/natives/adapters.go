package natives

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/waj/fango/runtime/fangort"
)

// evalBasics implements the interpreter half of the inline Basics templates.
// equal supplies structural equality for interpreter ADT values.
func evalBasics(name string, left, right any, equal func(any, any) bool) any {
	switch l := left.(type) {
	case int64:
		r := right.(int64)
		switch name {
		case "add":
			return l + r
		case "sub":
			return l - r
		case "mul":
			return l * r
		case "eq":
			return l == r
		case "neq":
			return l != r
		case "lt":
			return l < r
		case "gt":
			return l > r
		case "le":
			return l <= r
		case "ge":
			return l >= r
		}
	case float64:
		r := right.(float64)
		switch name {
		case "add":
			return l + r
		case "sub":
			return l - r
		case "mul":
			return l * r
		case "fdiv":
			return l / r
		case "eq":
			return l == r
		case "neq":
			return l != r
		case "lt":
			return l < r
		case "gt":
			return l > r
		case "le":
			return l <= r
		case "ge":
			return l >= r
		}
	case string:
		r := right.(string)
		switch name {
		case "append":
			return l + r
		case "eq":
			return l == r
		case "neq":
			return l != r
		case "lt":
			return l < r
		case "gt":
			return l > r
		case "le":
			return l <= r
		case "ge":
			return l >= r
		}
	case bool:
		r := right.(bool)
		if name == "eq" {
			return l == r
		}
		if name == "neq" {
			return l != r
		}
	}
	if name == "eq" {
		return equal(left, right)
	}
	if name == "neq" {
		return !equal(left, right)
	}
	panic("invalid Basics native application: " + name)
}

func printTo(w io.Writer, text string) error {
	_, err := fmt.Fprintln(w, text)
	return err
}

func hasInputFrom(r *bufio.Reader) (bool, error) { return fangort.HasInputFrom(r) }

func readRawLineFrom(r *bufio.Reader) (string, error) { return fangort.ReadRawLineFrom(r) }

func lineText(text string) string { return fangort.LineText(text) }

func lineEnding(text string) string { return fangort.LineEnding(text) }

func writeTo(w io.Writer, text string) error { return fangort.WriteStringTo(w, text) }

func argAt(args []string, index int64) (string, error) {
	if index < 0 || index >= int64(len(args)) {
		return "", fmt.Errorf("argument index %d is out of range", index)
	}
	return args[index], nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

func readFileText(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.ToValidUTF8(string(data), "\uFFFD"), nil
}

func writeFileText(path, text string) error { return os.WriteFile(path, []byte(text), 0o644) }
