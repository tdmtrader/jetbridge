package main

import (
	"bytes"
	"context"
	"testing"
)

// This catches a regression where the process accepts authority through stdin
// or an optional flag: every invocation must name exactly one mounted file.
func TestRunCLIRequiresOneAbsoluteAuthorityFile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runCLI(context.Background(), []string{"write"}, &bytes.Buffer{}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("write without authority exit=%d, want usage", code)
	}
	stderr.Reset()
	if code := runCLI(context.Background(), []string{"write", "--authority", "relative.json"}, &bytes.Buffer{}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("write with relative authority exit=%d, want usage", code)
	}
}
