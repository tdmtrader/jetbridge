package jetbridge

// A sidecar's logs have to go somewhere a person can read them. When the
// engine hands the step a per-sidecar event writer, they go there, unlabelled,
// because fly watch already knows whose lines those are. When it does not --
// every caller that predates per-sidecar writers, and every step whose
// delegate does not supply one -- they fall back to the step's own stdout,
// where they share a stream with the command's output and need a "[name] "
// prefix to be attributable at all.
//
// behavioral_runtime_spec_restored_test.go's SC-07 pins that routing for the
// direct Process path, and on the weakest possible observation: that some
// GetLogs request happened. The exec path (execProcess.Wait, which is what
// every task/get/put/check step actually runs) had no local test at all. This
// one asserts the bytes and the destination, not just the request.

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

func TestExecProcessRoutesSidecarLogsToStdoutWhenThereIsNoDedicatedWriter(t *testing.T) {
	const namespace = "sidecar-ns"
	const podName = "sidecar-pod"
	const sidecarName = "redis"
	const mainLine = "main command output\n"

	runWait := func(t *testing.T, processIO runtime.ProcessIO) (*fake.Clientset, runtime.ProcessResult) {
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
		if err != nil {
			t.Fatalf("Wait() = %v, want no error", err)
		}
		return clientset, result
	}

	t.Run("with no dedicated writer the sidecar falls back to stdout under its prefix", func(t *testing.T) {
		stdout := &lockedBuffer{}
		clientset, result := runWait(t, runtime.ProcessIO{Stdout: stdout, Stderr: &bytes.Buffer{}})

		if result.ExitStatus != 0 {
			t.Fatalf("ExitStatus = %d, want 0", result.ExitStatus)
		}
		got := stdout.String()
		want := "[" + sidecarName + "] " + fakeSidecarLogBody
		if !strings.Contains(got, want) {
			t.Errorf("stdout = %q, want it to carry the sidecar's log under %q", got, want)
		}
		if !strings.Contains(got, mainLine) {
			t.Errorf("stdout = %q, want it to still carry the command's own output %q", got, mainLine)
		}
		// The command's own output must not be labelled as the sidecar's.
		if strings.Contains(got, "["+sidecarName+"] "+mainLine) {
			t.Errorf("stdout = %q: the command's output was prefixed as the sidecar's", got)
		}
		assertSidecarLogsRequested(t, clientset, sidecarName)
	})

	t.Run("with a dedicated writer the sidecar goes there, unprefixed, and not to stdout", func(t *testing.T) {
		stdout := &lockedBuffer{}
		dedicated := &lockedBuffer{}
		clientset, _ := runWait(t, runtime.ProcessIO{
			Stdout:         stdout,
			Stderr:         &bytes.Buffer{},
			SidecarWriters: map[string]io.Writer{sidecarName: dedicated},
		})

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

	// The fallback is a fallback, not an unconditional read: a step with no
	// stdout and no dedicated writer has nowhere to put the lines and must not
	// go asking for them.
	t.Run("with neither writer the sidecar's logs are not streamed at all", func(t *testing.T) {
		clientset, _ := runWait(t, runtime.ProcessIO{Stderr: &bytes.Buffer{}})
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
