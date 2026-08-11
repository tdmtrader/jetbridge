//go:build live
// +build live

package jetbridge_test

import (
	"bytes"
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/postgresrunner"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

var livePostgresRunner postgresrunner.StandardTestRunner

func TestMain(m *testing.M) {
	os.Exit(livePostgresRunner.Main(m))
}

func useLiveJetbridgeDB(t *testing.T) jetbridgeDB {
	t.Helper()
	conn := livePostgresRunner.OpenConn(t)
	return jetbridgeDB{WorkerFactory: db.NewWorkerFactory(
		conn,
		db.NewStaticWorkerCache(lager.NewLogger("live-jetbridge-test"), conn, 0),
	)}
}

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

// adoptDaemonTLS makes the live config describe the daemon the cluster actually
// runs: where it stores artifacts, which port and service name address it, and
// whether it speaks HTTP or mTLS.
//
// Every one of those is a silent failure when guessed wrong, and they fail in
// different places. An unset host path is the worst: it leaves the worker with
// no storage backend at all, so no fetch-inputs init container is ever built.
// Nothing errors — the step pod just starts without its inputs and fails later
// on a missing file, pointing at the daemon rather than at the config that
// never asked it for anything. A wrong protocol is louder but just as
// misleading: the daemon serves HTTPS with mTLS whenever artifactDaemon.tls is
// set, which the chart REQUIRES once agentSnapshots is enabled, and a
// plain-HTTP caller gets 400 on every request.
//
// So the deployed DaemonSet is the source of truth for all of it, read from its
// own flags rather than assumed or passed in. Enabling TLS, renaming the
// service, or moving the storage path needs no change here and no change to
// whatever invokes these tests.
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

	adoptDaemonAddressing(t, cfg, dsNamespace, daemon)

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

	// The daemon authenticates every resolve with an HMAC capability the
	// caller mints, so a config without the signing key produces tokens the
	// daemon rejects and the fetch returns nothing. The real web gets this key
	// from the same Secret the chart mounts; adopt it here for the same reason
	// the certificates are adopted.
	adoptResolveCapability(t, clientset, cfg, dsNamespace, daemon)

	cfg.ArtifactDaemonTLSEnabled = true
	cfg.ArtifactDaemonTLSCACert = paths["ca.crt"]
	cfg.ArtifactDaemonTLSCert = paths["client.crt"]
	cfg.ArtifactDaemonTLSKey = paths["client.key"]

	t.Logf("daemon mTLS adopted from %s/%s (server name %s.%s.svc)",
		dsNamespace, secretName, cfg.ArtifactDaemonService, dsNamespace)
}

// adoptDaemonAddressing copies the deployed daemon's own storage path, port and
// service identity into the live config.
//
// These are read from the daemon's command line because that is what the daemon
// is actually running with; a value derived from the chart's defaults or from a
// caller's environment is a guess that happens to be right. In particular the
// storage path decides whether the worker gets a DaemonSet storage backend at
// all — empty means artifact passing is silently disabled rather than broken —
// and the service name decides which SAN the daemon's server certificate is
// verified against, so a stale name fails every ATC-side mTLS call while the
// init containers (which skip hostname verification) keep working.
//
// The namespace is the daemon's own, which is not the namespace these tests
// schedule their pods into: daemons are dialed by node IP, never a cert SAN, so
// verification has to name the headless service where the daemon actually runs.
func adoptDaemonAddressing(
	t *testing.T,
	cfg *jetbridge.Config,
	namespace string,
	daemon appsv1.DaemonSet,
) {
	t.Helper()

	cfg.ArtifactDaemonNamespace = namespace
	cfg.ArtifactDaemonService = daemon.Name

	container := daemon.Spec.Template.Spec.Containers[0]
	for _, arg := range append(append([]string{}, container.Command...), container.Args...) {
		switch {
		case strings.HasPrefix(arg, "--storage-path="):
			cfg.ArtifactDaemonHostPath = strings.TrimPrefix(arg, "--storage-path=")
		case strings.HasPrefix(arg, "--service-name="):
			cfg.ArtifactDaemonService = strings.TrimPrefix(arg, "--service-name=")
		case strings.HasPrefix(arg, "--port="):
			if port, err := strconv.Atoi(strings.TrimPrefix(arg, "--port=")); err == nil {
				cfg.ArtifactDaemonPort = port
			}
		}
	}

	t.Logf("daemon addressing adopted from %s/%s (storage path %q, service %s, port %d)",
		namespace, daemon.Name, cfg.ArtifactDaemonHostPath, cfg.ArtifactDaemonService, cfg.ArtifactDaemonPort)
}

