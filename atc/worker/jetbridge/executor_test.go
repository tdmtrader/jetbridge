package jetbridge

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	spdystream "k8s.io/apimachinery/pkg/util/httpstream/spdy"
	apiremotecommand "k8s.io/apimachinery/pkg/util/remotecommand"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

func TestNewSPDYExecutor(t *testing.T) {
	// This is a construction contract, not an API-response test. Use the actual
	// client implementation; creating it makes no network request.
	for _, tc := range []struct{ name, host string }{
		{"default", "https://localhost:6443"},
		{"in-cluster", "https://kubernetes.default.svc"},
		{"external", "https://my-cluster.example.com:6443"},
		{"localhost", "https://127.0.0.1:6443"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := &rest.Config{Host: tc.host}
			clientset, err := kubernetes.NewForConfig(config)
			if err != nil {
				t.Fatal(err)
			}
			executor := NewSPDYExecutor(clientset, config)
			if executor == nil {
				t.Fatal("NewSPDYExecutor returned nil")
			}
			if executor.clientset != clientset {
				t.Error("clientset not stored correctly")
			}
			if executor.restConfig != config {
				t.Error("restConfig not stored correctly")
			}
			if executor.restConfig == nil || executor.restConfig.Host != tc.host {
				t.Errorf("expected host %s, got config %+v", tc.host, executor.restConfig)
			}
		})
	}
}

// ExecExitError is defined in volume.go but is the primary error type returned
// by executor.ExecInPod. Test it here alongside the executor.
func TestExecExitErrorMessage(t *testing.T) {
	tests := []struct {
		exitCode int
		expected string
	}{
		{0, "process exited with code 0"},
		{1, "process exited with code 1"},
		{2, "process exited with code 2"},
		{127, "process exited with code 127"},
		{137, "process exited with code 137"},
	}

	for _, tc := range tests {
		err := &ExecExitError{ExitCode: tc.exitCode}
		if err.Error() != tc.expected {
			t.Errorf("ExecExitError{%d}.Error() = %q, want %q", tc.exitCode, err.Error(), tc.expected)
		}
	}
}

// ---------------------------------------------------------------------------
// The status-checking transport (exec_status.go), through the real one
// ---------------------------------------------------------------------------
//
// client-go reads the command's exit status off the SPDY error stream. A
// stream that closes with nothing on it is, to client-go, the same thing as a
// command that exited 0 -- which is exactly what a pod deleted under its own
// running command produces. executor.go therefore hands
// NewSPDYExecutorForTransports a statusCheckingUpgrader instead of letting
// remotecommand.NewSPDYExecutor build the plain one.
//
// exec_status_test.go covers the policy on hand-made streams. What it cannot
// see is whether ExecInPod is wired to it at all: restoring the plain
// remotecommand.NewSPDYExecutor leaves every assertion there passing. So this
// drives a real SPDY server over a real httptest listener, through the real
// client-go executor, and reads the answer ExecInPod gives.

// execStatusStubServer is the kubelet's half of an exec upgrade: it
// negotiates v4, accepts the client's streams, and closes them. When
// sendStatus is false the error stream closes with nothing on it, which is a
// kubelet whose container went away mid-command.
func execStatusStubServer(t *testing.T, sendStatus bool) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := httpstream.Handshake(r, w, []string{apiremotecommand.StreamProtocolV4Name}); err != nil {
			return // Handshake has already written the error response.
		}
		streams := make(chan httpstream.Stream, 8)
		conn := spdystream.NewResponseUpgrader().UpgradeResponse(w, r,
			func(stream httpstream.Stream, _ <-chan struct{}) error {
				streams <- stream
				return nil
			})
		if conn == nil {
			return
		}
		defer conn.Close()

		for {
			select {
			case stream := <-streams:
				if stream.Headers().Get(corev1.StreamType) == corev1.StreamTypeError && sendStatus {
					_, _ = stream.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Success"}`))
				}
				_ = stream.Close()
			case <-conn.CloseChan():
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func execAgainstStub(t *testing.T, server *httptest.Server) error {
	t.Helper()
	config := &rest.Config{Host: server.URL}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatalf("build a clientset against the stub kubelet: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return NewSPDYExecutor(clientset, config).ExecInPod(ctx, "ns", "pod", mainContainerName,
		[]string{"true"}, nil, io.Discard, nil, false, ExecAttrs{Purpose: "step-command"})
}

func TestSPDYExecutorRefusesAnExecThatDeliveredNoExitStatus(t *testing.T) {
	// The claim: an error stream that closed empty is not an exit 0.
	t.Run("an empty error stream is reported as a missing exit status", func(t *testing.T) {
		err := execAgainstStub(t, execStatusStubServer(t, false))
		if err == nil {
			t.Fatal("ExecInPod = nil: an exec that delivered no exit status was reported as success")
		}
		if !isExecStatusMissing(err) {
			t.Fatalf("ExecInPod = %v, want the missing-exit-status error", err)
		}
	})

	// The control, on the same server: a v4 Success status is an ordinary
	// exit 0, and the wrapper must not turn every exec into a failure.
	t.Run("a Success status on the error stream is still an exit 0", func(t *testing.T) {
		if err := execAgainstStub(t, execStatusStubServer(t, true)); err != nil {
			t.Fatalf("ExecInPod = %v, want no error", err)
		}
	})

	// What makes the first case a wiring test rather than a client-go test:
	// the plain executor, against the same server, reports the same empty
	// error stream as success. Restoring remotecommand.NewSPDYExecutor in
	// executor.go puts ExecInPod on this path.
	t.Run("the plain client-go executor reports the same stream as success", func(t *testing.T) {
		server := execStatusStubServer(t, false)
		config := &rest.Config{Host: server.URL}
		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			t.Fatal(err)
		}
		req := clientset.CoreV1().RESTClient().Post().
			Resource("pods").Name("pod").Namespace("ns").SubResource("exec").
			VersionedParams(&corev1.PodExecOptions{
				Container: mainContainerName,
				Command:   []string{"true"},
				Stdout:    true,
			}, scheme.ParameterCodec)

		exec, err := remotecommand.NewSPDYExecutor(config, http.MethodPost, req.URL())
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := exec.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: io.Discard}); err != nil {
			t.Fatalf("the plain executor returned %v; this test's premise is that it returns nil here, "+
				"so the assertion above would no longer be about executor.go's wiring", err)
		}
	})
}
