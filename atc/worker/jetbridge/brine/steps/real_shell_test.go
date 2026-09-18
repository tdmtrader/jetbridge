package steps

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Infrastructure failures must not become the nonzero script exit that a
// negative behavioral scenario expects. Use real applets for both outcomes.
func TestBusyboxScriptOutcomeBoundary(t *testing.T) {
	binary := os.Getenv("BRINE_BUSYBOX_BINARY")
	if binary == "" {
		var err error
		binary, err = filepath.Abs("../.build/busybox")
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("build the real BusyBox prerequisite with scripts/build-private-network: %v", err)
	}
	for _, tc := range []struct {
		name    string
		script  string
		binary  string
		timeout time.Duration
		exit    int
	}{
		{"success", "printf started", binary, 5 * time.Second, 0},
		{"script refusal", "printf started; exit 7", binary, 5 * time.Second, 7},
		{"deadline", "printf started; sleep 30", binary, time.Second, -1},
		{"missing prerequisite", "printf started", "", 5 * time.Second, -2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BRINE_BUSYBOX_BINARY", tc.binary)
			ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
			defer cancel()
			out, err := runBusyboxScript(ctx, []string{"sh", "-c", tc.script}, nil)
			var exitErr *exec.ExitError
			switch tc.exit {
			case 0:
				if err != nil || string(out) != "started" {
					t.Fatalf("real script did not succeed: output=%q, err=%v", out, err)
				}
			case -1:
				if !errors.Is(err, context.DeadlineExceeded) || errors.As(err, &exitErr) || string(out) != "started" {
					t.Fatalf("timeout was not distinguished from a script refusal: output=%q, err=%v", out, err)
				}
			case -2:
				if err == nil || errors.As(err, &exitErr) || !strings.Contains(err.Error(), "BRINE_BUSYBOX_BINARY") || len(out) != 0 {
					t.Fatalf("missing prerequisite was not refused before execution: output=%q, err=%v", out, err)
				}
			default:
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != tc.exit || string(out) != "started" {
					t.Fatalf("real script refusal was lost: output=%q, err=%v", out, err)
				}
			}
		})
	}
}