// cleanupPod registers a t.Cleanup that deletes the named pod. This is used
// across live tests to ensure pods don't leak after test completion.
func cleanupPod(t *testing.T, clientset kubernetes.Interface, namespace, podName string) {
	t.Helper()
	t.Cleanup(func() {
		if t.Failed() {
			logInitContainers(t, clientset, namespace, podName)
		}
		_ = clientset.CoreV1().Pods(namespace).Delete(context.Background(), podName, metav1.DeleteOptions{})
	})
}

// logInitContainers reports what a failed test's init containers did, before
// cleanup deletes the pod that holds the only copy of it.
//
// Artifact fetching happens entirely in an init container, so when it goes
// wrong the test itself sees only a step that could not find its input — the
// explanation lives in a log that is deleted seconds later and is effectively
// impossible to catch by polling from outside. Reporting it here is the
// difference between "the file is missing" and knowing whether the daemon
// refused the request, returned nothing, or was never asked: a pod with no
// fetch-inputs container at all means the artifact backend was not configured,
// not that the daemon misbehaved.
func logInitContainers(t *testing.T, clientset kubernetes.Interface, namespace, podName string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		t.Logf("pod %s: unavailable for diagnosis: %v", podName, err)
		return
	}
	if len(pod.Spec.InitContainers) == 0 {
		t.Logf("pod %s: no init containers — nothing fetched this pod's inputs", podName)
		return
	}
	for _, status := range pod.Status.InitContainerStatuses {
		t.Logf("pod %s: init container %s: %+v", podName, status.Name, status.State)
	}
	for _, container := range pod.Spec.InitContainers {
		logs, err := clientset.CoreV1().Pods(namespace).
			GetLogs(podName, &corev1.PodLogOptions{Container: container.Name}).DoRaw(ctx)
		if err != nil {
			t.Logf("pod %s: no logs for init container %s: %v", podName, container.Name, err)
			continue
		}
		t.Logf("pod %s: init container %s logs:\n%s", podName, container.Name, string(logs))
	}
}

// requireArtifactBackend fails before a test that passes artifacts between
// steps can reach its confusing symptom.
//
// Without a storage backend the worker builds pods that never fetch their
// inputs, and the test that follows fails several steps later on a file that
// was never going to be there. That reads as a broken daemon rather than a
// cluster this suite could not find one in.
func requireArtifactBackend(t *testing.T, cfg *jetbridge.Config) {
	t.Helper()
	if cfg.ArtifactDaemonHostPath == "" {
		t.Fatalf("no artifact daemon found in namespace %s (set K8S_ARTIFACT_DAEMON_NAMESPACE): "+
			"artifact passing is unconfigured, so this test would fail on a missing input rather than on the behavior it covers",
			daemonNamespace())
	}
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

// adoptResolveCapability copies the daemon's resolve-capability HMAC key into
// the live config.
//
// The daemon authenticates every /resolve-batch with a short-lived capability
// the caller signs, so a config with no key mints tokens the daemon refuses.
// The failure is quiet in the worst way: the init container still exits 0 and
// the step pod still starts, but the requested artifact never lands, so the
// step fails later with a missing file rather than a fetch error.
//
// The key name comes from the daemon's own --resolve-capability-key flag so
// this follows a chart rename, and the Secret is found via the daemon's
// resolve-capability volume rather than a hardcoded name.
func adoptResolveCapability(
	t *testing.T,
	clientset kubernetes.Interface,
	cfg *jetbridge.Config,
	namespace string,
	daemon appsv1.DaemonSet,
) {
	t.Helper()

	var secretName string
	for _, volume := range daemon.Spec.Template.Spec.Volumes {
		if volume.Name == "resolve-capability" && volume.Secret != nil {
			secretName = volume.Secret.SecretName
			break
		}
	}
	if secretName == "" {
		return
	}

	keyName := "resolve.key"
	for _, arg := range daemon.Spec.Template.Spec.Containers[0].Command {
		if rest, found := strings.CutPrefix(arg, "--resolve-capability-key="); found {
			if base := rest[strings.LastIndex(rest, "/")+1:]; base != "" {
				keyName = base
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	secret, err := clientset.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("daemon resolve capability secret %s/%s is unreadable: %v", namespace, secretName, err)
	}
	key, found := secret.Data[keyName]
	if !found {
		t.Fatalf("resolve capability secret %s/%s has no %s", namespace, secretName, keyName)
	}

	cfg.ArtifactDaemonResolveCapabilityKey = key
	t.Logf("resolve capability adopted from %s/%s (%s, %d bytes)", namespace, secretName, keyName, len(key))
}
