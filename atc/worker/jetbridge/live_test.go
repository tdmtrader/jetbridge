//go:build live
// +build live

package jetbridge_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/worker/jetbridge"
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

func kubeClient(t *testing.T) (kubernetes.Interface, *jetbridge.Config) {
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
	cfg := jetbridge.NewConfig(ns, kubeconfig)
	clientset, err := jetbridge.NewClientset(cfg)
	if err != nil {
		t.Fatalf("creating clientset: %v", err)
	}
	adoptDaemonTLS(t, clientset, &cfg)
	return clientset, &cfg
}

// daemonNamespace is where the artifact daemon actually runs, which is not the
// namespace these tests schedule their own pods into (K8S_TEST_NAMESPACE).
func daemonNamespace() string {
	if ns := os.Getenv("K8S_ARTIFACT_DAEMON_NAMESPACE"); ns != "" {
		return ns
	}
	return "cicd"
}

// adoptDaemonTLS makes the live config speak whatever protocol the deployed
// daemon actually speaks.
//
// The daemon serves HTTPS with mTLS whenever artifactDaemon.tls.enabled is set,
// which the chart REQUIRES once agentSnapshots is enabled. A config without the
// TLS fields addresses it as plain HTTP and every request comes back 400, so
// these tests are only meaningful if they track the cluster rather than assume
// a protocol. Detection is from the live DaemonSet, not an env var, so enabling
// or disabling TLS needs no change here.
//
// Absent or unreadable daemon state leaves the config untouched: a cluster
// without the daemon, or a caller without permission to read it, still runs
// every test that does not talk to the daemon.
func adoptDaemonTLS(t *testing.T, clientset kubernetes.Interface, cfg *jetbridge.Config) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	dsNamespace := daemonNamespace()
	daemons, err := clientset.AppsV1().DaemonSets(dsNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/component=artifact-daemon",
	})
	if err != nil || len(daemons.Items) == 0 {
		return
	}
	daemon := daemons.Items[0]

	var secretName string
	for _, volume := range daemon.Spec.Template.Spec.Volumes {
		if volume.Name == "daemon-tls" && volume.Secret != nil {
			secretName = volume.Secret.SecretName
			break
		}
	}
	if secretName == "" {
		// TLS is off; the default plain-HTTP config is already correct.
		return
	}

	secret, err := clientset.CoreV1().Secrets(dsNamespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("daemon TLS is enabled but its secret %s/%s is unreadable: %v", dsNamespace, secretName, err)
	}

	dir := t.TempDir()
	paths := map[string]string{}
	for _, key := range []string{"ca.crt", "client.crt", "client.key"} {
		data, found := secret.Data[key]
		if !found {
			t.Fatalf("daemon TLS secret %s/%s has no %s", dsNamespace, secretName, key)
		}
		path := dir + "/" + key
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
		paths[key] = path
	}

	cfg.ArtifactDaemonTLSEnabled = true
	cfg.ArtifactDaemonTLSCACert = paths["ca.crt"]
	cfg.ArtifactDaemonTLSCert = paths["client.crt"]
	cfg.ArtifactDaemonTLSKey = paths["client.key"]
	// Daemons are dialed by node IP, which is not a SAN. The server cert
	// carries the headless service name in the DAEMON's namespace, which is
	// not this config's namespace.
	cfg.ArtifactDaemonNamespace = dsNamespace
	if cfg.ArtifactDaemonService == "" {
		cfg.ArtifactDaemonService = daemon.Name
	}

	t.Logf("daemon mTLS adopted from %s/%s (server name %s.%s.svc)",
		dsNamespace, secretName, cfg.ArtifactDaemonService, dsNamespace)
}

// cleanupPod registers a t.Cleanup that deletes the named pod. This is used
// across live tests to ensure pods don't leak after test completion.
func cleanupPod(t *testing.T, clientset kubernetes.Interface, namespace, podName string) {
	t.Helper()
	t.Cleanup(func() {
		_ = clientset.CoreV1().Pods(namespace).Delete(context.Background(), podName, metav1.DeleteOptions{})
	})
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

	restConfig, err := jetbridge.RestConfig(*cfg)
	if err != nil {
		t.Fatalf("creating rest config: %v", err)
	}
	executor := jetbridge.NewSPDYExecutor(clientset, restConfig)

	// Create a dedicated pod for exec tests instead of requiring a pre-existing one.
	podName := "live-exec-" + time.Now().Format("150405")
	cleanupPod(t, clientset, ns, podName)
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
			false, jetbridge.ExecAttrs{})
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
			false, jetbridge.ExecAttrs{})
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
			false, jetbridge.ExecAttrs{})
		if err == nil {
			t.Fatal("expected error for non-zero exit code")
		}

		var exitErr *jetbridge.ExecExitError
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
			false, jetbridge.ExecAttrs{})
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
			false, jetbridge.ExecAttrs{})
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
			false, jetbridge.ExecAttrs{})
		if err == nil {
			t.Fatal("expected error on context cancellation")
		}
		t.Logf("context cancellation error: %v", err)
	})
}

func isExecExitError(err error, target **jetbridge.ExecExitError) bool {
	if e, ok := err.(*jetbridge.ExecExitError); ok {
		*target = e
		return true
	}
	return false
}
