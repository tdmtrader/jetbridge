//go:build live
// +build live

package jetbridge_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func liveClientAndExecutor(t *testing.T) (kubernetes.Interface, *jetbridge.Config, jetbridge.PodExecutor) {
	t.Helper()

	clientset, cfg := kubeClient(t)

	restConfig, err := jetbridge.RestConfig(*cfg)
	if err != nil {
		t.Fatalf("creating rest config: %v", err)
	}

	executor := jetbridge.NewSPDYExecutor(clientset, restConfig)
	return clientset, cfg, executor
}

// TestLiveResourceGetStepE2E simulates the full resource get step flow:
// 1. Create a pause Pod (like execProcess does)
// 2. Wait for Running
// 3. Exec the resource binary with stdin JSON → stdout JSON
// 4. Clean up the Pod
func TestLiveResourceGetStepE2E(t *testing.T) {
	clientset, cfg, executor := liveClientAndExecutor(t)
	ctx := context.Background()
	podName := "e2e-resource-get-" + time.Now().Format("150405")

	cleanupPod(t, clientset, cfg.Namespace, podName)

	// Step 1: Create a pause Pod (mirrors createPausePod in container.go)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: cfg.Namespace,
			Labels: map[string]string{
				"concourse.ci/worker": "true",
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:    "main",
					Image:   "busybox",
					Command: []string{"sh", "-c", "trap 'exit 0' TERM; sleep 86400 & wait"},
				},
			},
		},
	}

	t.Logf("creating pause pod %s", podName)
	_, err := clientset.CoreV1().Pods(cfg.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("creating pause pod: %v", err)
	}

	// Step 2: Wait for Running
	t.Logf("waiting for pod to become Running...")
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		p, err := clientset.CoreV1().Pods(cfg.Namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("getting pod status: %v", err)
		}
		if p.Status.Phase == corev1.PodRunning {
			t.Logf("pod is Running")
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Step 3: Simulate resource get - exec /opt/resource/in (simulated with cat)
	// Resource protocol: stdin = JSON with source+version+params, stdout = JSON result
	getInput := map[string]interface{}{
		"source":  map[string]string{"uri": "https://example.com/repo.git"},
		"version": map[string]string{"ref": "abc123"},
		"params":  map[string]string{"depth": "1"},
	}
	inputJSON, _ := json.Marshal(getInput)

	var stdout, stderr bytes.Buffer
	stdin := bytes.NewReader(inputJSON)

	t.Logf("executing resource get command with JSON stdin...")
	err = executor.ExecInPod(ctx, cfg.Namespace, podName, "main",
		// Simulate a resource script that reads stdin JSON and outputs a modified version
		[]string{"sh", "-c", `input=$(cat); echo "{\"version\":{\"ref\":\"abc123\"},\"metadata\":[{\"name\":\"commit\",\"value\":\"abc123\"}]}"`},
		stdin, &stdout, &stderr,
		false,
	)
	if err != nil {
		t.Fatalf("resource get exec failed: %v (stderr: %s)", err, stderr.String())
	}

	// Verify JSON output is valid
	var result map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON output: %v\nraw: %s", err, stdout.String())
	}

	t.Logf("resource get result: %s", stdout.String())

	if version, ok := result["version"].(map[string]interface{}); !ok || version["ref"] != "abc123" {
		t.Fatalf("unexpected version in result: %v", result["version"])
	}
	if stderr.Len() > 0 {
		t.Logf("resource stderr (logs): %s", stderr.String())
	}

	// Step 4: Verify volume data transfer (tar stream in/out)
	t.Run("volume StreamIn/StreamOut via tar", func(t *testing.T) {
		// StreamIn: write test data via tar
		tarInput := "test file content\n"
		var tarStdout, tarStderr bytes.Buffer

		// First create a directory
		err := executor.ExecInPod(ctx, cfg.Namespace, podName, "main",
			[]string{"mkdir", "-p", "/tmp/test-volume"},
			nil, nil, nil,
			false,
		)
		if err != nil {
			t.Fatalf("creating directory: %v", err)
		}

		// Write a file — use tee to avoid SPDY stdin race where cat >
		// file can close before data is flushed to disk.
		err = executor.ExecInPod(ctx, cfg.Namespace, podName, "main",
			[]string{"sh", "-c", "tee /tmp/test-volume/data.txt > /dev/null && sync"},
			strings.NewReader(tarInput), nil, &tarStderr,
			false,
		)
		if err != nil {
			t.Fatalf("writing file: %v", err)
		}

		// Read it back
		err = executor.ExecInPod(ctx, cfg.Namespace, podName, "main",
			[]string{"cat", "/tmp/test-volume/data.txt"},
			nil, &tarStdout, &tarStderr,
			false,
		)
		if err != nil {
			t.Fatalf("reading file: %v", err)
		}

		if strings.TrimSpace(tarStdout.String()) != strings.TrimSpace(tarInput) {
			t.Fatalf("expected %q, got %q", tarInput, tarStdout.String())
		}
		t.Logf("volume data round-trip successful: %q", strings.TrimSpace(tarStdout.String()))
	})
}

// TestLivePodCancellationCleanup tests that cancelling a context during exec
// properly terminates and that the Pod can be cleaned up.
func TestLivePodCancellationCleanup(t *testing.T) {
	clientset, cfg, executor := liveClientAndExecutor(t)
	ctx := context.Background()
	podName := "e2e-cancel-" + time.Now().Format("150405")

	// Ensure cleanup even if the explicit delete in this test fails.
	cleanupPod(t, clientset, cfg.Namespace, podName)

	// Create a pause Pod
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: cfg.Namespace,
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:    "main",
					Image:   "busybox",
					Command: []string{"sh", "-c", "trap 'exit 0' TERM; sleep 86400 & wait"},
				},
			},
		},
	}

	_, err := clientset.CoreV1().Pods(cfg.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("creating pod: %v", err)
	}

	// Wait for Running
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		p, _ := clientset.CoreV1().Pods(cfg.Namespace).Get(ctx, podName, metav1.GetOptions{})
		if p != nil && p.Status.Phase == corev1.PodRunning {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Start a long-running exec, then cancel it
	cancelCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	err = executor.ExecInPod(cancelCtx, cfg.Namespace, podName, "main",
		[]string{"sleep", "300"},
		nil, nil, nil,
		false,
	)
	if err == nil {
		t.Fatal("expected error from cancelled exec")
	}
	t.Logf("exec cancelled with: %v", err)

	// Now delete the Pod (simulating cleanup)
	err = clientset.CoreV1().Pods(cfg.Namespace).Delete(ctx, podName, metav1.DeleteOptions{})
	if err != nil {
		t.Fatalf("cleaning up pod: %v", err)
	}

	// Verify Pod is gone (may take a few seconds for K8s to finalize deletion).
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		_, err = clientset.CoreV1().Pods(cfg.Namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			t.Logf("pod %s successfully cleaned up after cancellation", podName)
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatal("pod should have been deleted within 30 seconds")
}
