package jetbridge

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func TestExecProcessSafeBoundarySignalsExactChildAndReleases(t *testing.T) {
	executor := &safeBoundaryTestExecutor{}
	process := safeBoundaryTestProcess(executor)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	lease, err := process.AcquireSafeBoundary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := executor.purposeCalls("safe-boundary-lease"); got != 1 {
		t.Fatalf("lease calls = %d, want 1", got)
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := executor.exits(); got != 1 {
		t.Fatalf("helper exits = %d, want 1", got)
	}
	if !executor.sawPrivateStdin || executor.sawTTY || executor.sawNonMain {
		t.Fatalf("boundary exec violated isolated secondary exec contract: private-stdin=%t tty=%t nonmain=%t", executor.sawPrivateStdin, executor.sawTTY, executor.sawNonMain)
	}
	if !strings.Contains(executor.command, "kill -CONT \"$pid\"") || !strings.Contains(executor.command, "kill -USR2 \"$pid\"") {
		t.Fatalf("lease shell does not resume and cancel exact child: %s", executor.command)
	}
}

func TestExecProcessSafeBoundaryRejectsUnsafeOrUnconfiguredBeforeExec(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*execProcess)
	}{
		{"wrong pod", func(p *execProcess) { p.podName = "wrong" }},
		{"non-agent", func(p *execProcess) { p.container.metadata.Type = db.ContainerTypeTask }},
		{"unconfigured", func(p *execProcess) { p.id = "" }},
		{"stdin", func(p *execProcess) { p.processIO.Stdin = &safeBoundaryReader{} }},
		{"checkpoint disabled", func(p *execProcess) { p.container.containerSpec.CheckpointCapture = false }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := &safeBoundaryTestExecutor{}
			process := safeBoundaryTestProcess(executor)
			tt.mutate(process)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := process.AcquireSafeBoundary(ctx); err == nil {
				t.Fatal("accepted an unsafe boundary")
			}
			if got := executor.totalCalls(); got != 0 {
				t.Fatalf("unsafe boundary started %d execs", got)
			}
		})
	}
}

func TestExecProcessSafeBoundaryRejectsStoppedOrMutatedChildAndCancels(t *testing.T) {
	t.Run("initially stopped", func(t *testing.T) {
		executor := &safeBoundaryTestExecutor{leaseErr: errors.New("child stopped")}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, err := safeBoundaryTestProcess(executor).AcquireSafeBoundary(ctx); err == nil {
			t.Fatal("accepted initially stopped child")
		}
		if got := executor.exits(); got != 1 {
			t.Fatalf("stopped child helper exits = %d, want 1", got)
		}
	})
	t.Run("mutated child protocol", func(t *testing.T) {
		executor := &safeBoundaryTestExecutor{readyOutput: "READY 42 0101\n"}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, err := safeBoundaryTestProcess(executor).AcquireSafeBoundary(ctx); err == nil {
			t.Fatal("accepted mutated child identity")
		}
		if got := executor.exits(); got != 1 {
			t.Fatalf("changed child helper exits = %d, want 1", got)
		}
		if got := executor.purposeCalls("safe-boundary-cancel"); got != 1 {
			t.Fatalf("USR2 cancellation calls = %d, want 1", got)
		}
	})
}

func TestExecProcessSafeBoundaryTimeoutCancelsAndDeadlineAutoReleases(t *testing.T) {
	t.Run("timeout sends USR2", func(t *testing.T) {
		executor := &safeBoundaryTestExecutor{blockReady: true}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if _, err := safeBoundaryTestProcess(executor).AcquireSafeBoundary(ctx); err == nil {
			t.Fatal("accepted timed out stop")
		}
		if got := executor.exits(); got != 1 {
			t.Fatalf("timed out helper exits = %d, want 1", got)
		}
		if got := executor.purposeCalls("safe-boundary-cancel"); got != 1 {
			t.Fatalf("timeout USR2 cancellation calls = %d, want 1", got)
		}
	})
	t.Run("caller deadline delivers bounded CONT", func(t *testing.T) {
		executor := &safeBoundaryTestExecutor{}
		process := safeBoundaryTestProcess(executor)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if _, err := process.AcquireSafeBoundary(ctx); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(time.Second)
		for executor.exits() != 1 {
			if time.Now().After(deadline) {
				t.Fatal("deadline did not release safe boundary")
			}
			time.Sleep(time.Millisecond)
		}
	})
}

