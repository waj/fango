package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// The integration harness defaults to both backends for direct go test runs.
// make test selects compiled execution; make ci explicitly selects both.
// Interpreter-specific Go unit tests are independent of this selection.
const testBackendsEnv = "FANGO_TEST_BACKENDS"

type testBackendMode string

const (
	bothTestBackends    testBackendMode = "both"
	compiledTestBackend testBackendMode = "compiled"
)

func parseTestBackendMode(value string) (testBackendMode, error) {
	switch value {
	case "", "both":
		return bothTestBackends, nil
	case "compiled":
		return compiledTestBackend, nil
	default:
		return "", fmt.Errorf("%s must be both or compiled, got %q", testBackendsEnv, value)
	}
}

func selectedTestBackends(mode testBackendMode, short bool) []string {
	// Short mode preserves the existing interpreter-only development checks.
	if short {
		return []string{"interpreter"}
	}
	if mode == compiledTestBackend {
		return []string{"compiled"}
	}
	return []string{"interpreter", "compiled"}
}

func executionTestBackends() []string {
	return selectedTestBackends(executionBackendMode(), testing.Short())
}

func interpreterTestsEnabled() bool {
	return testing.Short() || executionBackendMode() == bothTestBackends
}

func executionBackendMode() testBackendMode {
	// Read during test execution so Go's test cache records the selection.
	// Reading only in TestMain would let CI reuse a compiled-only test result.
	mode, err := parseTestBackendMode(os.Getenv(testBackendsEnv))
	if err != nil {
		panic(err) // TestMain already rejects invalid configuration before tests.
	}
	return mode
}

func TestExecutionBackendSelection(t *testing.T) {
	// Track the configured mode even when this selection test runs on its own.
	_ = executionBackendMode()
	for _, tc := range []struct {
		value string
		short bool
		want  string
	}{
		{"", false, "interpreter,compiled"},
		{"both", false, "interpreter,compiled"},
		{"compiled", false, "compiled"},
		{"", true, "interpreter"},
		{"both", true, "interpreter"},
		{"compiled", true, "interpreter"},
	} {
		mode, err := parseTestBackendMode(tc.value)
		if err != nil {
			t.Fatal(err)
		}
		got := selectedTestBackends(mode, tc.short)
		joined := strings.Join(got, ",")
		if joined != tc.want {
			t.Errorf("mode %q, short %v: %s, want %s", tc.value, tc.short, joined, tc.want)
		}
	}
	if _, err := parseTestBackendMode("complied"); err == nil {
		t.Fatal("invalid backend selection silently accepted")
	}
}
