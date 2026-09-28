package testutil

import (
	"fmt"
	"strings"
)

// CaptureGraph is a small source-ordered diamond graph with a recursive edge
// through its enclosing function. Runtime takes only the base case; analysis
// must still visit every path. The effectful variant is also checked as an
// exported root with abstract evidence, before main supplies Fail.attempt.
func CaptureGraph(depth, literal int, effectful bool) string {
	var b strings.Builder
	if effectful {
		b.WriteString("import Fail exposing (Fail, attempt, fail)\n\n")
	}
	b.WriteString("type Token = Stop | More Token\n\n")
	if effectful {
		b.WriteString("walk : Token ->{Fail String} Int\n")
	} else {
		b.WriteString("walk : Token -> Int\n")
	}
	b.WriteString("walk token =\n")
	if effectful {
		b.WriteString("    h0 : Token ->{Fail String} Int\n")
	}
	b.WriteString("    h0 value =\n        case value of\n            Stop -> ")
	if effectful {
		fmt.Fprintf(&b, "if True then %d else fail \"stop\"\n", literal)
	} else {
		fmt.Fprintf(&b, "%d\n", literal)
	}
	b.WriteString("            More rest -> walk rest\n")
	for i := 1; i <= depth; i++ {
		if effectful {
			fmt.Fprintf(&b, "    h%d : Token ->{Fail String} Int\n", i)
		}
		fmt.Fprintf(&b, "    h%d value =\n        case value of\n            Stop -> h%d value\n            More rest -> h%d rest\n", i, i-1, i-1)
	}
	if effectful {
		fmt.Fprintf(&b, "    case token of\n        Stop -> h%d token\n        More _ -> fail \"stop\"\n\nmain = attempt { _ -> walk Stop }\n", depth)
	} else {
		fmt.Fprintf(&b, "    h%d token\n\nmain = walk Stop\n", depth)
	}
	return b.String()
}
