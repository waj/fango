package native

import "fmt"

func ArgCount() int64 { return int64(len(FangoHost.Arguments())) }

func ArgAt(index int64) string {
	args := FangoHost.Arguments()
	if index < 0 || index >= int64(len(args)) {
		panic(fmt.Sprintf("argument index %d is out of range", index))
	}
	return args[index]
}

func ExitWith(code int64) { FangoHost.Exit(int(code)) }

// CheckedStatus faults on a status a process cannot report, rather than
// letting the host truncate it.
func CheckedStatus(code int64) int64 {
	if code < 0 || code > 255 {
		panic(fmt.Sprintf("exit status %d is out of range 0 through 255", code))
	}
	return code
}
