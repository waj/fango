//go:build !darwin && !linux

package nativedebug

import (
	"fmt"
	"io"
)

func Run(binary string, args []string, input io.Reader, output, stderr io.Writer) error {
	return fmt.Errorf("native source debugging is supported on macOS and Linux")
}
