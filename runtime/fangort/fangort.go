// Package fangort is the fango runtime support package. It is embedded into
// the compiler binary and materialized into every build directory; generated
// code imports it, and the interpreter and REPL import it directly — one
// shared formatting implementation across both backends, by construction.
package fangort

import (
	"fmt"
	"strconv"
)

// ShowInt renders an Int exactly as the surface language shows it.
func ShowInt(v int64) string { return strconv.FormatInt(v, 10) }

// PrintInt writes ShowInt(v) and a newline to stdout. Used by the
// test-internal print-main codegen mode until `print` lands in S1.
func PrintInt(v int64) { fmt.Println(ShowInt(v)) }
