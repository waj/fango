package native

import "github.com/waj/fango/runtime/fangort"

func Panic(message string)                       { panic(message) }
func InputHasBytes() (bool, error)               { return fangort.ConsoleHasInput(FangoHost) }
func ReadInputLine() (string, error)             { return fangort.ReadConsoleLine(FangoHost) }
func ReadInputBytes(count int64) ([]byte, error) { return fangort.ReadConsoleBytes(FangoHost, count) }
func WriteOutputBytes(endpoint int64, data []byte) error {
	return fangort.WriteConsoleBytes(FangoHost, endpoint, data)
}
