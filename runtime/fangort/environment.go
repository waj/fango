package fangort

import (
	"fmt"
	"strings"
)

// EnvironmentEntry splits the index'th "NAME=value" entry of an environment
// list. The search for "=" starts after the first byte, so a Windows
// "=C:=C:\dir" entry keeps "=C:" as its name.
func EnvironmentEntry(env []string, index int64) (string, string) {
	if index < 0 || index >= int64(len(env)) {
		panic(fmt.Sprintf("environment index %d is out of range", index))
	}
	return splitEnvironmentEntry(env[index])
}

// LookupEnvironment answers the value of the last entry named name, as the
// variables dictionary keeps the last of duplicate names.
func LookupEnvironment(env []string, name string) (string, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		if entryName, value := splitEnvironmentEntry(env[i]); entryName == name {
			return value, true
		}
	}
	return "", false
}

func splitEnvironmentEntry(entry string) (string, string) {
	if len(entry) > 0 {
		if i := strings.IndexByte(entry[1:], '='); i >= 0 {
			return entry[:i+1], entry[i+2:]
		}
	}
	return entry, ""
}
