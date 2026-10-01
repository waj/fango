package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/waj/fango/internal/llvmbuild"
)

func llvmCheck(entry string, stderr io.Writer, session *compilationSession, report *reporter) int {
	session.nativeC = true
	result, ok := checkGraph(entry, stderr, session)
	if !ok {
		return 1
	}
	start := time.Now()
	err := llvmbuild.Check(entry, result)
	report.phase("LLVM check", start)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	report.finish("")
	return 0
}

func llvmBuilt(entry string, stderr io.Writer, session *compilationSession, report *reporter) (string, bool) {
	if session == nil {
		session = &compilationSession{}
	}
	session.nativeC = true
	result, ok := checkGraph(entry, stderr, session)
	if !ok {
		return "", false
	}
	start := time.Now()
	binary, err := llvmbuild.BuildWithOptions(entry, result, os.Getenv("FANGO_INTERNAL_PRINT_MAIN") == "1", session.noCache)
	report.phase("LLVM build", start)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return "", false
	}
	return binary, true
}
func llvmBuild(entry, out string, stderr io.Writer, session *compilationSession, report *reporter) int {
	binary, ok := llvmBuilt(entry, stderr, session, report)
	if !ok {
		return 1
	}
	if out == "" {
		out = strings.TrimSuffix(filepath.Base(entry), ".fango")
	}
	data, err := os.ReadFile(binary)
	if err == nil {
		err = os.WriteFile(out, data, 0755)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	report.finish(out)
	return 0
}
func llvmRun(entry string, args []string, stderr io.Writer, session *compilationSession, report *reporter) int {
	binary, ok := llvmBuilt(entry, stderr, session, report)
	if !ok {
		return 1
	}
	report.finish(binary)
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	cmd := exec.Command(binary, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode()
		}
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
