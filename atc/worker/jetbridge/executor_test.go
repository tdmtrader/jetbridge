package jetbridge

import (
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
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
