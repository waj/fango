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

func Exit(code int64) { FangoHost.Exit(int(code)) }
