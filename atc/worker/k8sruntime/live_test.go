//go:build live
// +build live

package k8sruntime_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/worker/k8sruntime"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func liveTestNamespace() string {
	if ns := os.Getenv("K8S_TEST_NAMESPACE"); ns != "" {
		return ns
	}
	return "concourse"
}

func kubeClient(t *testing.T) (kubernetes.Interface, *k8sruntime.Config) {
	t.Helper()

	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		// Check if the default kubeconfig file exists; if not, leave it
		// empty so NewConfig/NewClientset will fall back to in-cluster config.
		home, _ := os.UserHomeDir()
		candidate := home + "/.kube/config"
		if _, err := os.Stat(candidate); err == nil {
			kubeconfig = candidate
		}
	}

	// When running inside a K8s pod (SA token exists) but the standard
	// KUBERNETES_SERVICE_HOST env var isn't set (some container runtimes
	// don't inject it), set it to the well-known in-cluster DNS name so
	// that rest.InClusterConfig() succeeds.
	if kubeconfig == "" {
		if _, err := os.Stat("/var/run/secrets/kubernetes.io/serviceaccount/token"); err == nil {
			if os.Getenv("KUBERNETES_SERVICE_HOST") == "" {
				os.Setenv("KUBERNETES_SERVICE_HOST", "kubernetes.default.svc")
				os.Setenv("KUBERNETES_SERVICE_PORT", "443")
			}
		}
	}

	ns := liveTestNamespace()
	cfg := k8sruntime.NewConfig(ns, kubeconfig)
	clientset, err := k8sruntime.NewClientset(cfg)
	if err != nil {
		t.Fatalf("creating clientset: %v", err)
	}
	return clientset, &cfg
}

func TestLiveCountActivePods(t *testing.T) {
	clientset, _ := kubeClient(t)
	ctx := context.Background()
	ns := liveTestNamespace()

	pods, err := clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "concourse.ci/worker",
	})
	if err != nil {
		t.Fatalf("listing pods: %v", err)
	}

	count := 0
	for _, p := range pods.Items {
		if p.Status.Phase == corev1.PodRunning {
			count++
		}
	}
	t.Logf("found %d active pods with concourse.ci/worker label in namespace %s", count, ns)
	// This test is informational — it reports how many worker-managed pods exist.
	// It does not fail if zero are found since that depends on whether
	// Concourse web is actively scheduling work.
}