func TestExecProcessSafeBoundaryRejectsDuplicateLease(t *testing.T) {
	executor := &safeBoundaryTestExecutor{}
	process := safeBoundaryTestProcess(executor)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	lease, err := process.AcquireSafeBoundary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := process.AcquireSafeBoundary(ctx); err == nil {
		t.Fatal("accepted duplicate lease")
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestParseSafeBoundaryChildRequiresStrictPIDStarttime(t *testing.T) {
	for _, value := range []string{"READY 42 100\n", "READY 1 100\n", "READY 42 0\n", "READY 042 100\n", "READY 42 0100\n", "READY 42 100\nextra\n", "READY 42 100"} {
		_, err := parseSafeBoundaryChild(value, "READY")
		valid := value == "READY 42 100\n"
		if (err == nil) != valid {
			t.Fatalf("parse %q error = %v, valid = %t", value, err, valid)
		}
	}
}

func TestSafeBoundaryShellPinsPIDStarttimeAndNeverSignalsAGroup(t *testing.T) {
	if !strings.Contains(safeBoundaryLeaseShell, `[ "$1" = "$pid" ] && [ "$2" = "$start" ] || return 1`) {
		t.Fatal("safe boundary helper does not reject a mutated child record")
	}
	if strings.Contains(safeBoundaryLeaseShell, `"-$pid"`) || strings.Contains(safeBoundaryCancelShell, `"-$pid"`) {
		t.Fatal("safe boundary helper must never signal a process group")
	}
}

func safeBoundaryTestProcess(executor PodExecutor) *execProcess {
	p := checkpointTestProcess(fake.NewClientset(checkpointTestPod("agent-42", "uid-42", "main")), executor)
	p.id = "agent"
	p.processSpec = runtime.ProcessSpec{Path: "agent-runner"}
	return p
}

type safeBoundaryReader struct{}

func (*safeBoundaryReader) Read([]byte) (int, error) { return 0, io.EOF }

type safeBoundaryTestExecutor struct {
	mu              sync.Mutex
	calls           map[string]int
	leaseErr        error
	readyOutput     string
	blockReady      bool
	sawPrivateStdin bool
	sawTTY          bool
	sawNonMain      bool
	command         string
	exitCount       int
}

func (executor *safeBoundaryTestExecutor) ExecInPod(ctx context.Context, _ string, _ string, container string, command []string, stdin io.Reader, stdout, _ io.Writer, tty bool, attrs ExecAttrs) error {
	executor.mu.Lock()
	if executor.calls == nil {
		executor.calls = map[string]int{}
	}
	executor.calls[attrs.Purpose]++
	executor.sawPrivateStdin = executor.sawPrivateStdin || stdin != nil
	executor.sawTTY = executor.sawTTY || tty
	executor.sawNonMain = executor.sawNonMain || container != mainContainerName
	if len(command) > 2 {
		executor.command = command[2]
	}
	leaseErr, readyOutput, blockReady := executor.leaseErr, executor.readyOutput, executor.blockReady
	executor.mu.Unlock()
	if attrs.Purpose == "safe-boundary-cancel" {
		return nil
	}
	if attrs.Purpose != "safe-boundary-lease" {
		return errors.New("unexpected boundary purpose")
	}
	if leaseErr != nil {
		executor.mu.Lock()
		executor.exitCount++
		executor.mu.Unlock()
		return leaseErr
	}
	if !blockReady {
		if readyOutput == "" {
			readyOutput = "READY 42 100\n"
		}
		_, _ = io.WriteString(stdout, readyOutput)
	}
	_, _ = io.Copy(io.Discard, stdin)
	executor.mu.Lock()
	executor.exitCount++
	executor.mu.Unlock()
	return nil
}

func (executor *safeBoundaryTestExecutor) purposeCalls(purpose string) int {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return executor.calls[purpose]
}

func (executor *safeBoundaryTestExecutor) totalCalls() int {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	var total int
	for _, count := range executor.calls {
		total += count
	}
	return total
}

func (executor *safeBoundaryTestExecutor) exits() int {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return executor.exitCount
}
