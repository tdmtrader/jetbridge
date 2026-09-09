package steps

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/worker/jetbridge"
)

func TestLocalExecutorStreamsAndExitStatus(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := (localExecutor{}).ExecInPod(context.Background(), "ns", "pod", "main",
		[]string{"sh", "-c", "cat; printf diagnostic >&2; exit 7"},
		strings.NewReader("artifact bytes"), &stdout, &stderr, false, jetbridge.ExecAttrs{})
	var status *jetbridge.ExecExitError
	if !errors.As(err, &status) || status.ExitCode != 7 {
		t.Fatalf("expected exit 7, got %v", err)
	}
	if stdout.String() != "artifact bytes" || stderr.String() != "diagnostic" {
		t.Fatalf("streams were lost or mixed: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestLocalExecutorTerminalMatchesRequest(t *testing.T) {
	for _, tty := range []bool{false, true} {
		var stdout bytes.Buffer
		err := (localExecutor{}).ExecInPod(context.Background(), "ns", "pod", "main",
			[]string{"sh", "-c", "if test -t 1; then printf terminal; else printf pipe; fi"},
			nil, &stdout, nil, tty, jetbridge.ExecAttrs{})
		want := "pipe"
		if tty {
			want = "terminal"
		}
		if err != nil || stdout.String() != want {
			t.Fatalf("tty=%v: output=%q error=%v, want %q", tty, stdout.String(), err, want)
		}
	}
}

func TestLocalExecutorCancellationStopsDescendants(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	done := make(chan error, 1)
	go func() {
		err := (localExecutor{}).ExecInPod(ctx, "ns", "pod", "main",
			[]string{"sh", "-c", "sleep 60 & echo $!; wait"}, nil, writer, io.Discard, false, jetbridge.ExecAttrs{})
		_ = writer.CloseWithError(err)
		done <- err
	}()
	pid, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil {
		t.Fatalf("wait for child: %v", err)
	}
	pid = strings.TrimSpace(pid)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation left command or stream copying blocked")
	}
	if err := processStopped(pid); err != nil {
		t.Fatal(err)
	}
}

func TestLocalExecutorTerminalInputReachesCommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var stdout bytes.Buffer
	err := (localExecutor{}).ExecInPod(ctx, "ns", "pod", "main",
		[]string{"sh", "-c", "read value; printf 'received:%s' \"$value\""},
		strings.NewReader("terminal input\n"), &stdout, nil, true, jetbridge.ExecAttrs{})
	if err != nil || !strings.Contains(stdout.String(), "received:terminal input") {
		t.Fatalf("terminal input lost: output=%q error=%v", stdout.String(), err)
	}
}

func TestSupervisorStateIsOwnedByItsWorkspace(t *testing.T) {
	resource := TaskWorkspaceResourceDefinition()
	newWorkspace := func() TaskWorkspace {
		value, err := resource.Factory(nil)
		if err != nil {
			t.Fatal(err)
		}
		w := value.(TaskWorkspace)
		// Independent test teardown still reclaims our allocations if the
		// disposer under test is broken. Assertions run before this cleanup.
		t.Cleanup(func() { _ = os.RemoveAll(w.ownedDir) })
		return w
	}
	first, second := newWorkspace(), newWorkspace()
	original, err := os.MkdirTemp("/tmp", "brine-supervisor-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(original) })
	sentinel := filepath.Join(original, "sentinel")
	if err := os.WriteFile(sentinel, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	script := "S='" + original + "'\n" +
		"mkdir -p \"$S\"\n" +
		"n=0; [ ! -f \"$S/count\" ] || n=$(cat \"$S/count\")\n" +
		"printf '%s' \"$((n+1))\" >\"$S/count\"\ncat \"$S/count\""
	command := []string{"sh", "-c", script}
	run := func(w TaskWorkspace, want string) {
		t.Helper()
		var stdout bytes.Buffer
		executor := localExecutor{supervisorRoot: filepath.Join(w.Dir, "quote's workspace")}
		err := executor.ExecInPod(context.Background(), "ns", "pod", "main",
			command, nil, &stdout, nil, false, jetbridge.ExecAttrs{})
		if err != nil || stdout.String() != want {
			t.Fatalf("state output=%q, want %q; err=%v", stdout.String(), want, err)
		}
	}
	run(first, "1")
	run(first, "2")
	run(second, "1")
	if command[2] != script {
		t.Fatal("executor changed the caller's command")
	}
	if _, err := os.Stat(filepath.Join(original, "count")); !os.IsNotExist(err) {
		t.Fatalf("supervisor wrote outside its workspace: %v", err)
	}
	if err := resource.Disposer(TaskWorkspace{Dir: original}); err == nil {
		t.Fatal("disposer accepted an unowned workspace")
	}
	owned := first.Dir
	first.Dir = original
	if err := resource.Disposer(first); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatalf("owned workspace survived disposal: %v", err)
	}
	other := filepath.Join(second.Dir, "quote's workspace", supervisorStateDirectory, filepath.Base(original), "count")
	if data, err := os.ReadFile(other); err != nil || string(data) != "1" {
		t.Fatalf("disposing one workspace damaged another: %q, %v", data, err)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "outside" {
		t.Fatalf("disposal followed the public path outside its allocation: %q, %v", data, err)
	}
	if err := resource.Disposer(second); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorStateMappingRejectsUnsafeHeaders(t *testing.T) {
	executor := localExecutor{supervisorRoot: t.TempDir()}
	for _, script := range []string{
		"S=/tmp/state\nprintf unsafe",
		"S='/etc/state'\nprintf unsafe",
		"S='/tmp/nested/state'\nprintf unsafe",
		"S='/tmp/../state'\nprintf unsafe",
		"S='/tmp/state'",
	} {
		err := executor.ExecInPod(context.Background(), "ns", "pod", "main",
			[]string{"sh", "-c", script}, nil, nil, nil, false, jetbridge.ExecAttrs{})
		if err == nil {
			t.Fatalf("accepted unsafe supervisor header: %q", script)
		}
	}
	var stdout bytes.Buffer
	err := executor.ExecInPod(context.Background(), "ns", "pod", "main",
		[]string{"sh", "-c", "printf plain"}, nil, &stdout, nil, false, jetbridge.ExecAttrs{})
	if err != nil || stdout.String() != "plain" {
		t.Fatalf("ordinary command changed: %q, %v", stdout.String(), err)
	}
}