func TestLiveExecInPod(t *testing.T) {
	clientset, cfg := kubeClient(t)
	ctx := context.Background()
	ns := liveTestNamespace()

	restConfig, err := k8sruntime.RestConfig(*cfg)
	if err != nil {
		t.Fatalf("creating rest config: %v", err)
	}
	executor := k8sruntime.NewSPDYExecutor(clientset, restConfig)

	// Create a dedicated pod for exec tests instead of requiring a pre-existing one.
	podName := "live-exec-" + time.Now().Format("150405")
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: ns,
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

	t.Logf("creating exec test pod %s in namespace %s", podName, ns)
	_, err = clientset.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("creating exec test pod: %v", err)
	}
	defer func() {
		t.Logf("cleaning up pod %s", podName)
		_ = clientset.CoreV1().Pods(ns).Delete(context.Background(), podName, metav1.DeleteOptions{})
	}()

	// Wait for Running
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		p, err := clientset.CoreV1().Pods(ns).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("getting pod status: %v", err)
		}
		if p.Status.Phase == corev1.PodRunning {
			t.Logf("pod %s is Running", podName)
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Test 1: Simple echo command with stdout capture
	t.Run("echo command captures stdout", func(t *testing.T) {
		var stdout bytes.Buffer
		var stderr bytes.Buffer

		err := executor.ExecInPod(ctx, ns, podName, "main",
			[]string{"echo", "hello from k8s"},
			nil, &stdout, &stderr,
		false)
		if err != nil {
			t.Fatalf("exec failed: %v", err)
		}

		output := strings.TrimSpace(stdout.String())
		if output != "hello from k8s" {
			t.Fatalf("expected 'hello from k8s', got %q", output)
		}
		t.Logf("stdout: %q", output)
		t.Logf("stderr: %q", stderr.String())
	})

	// Test 2: stdin piping
	t.Run("stdin piping works", func(t *testing.T) {
		var stdout bytes.Buffer
		stdin := strings.NewReader("data from stdin\n")

		err := executor.ExecInPod(ctx, ns, podName, "main",
			[]string{"cat"},
			stdin, &stdout, nil,
		false)
		if err != nil {
			t.Fatalf("exec with stdin failed: %v", err)
		}

		output := strings.TrimSpace(stdout.String())
		if output != "data from stdin" {
			t.Fatalf("expected 'data from stdin', got %q", output)
		}
		t.Logf("stdin->stdout passthrough: %q", output)
	})

	// Test 3: Non-zero exit code
	t.Run("non-zero exit code returns ExecExitError", func(t *testing.T) {
		err := executor.ExecInPod(ctx, ns, podName, "main",
			[]string{"sh", "-c", "exit 42"},
			nil, nil, nil,
		false)
		if err == nil {
			t.Fatal("expected error for non-zero exit code")
		}

		var exitErr *k8sruntime.ExecExitError
		if !isExecExitError(err, &exitErr) {
			t.Fatalf("expected ExecExitError, got %T: %v", err, err)
		}
		if exitErr.ExitCode != 42 {
			t.Fatalf("expected exit code 42, got %d", exitErr.ExitCode)
		}
		t.Logf("correctly got exit code %d", exitErr.ExitCode)
	})

	// Test 4: JSON protocol round-trip (simulates resource get/put)
	t.Run("JSON protocol stdin/stdout round-trip", func(t *testing.T) {
		jsonInput := `{"source":{"uri":"https://example.com"},"version":{"ref":"abc123"}}`
		stdin := strings.NewReader(jsonInput)
		var stdout bytes.Buffer

		err := executor.ExecInPod(ctx, ns, podName, "main",
			[]string{"cat"},
			stdin, &stdout, nil,
		false)
		if err != nil {
			t.Fatalf("JSON round-trip failed: %v", err)
		}

		output := strings.TrimSpace(stdout.String())
		if output != jsonInput {
			t.Fatalf("JSON mismatch.\nexpected: %s\ngot:      %s", jsonInput, output)
		}
		t.Logf("JSON round-trip successful: %s", output)
	})

	// Test 5: stderr separation
	t.Run("stderr is separated from stdout", func(t *testing.T) {
		var stdout, stderr bytes.Buffer

		err := executor.ExecInPod(ctx, ns, podName, "main",
			[]string{"sh", "-c", "echo out-data; echo err-data >&2"},
			nil, &stdout, &stderr,
		false)
		if err != nil {
			t.Fatalf("exec failed: %v", err)
		}

		outStr := strings.TrimSpace(stdout.String())
		errStr := strings.TrimSpace(stderr.String())

		if outStr != "out-data" {
			t.Fatalf("expected stdout 'out-data', got %q", outStr)
		}
		if errStr != "err-data" {
			t.Fatalf("expected stderr 'err-data', got %q", errStr)
		}
		t.Logf("stdout: %q, stderr: %q — correctly separated", outStr, errStr)
	})

	// Test 6: Context cancellation
	t.Run("context cancellation stops exec", func(t *testing.T) {
		cancelCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()

		err := executor.ExecInPod(cancelCtx, ns, podName, "main",
			[]string{"sleep", "300"},
			nil, nil, nil,
		false)
		if err == nil {
			t.Fatal("expected error on context cancellation")
		}
		t.Logf("context cancellation error: %v", err)
	})
}

func isExecExitError(err error, target **k8sruntime.ExecExitError) bool {
	if e, ok := err.(*k8sruntime.ExecExitError); ok {
		*target = e
		return true
	}
	return false
}
