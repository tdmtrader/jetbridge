package jetbridge

// A sidecar's logs go to the per-sidecar event writer the engine hands the
// step, unlabelled, because fly watch already knows whose lines those are.
// There is no other place for them: a step that wants output (has a Stdout)
// and declares a sidecar it has no writer for is refused by Wait, naming the
// sidecar, rather than having the lines folded into the command's stdout
// under a "[name] " prefix as the removed fallback did.
//
// behavioral_runtime_spec_restored_test.go's SC-07 pins the same routing for
// the direct Process path. The exec path (execProcess.Wait, which is what
// every task/get/put/check step actually runs) is pinned here on the bytes and
// the destination, not just the request.

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
)

// The fake clientset's GetLogs serves this for every container.
const fakeSidecarLogBody = "fake logs"

// mainOutputExecutor is the step's own command: it writes one line to the
// stdout it was handed and exits 0. That line shares the writer with any
// sidecar falling back to it, which is the situation the serialized writer
// exists for.
type mainOutputExecutor struct {
	line string
}

func (e *mainOutputExecutor) ExecInPod(_ context.Context, _, _, _ string, _ []string,
	_ io.Reader, stdout, _ io.Writer, _ bool, _ ExecAttrs) error {
	if stdout != nil {
		_, _ = io.WriteString(stdout, e.line)
	}
	return nil
}

func sidecarPausePod(name, namespace string, sidecars ...string) *corev1.Pod {
	containers := []corev1.Container{{Name: mainContainerName}}
	statuses := []corev1.ContainerStatus{{
		Name: mainContainerName, Ready: true,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}
	for _, sc := range sidecars {
		containers = append(containers, corev1.Container{Name: sc})
		statuses = append(statuses, corev1.ContainerStatus{
			Name: sc, Ready: true,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		})
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       corev1.PodSpec{NodeName: "node-1", Containers: containers},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			ContainerStatuses: statuses,
		},
	}
}

// lockedBuffer is the test's stand-in for a build-event writer: it records
// what it was given, and tolerates a caller that writes to it concurrently
// without going through the serializing writer.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestExecProcessStreamsSidecarLogsToTheirDedicatedWriters(t *testing.T) {
	const namespace = "sidecar-ns"
	const podName = "sidecar-pod"
	const sidecarName = "redis"
	const mainLine = "main command output\n"

	runWait := func(t *testing.T, processIO runtime.ProcessIO) (*fake.Clientset, runtime.ProcessResult, error) {
		t.Helper()
		clientset := fake.NewSimpleClientset(sidecarPausePod(podName, namespace, sidecarName))
		config := NewConfig(namespace, "")
		container := &Container{
			handle:   "sidecar-handle",
			podName:  podName,
			metadata: db.ContainerMetadata{Type: db.ContainerTypeTask},
			containerSpec: runtime.ContainerSpec{
				Type:     db.ContainerTypeTask,
				Sidecars: []atc.SidecarConfig{{Name: sidecarName, Image: "redis:7"}},
			},
			clientset:  clientset,
			config:     config,
			properties: map[string]string{},
		}
		process := newExecProcess("proc-1", podName, clientset, config, container,
			&mainOutputExecutor{line: mainLine},
			runtime.ProcessSpec{Path: "sh", Args: []string{"-c", "echo hi"}},
			processIO, nil)

		result, err := process.Wait(context.Background())
		return clientset, result, err
	}

	t.Run("with a dedicated writer the sidecar goes there, unprefixed, and not to stdout", func(t *testing.T) {
		stdout := &lockedBuffer{}
		dedicated := &lockedBuffer{}
		clientset, result, err := runWait(t, runtime.ProcessIO{
			Stdout:         stdout,
			Stderr:         &bytes.Buffer{},
			SidecarWriters: map[string]io.Writer{sidecarName: dedicated},
		})
		if err != nil {
			t.Fatalf("Wait() = %v, want no error", err)
		}
		if result.ExitStatus != 0 {
			t.Fatalf("ExitStatus = %d, want 0", result.ExitStatus)
		}
		if got := dedicated.String(); got != fakeSidecarLogBody {
			t.Errorf("dedicated sidecar writer = %q, want %q with no prefix", got, fakeSidecarLogBody)
		}
		if got := stdout.String(); strings.Contains(got, fakeSidecarLogBody) || strings.Contains(got, "["+sidecarName+"]") {
			t.Errorf("stdout = %q, want the sidecar's log only on its dedicated writer", got)
		}
		if got := stdout.String(); !strings.Contains(got, mainLine) {
			t.Errorf("stdout = %q, want the command's own output %q", got, mainLine)
		}
		assertSidecarLogsRequested(t, clientset, sidecarName)
	})

	// The removed fallback would have folded the sidecar's lines into stdout
	// here. A step that wants output and declared a sidecar it has no writer
	// for is a caller bug, named before anything runs.
	t.Run("with a stdout but no writer for the sidecar Wait refuses, naming it", func(t *testing.T) {
		stdout := &lockedBuffer{}
		clientset, _, err := runWait(t, runtime.ProcessIO{Stdout: stdout, Stderr: &bytes.Buffer{}})
		if err == nil || !strings.Contains(err.Error(), `sidecar "`+sidecarName+`" has no log writer`) {
			t.Fatalf("Wait() = %v, want an error naming the sidecar without a writer", err)
		}
		if got := stdout.String(); got != "" {
			t.Errorf("stdout = %q, want nothing: the step did not run", got)
		}
		for _, action := range clientset.Actions() {
			if action.GetVerb() == "get" && action.GetSubresource() == "log" {
				t.Fatalf("logs were requested with nowhere to write them: %#v", action)
			}
		}
	})

	// A step with no stdout has asked for no output at all, and nothing is
	// streamed for its sidecars either; it needs no writers.
	t.Run("with no stdout the sidecar's logs are not streamed at all", func(t *testing.T) {
		clientset, _, err := runWait(t, runtime.ProcessIO{Stderr: &bytes.Buffer{}})
		if err != nil {
			t.Fatalf("Wait() = %v, want no error", err)
		}
		for _, action := range clientset.Actions() {
			if action.GetVerb() == "get" && action.GetSubresource() == "log" {
				t.Fatalf("logs were requested with nowhere to write them: %#v", action)
			}
		}
	})
}

func assertSidecarLogsRequested(t *testing.T, clientset *fake.Clientset, sidecar string) {
	t.Helper()
	for _, action := range clientset.Actions() {
		if action.GetVerb() == "get" && action.GetSubresource() == "log" {
			return
		}
	}
	t.Fatalf("no GetLogs request was made for sidecar %q", sidecar)
}
