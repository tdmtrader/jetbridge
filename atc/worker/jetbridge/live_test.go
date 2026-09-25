//go:build live
// +build live

package jetbridge_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/postgresrunner"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/onsi/gomega"
	"github.com/tedsuo/ifrit"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// livePostgresRunner is the package's one postmaster; each test that needs a
// database gets a fresh copy of the migrated template from it.
//
// postgresrunner.Runner asserts with Gomega, which panics unless a fail
// handler is registered; these are plain testing.T tests with no Ginkgo suite
// to supply one, so TestMain registers a handler that panics with the message.
// A bootstrap failure then kills the run loudly instead of leaving every test
// to fail on a connection it never got.
var livePostgresRunner postgresrunner.Runner

func TestMain(m *testing.M) {
	os.Exit(runLive(m))
}

func runLive(m *testing.M) int {
	gomega.RegisterFailHandler(func(message string, _ ...int) {
		panic("postgresrunner: " + message)
	})

	// The `live` build's half of the temp guard: the untagged build has a
	// TestMain of its own for this and a binary may hold only one, so the rule
	// -- this process takes its temp root with it -- is spelled in both.
	before := jetbridge.TempSuspects()

	// The deployment is a precondition of the whole tier, not something each
	// test may find missing and quietly work around. Discovery runs before the
	// postmaster so a missing or unreadable deployment costs one message, and
	// its credentials directory is gone again before the temp guard looks.
	tlsRoot, err := os.MkdirTemp("", "jetbridge-live-daemon-tls-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "live tier precondition:", err)
		return 1
	}
	defer os.RemoveAll(tlsRoot)
	clientset, err := jetbridge.NewClientset(jetbridge.NewConfig(liveTestNamespace(), liveKubeconfig()))
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		deployed, err = discoverDeployment(ctx, clientset, releaseNamespace(), tlsRoot)
		cancel()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "live tier precondition: no usable JetBridge deployment:", err)
		return 1
	}
	fmt.Println("live tier runs against", deployed)

	livePostgresRunner = postgresrunner.Runner{Port: postgresrunner.PickPort()}
	process := ifrit.Invoke(livePostgresRunner)
	livePostgresRunner.InitializeTestDBTemplate()

	code := m.Run()

	process.Signal(os.Interrupt)
	<-process.Wait()
	os.RemoveAll(tlsRoot)

	if leaks := jetbridge.TempLeaks(before); len(leaks) != 0 {
		for _, leak := range leaks {
			fmt.Fprintln(os.Stderr, "temp leak:", leak)
		}
		if code == 0 {
			code = 1
		}
	}

	return code
}

// liveDBs hands each test exactly one database for its whole lifetime. The
// runner names the test database once ("testdb"), so a test that asks twice —
// TestLiveTaskResume builds two webs over one schema — must get the same
// connection back, not a second CREATE DATABASE that fails on the first.
var (
	liveDBsMu sync.Mutex
	liveDBs   = map[*testing.T]db.DbConn{}
)

func useLiveJetbridgeDB(t *testing.T) jetbridgeDB {
	t.Helper()

	liveDBsMu.Lock()
	defer liveDBsMu.Unlock()

	conn, ok := liveDBs[t]
	if !ok {
		conn = openLiveDB(t)
		liveDBs[t] = conn
		t.Cleanup(func() {
			liveDBsMu.Lock()
			delete(liveDBs, t)
			liveDBsMu.Unlock()
			_ = conn.Close()
			// A worker goroutine can still hold a connection here; the runner
			// asserts the drop succeeded and the fail handler panics. Losing
			// one database's cleanup must not take the rest of the run with it.
			runnerQuietly(func() { livePostgresRunner.DropTestDB() }, func(msg string) {
				t.Logf("dropping the test database: %s", msg)
			})
		})
	}
	return jetbridgeDB{WorkerFactory: db.NewWorkerFactory(
		conn,
		db.NewStaticWorkerCache(lager.NewLogger("live-jetbridge-test"), conn, 0),
	)}
}

// openLiveDB creates the test database from the template. A leftover from a
// test whose drop failed makes the create fail; drop and try once more before
// giving up, so one leaked connection costs a log line, not the whole tier.
func openLiveDB(t *testing.T) db.DbConn {
	t.Helper()
	created := false
	runnerQuietly(func() { livePostgresRunner.CreateTestDBFromTemplate(); created = true }, func(msg string) {
		t.Logf("creating the test database: %s; dropping the leftover and retrying", msg)
	})
	if !created {
		runnerQuietly(func() { livePostgresRunner.DropTestDB() }, func(msg string) {
			t.Logf("dropping the leftover test database: %s", msg)
		})
		livePostgresRunner.CreateTestDBFromTemplate()
	}
	return livePostgresRunner.OpenConn()
}

// runnerQuietly runs a postgresrunner call whose Gomega assertion would panic
// through the fail handler registered in TestMain, and reports the message to
// onFail instead of unwinding the test.
func runnerQuietly(run func(), onFail func(msg string)) {
	defer func() {
		if r := recover(); r != nil {
			onFail(fmt.Sprint(r))
		}
	}()
	run()
}

func liveTestNamespace() string {
	if ns := os.Getenv("K8S_TEST_NAMESPACE"); ns != "" {
		return ns
	}
	return "concourse"
}

func kubeClient(t *testing.T) (kubernetes.Interface, *jetbridge.Config) {
	t.Helper()

	cfg := jetbridge.NewConfig(liveTestNamespace(), liveKubeconfig())
	clientset, err := jetbridge.NewClientset(cfg)
	if err != nil {
		t.Fatalf("creating clientset: %v", err)
	}
	deployed.configure(&cfg)
	return clientset, &cfg
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
