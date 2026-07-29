package main

import (
	"bytes"
	"context"
	"testing"
)

// This catches a regression where an agent chooses its own authority file.
// Production always reads one fixed, platform-mounted authority location.
func TestRunCLIRejectsAuthorityOverride(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runCLI(context.Background(), []string{"write", "--authority", "/tmp/forged.json"}, &bytes.Buffer{}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("write with authority override exit=%d, want usage", code)
	}
}
