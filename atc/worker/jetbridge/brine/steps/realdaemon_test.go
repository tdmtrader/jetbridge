package steps

import (
	"strings"
	"testing"
)

// The guard this replaces could not fire. It tested cmd.ProcessState, which
// exec.Cmd populates only inside Wait/Run — and nothing called either — so it
// was nil on every iteration of the readiness loop. A daemon that died at boot
// was reported twenty seconds later as "did not answer", with its exit code and
// its reason thrown away.
func TestStartRealDaemonReportsADaemonThatDiesAtBoot(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the artifact-daemon binary")
	}
	// An unknown durable store is refused at startup, so the process exits
	// before it ever listens.
	d, err := startRealDaemon("--durable-store", "not-a-real-store")
	if err == nil {
		_ = d.stop()
		t.Fatal("a daemon that cannot start MUST be reported as such, not waited on")
	}
	if strings.Contains(err.Error(), "did not answer within") {
		t.Fatalf("the death was reported as a timeout, which is the bug this guards: %v", err)
	}
	if !strings.Contains(err.Error(), "exited during startup") {
		t.Fatalf("expected the failure to say the daemon exited, got: %v", err)
	}
}
