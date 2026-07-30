package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestPolicyRejectsBrokerWorkspaceProcAndSiblingScratchAccess(t *testing.T) {
	scratchRoot := t.TempDir()
	runScratch := filepath.Join(scratchRoot, "broker-run-1")
	if err := os.Mkdir(runScratch, 0o700); err != nil {
		t.Fatal(err)
	}
	safe := t.TempDir()
	if err := (Policy{WritableRoot: runScratch, ReadOnlyPaths: []string{safe}}).Validate(); err != nil {
		t.Fatalf("safe policy: %v", err)
	}
	for _, forbidden := range []string{
		"/workspace",
		"/run/concourse/agent-broker",
		"/proc",
		scratchRoot,
		filepath.Join(scratchRoot, "broker-run-2"),
	} {
		t.Run(strings.ReplaceAll(forbidden, "/", "_"), func(t *testing.T) {
			if err := (Policy{WritableRoot: runScratch, ReadOnlyPaths: []string{forbidden}}).Validate(); err == nil {
				t.Fatalf("policy admitted forbidden read path %q", forbidden)
			}
		})
	}
}

func TestParseExecArgsPreservesNativeHarnessArgv(t *testing.T) {
	policy, binary, arguments, err := ParseExecArgs([]string{
		"--writable-root", "/scratch/broker-run-1",
		"--read-only", "/opt/harness",
		"--read-only", "/opt/schema.json",
		"--", "/opt/harness/codex", "exec", "--model", "provider/model", "-",
	})
	if err != nil {
		t.Fatal(err)
	}
	if policy.WritableRoot != "/scratch/broker-run-1" || len(policy.ReadOnlyPaths) != 2 ||
		binary != "/opt/harness/codex" || strings.Join(arguments, "\x00") != "exec\x00--model\x00provider/model\x00-" {
		t.Fatalf("parsed = (%#v, %q, %#v)", policy, binary, arguments)
	}
}

func TestValidateAvailabilityFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name string
		abi  int
		err  error
	}{
		{"missing syscall", 0, syscall.ENOSYS},
		{"unsupported", 0, syscall.EOPNOTSUPP},
		{"seccomp denied", 0, syscall.EPERM},
		{"old ABI", MinimumABI - 1, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateAvailability(test.abi, test.err); err == nil {
				t.Fatal("unsupported Landlock was accepted")
			}
		})
	}
	if err := ValidateAvailability(MinimumABI, nil); err != nil {
		t.Fatalf("minimum ABI rejected: %v", err)
	}
	if err := ValidateAvailability(0, errors.New("boom")); err == nil {
		t.Fatal("unexpected query error was accepted")
	}
}
