package native

import (
	"bufio"
	"fmt"
	"io"

	"github.com/waj/fango/runtime/fangort"
)

func PrintTo(w io.Writer, text string) error {
	_, err := fmt.Fprintln(w, text)
	return err
}

func ReadLineFrom(r *bufio.Reader) (string, error) { return fangort.ReadLineFrom(r) }

func WriteTo(w io.Writer, text string) error { return fangort.WriteStringTo(w, text) }
