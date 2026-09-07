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

func HasInputFrom(r *bufio.Reader) (bool, error) { return fangort.HasInputFrom(r) }

func ReadRawLineFrom(r *bufio.Reader) (string, error) { return fangort.ReadRawLineFrom(r) }

func LineText(text string) string { return fangort.LineText(text) }

func LineEnding(text string) string { return fangort.LineEnding(text) }

func WriteTo(w io.Writer, text string) error { return fangort.WriteStringTo(w, text) }
