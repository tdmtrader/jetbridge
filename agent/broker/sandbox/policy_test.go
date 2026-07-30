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

func TestPolicyRejectsAncestorSymlinkResolvingIntoSiblingScratch(t *testing.T) {
	scratchRoot := t.TempDir()
	runScratch := filepath.Join(scratchRoot, "broker-run-1")
	siblingScratch := filepath.Join(scratchRoot, "broker-run-2")
	if err := os.Mkdir(runScratch, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(siblingScratch, "runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	lexicalRoot := t.TempDir()
	if err := os.Symlink(siblingScratch, filepath.Join(lexicalRoot, "safe-runtime")); err != nil {
		t.Fatal(err)
	}
	lexicallySafe := filepath.Join(lexicalRoot, "safe-runtime", "runtime")

	if _, err := (Policy{
		WritableRoot:  runScratch,
		ReadOnlyPaths: []string{lexicallySafe},
	}).Canonical(); err == nil || !strings.Contains(err.Error(), "scratch") {
		t.Fatalf("ancestor symlink into sibling scratch error = %v", err)
	}
}

func TestPolicyCanonicalizesWritableRootAndReadPaths(t *testing.T) {
	physical := t.TempDir()
	runScratch := filepath.Join(physical, "scratch", "broker-run-1")
	readPath := filepath.Join(physical, "runtime")
	if err := os.MkdirAll(runScratch, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(readPath, 0o700); err != nil {
		t.Fatal(err)
	}
	lexical := t.TempDir()
	if err := os.Symlink(filepath.Join(physical, "scratch"), filepath.Join(lexical, "scratch-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(readPath, filepath.Join(lexical, "runtime-link")); err != nil {
		t.Fatal(err)
	}
	canonical, err := (Policy{
		WritableRoot:  filepath.Join(lexical, "scratch-link", "broker-run-1"),
		ReadOnlyPaths: []string{filepath.Join(lexical, "runtime-link")},
	}).Canonical()
	if err != nil {
		t.Fatal(err)
	}
	canonicalRunScratch, _ := filepath.EvalSymlinks(runScratch)
	canonicalReadPath, _ := filepath.EvalSymlinks(readPath)
	if canonical.WritableRoot != canonicalRunScratch || len(canonical.ReadOnlyPaths) != 1 ||
		canonical.ReadOnlyPaths[0] != canonicalReadPath {
		t.Fatalf("canonical policy = %#v", canonical)
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

func TestDeniedProcessSyscallSetCoversCrossProcessRoutes(t *testing.T) {
	denied := map[string]struct{}{}
	for _, name := range DeniedProcessSyscalls() {
		denied[name] = struct{}{}
	}
	for _, required := range []string{
		"kill", "tkill", "tgkill", "rt_sigqueueinfo", "rt_tgsigqueueinfo",
		"pidfd_send_signal", "ptrace", "process_vm_readv", "process_vm_writev",
		"pidfd_getfd", "kcmp", "prlimit64", "process_madvise",
		"process_mrelease", "get_robust_list", "setpriority", "ioprio_set",
		"sched_setaffinity", "sched_setscheduler", "sched_setparam",
		"sched_setattr", "migrate_pages", "move_pages", "perf_event_open",
		"pidfd_open",
	} {
		if _, found := denied[required]; !found {
			t.Errorf("cross-process syscall %q is not denied", required)
		}
	}
}

func TestValidateDumpabilityFailsClosed(t *testing.T) {
	if err := ValidateDumpability(0, nil); err != nil {
		t.Fatalf("nondumpable process rejected: %v", err)
	}
	if err := ValidateDumpability(1, nil); err == nil {
		t.Fatal("dumpable broker process accepted")
	}
	if err := ValidateDumpability(0, errors.New("query failed")); err == nil {
		t.Fatal("dumpability query failure accepted")
	}
}
