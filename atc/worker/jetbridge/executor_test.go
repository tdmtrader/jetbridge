package jetbridge

import (
	"testing"

	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func TestNewSPDYExecutorCreation(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	config := &rest.Config{Host: "https://localhost:6443"}

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
}

func TestNewSPDYExecutorWithDifferentConfigs(t *testing.T) {
	tests := []struct {
		name string
		host string
	}{
		{"in-cluster", "https://kubernetes.default.svc"},
		{"external", "https://my-cluster.example.com:6443"},
		{"localhost", "https://127.0.0.1:6443"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clientset := fake.NewSimpleClientset()
			config := &rest.Config{Host: tc.host}

			executor := NewSPDYExecutor(clientset, config)
			if executor == nil {
				t.Fatal("NewSPDYExecutor returned nil")
			}
			if executor.restConfig.Host != tc.host {
				t.Errorf("expected host %s, got %s", tc.host, executor.restConfig.Host)
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
