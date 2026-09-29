package main

import (
	"bytes"
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestLSPCommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, cliBinary(t), "lsp")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("fango lsp with closed stdin: %v; stderr: %s", err, stderr.String())
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("fango lsp with closed stdin wrote stdout %q, stderr %q", stdout.String(), stderr.String())
	}
}
