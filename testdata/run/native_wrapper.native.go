package native

import "fmt"

var names []string

func Mint(name string) int64 {
	names = append(names, name)
	return int64(len(names) - 1)
}

func Describe(token int64) string {
	return fmt.Sprintf("token %d = %s", token, names[token])
}

func Next(token int64) int64 {
	names = append(names, names[token]+"'")
	return int64(len(names) - 1)
}
