package main_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	daemon "github.com/concourse/concourse/cmd/artifact-daemon"
)

type stepPodsFunc func(context.Context) (map[string]struct{}, error)

func (f stepPodsFunc) Live(ctx context.Context) (map[string]struct{}, error) { return f(ctx) }

func liveHandles(handles ...string) daemon.StepPods {
	return stepPodsFunc(func(context.Context) (map[string]struct{}, error) {
		live := map[string]struct{}{}
		for _, h := range handles {
			live[h] = struct{}{}
		}
		return live, nil
	})
}

// expiredStepDir makes steps/<handle> with one file, aged past any TTL the
// tests use.
func expiredStepDir(t *testing.T, storagePath, handle string) string {
	t.Helper()
	dir := filepath.Join(storagePath, "steps", handle)
	if err := os.MkdirAll(filepath.Join(dir, "output"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "output", "file.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A step that outlives --ttl still has its inputs and live outputs under
// steps/<handle>. The directory's own mtime says nothing about that -- nothing
// touches it while the task runs -- so the sweeper asks the node which step
// pods are still alive, spares theirs, and restarts the TTL from this pass.
func TestSweeper_SparesTheDirectoryOfAStepWhosePodIsStillRunning(t *testing.T) {
	storagePath := t.TempDir()
	running := expiredStepDir(t, storagePath, "running-handle")
	finished := expiredStepDir(t, storagePath, "finished-handle")

	sweeper := daemon.NewSweeper(lagertest.NewTestLogger("sweeper"), storagePath, 2*time.Hour, 5*time.Minute, nil)
	sweeper.SetStepPods(liveHandles("running-handle"))

	before := time.Now().Add(-time.Second)
	sweeper.SweepOnce()

	info, err := os.Stat(running)
	if err != nil {
		t.Fatalf("the running step's directory was removed: %v", err)
	}
	if info.ModTime().Before(before) {
		t.Errorf("the running step's mtime was not refreshed by the sweep: %v", info.ModTime())
	}
	if _, err := os.Stat(filepath.Join(running, "output", "file.txt")); err != nil {
		t.Errorf("the running step's output was removed: %v", err)
	}
	if _, err := os.Stat(finished); !os.IsNotExist(err) {
		t.Errorf("the finished step's expired directory was not removed (err=%v)", err)
	}
}

// When the node cannot be asked, no step directory is destroyed on this pass,
// and none has its clock restarted either: the next pass that can ask decides.
func TestSweeper_SparesEveryStepDirectoryWhenThePodViewFails(t *testing.T) {
	storagePath := t.TempDir()
	expired := expiredStepDir(t, storagePath, "unknown-handle")

	untouched, err := os.Stat(expired)
	if err != nil {
		t.Fatal(err)
	}

	sweeper := daemon.NewSweeper(lagertest.NewTestLogger("sweeper"), storagePath, 2*time.Hour, 5*time.Minute, nil)
	sweeper.SetStepPods(stepPodsFunc(func(context.Context) (map[string]struct{}, error) {
		return nil, errors.New("the API server is away")
	}))

	sweeper.SweepOnce()

	info, err := os.Stat(expired)
	if err != nil {
		t.Fatalf("an expired step directory was removed while the pod view was unavailable: %v", err)
	}
	if !info.ModTime().Equal(untouched.ModTime()) {
		t.Errorf("the spared directory's mtime was changed to %v; a failed pod view must not restart its TTL", info.ModTime())
	}
}

// The in-cluster view: this node's step pods in the task namespace, named by
// the concourse.ci/handle label the web stamps on every step pod. A pod whose
// containers have all terminated is no longer a writer; a pod on another node
// is not this daemon's. The fake clientset ignores FieldSelector, so the
// in-code Spec.NodeName re-check is what excludes the other node's pod here.
func TestNodeStepPods_ListsTheHandlesOfThisNodesUnterminatedStepPods(t *testing.T) {
	pod := func(name, handle, node string, terminated bool) *corev1.Pod {
		state := corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
		if terminated {
			state = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}
		}
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "tasks", Labels: map[string]string{"concourse.ci/handle": handle}},
			Spec:       corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{Name: "task"}}},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "task", State: state}}},
		}
	}
	client := fake.NewSimpleClientset(
		pod("running", "h-running", "node-a", false),
		pod("done", "h-done", "node-a", true),
		pod("elsewhere", "h-elsewhere", "node-b", false),
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "unlabelled", Namespace: "tasks"}, Spec: corev1.PodSpec{NodeName: "node-a"}},
	)

	live, err := daemon.NewNodeStepPods(client, "tasks", "node-a").Live(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := live["h-running"]; !ok {
		t.Errorf("the running step pod's handle is missing from %v", live)
	}
	if _, ok := live["h-done"]; ok {
		t.Errorf("a terminated step pod's handle is reported live: %v", live)
	}
	if _, ok := live["h-elsewhere"]; ok {
		t.Errorf("another node's step pod is reported live here: %v", live)
	}
	if len(live) != 1 {
		t.Errorf("want exactly one live handle, got %v", live)
	}
}

// A daemon whose step pod view cannot be read -- the pods Role is missing --
// must say so at startup; main exits on this error rather than sweeping no
// step directory for the life of the process.
func TestProbeStepPods_FailsWhenTheNodesStepPodsCannotBeListed(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("pods is forbidden")
	})
	if err := daemon.ProbeStepPods(context.Background(), daemon.NewNodeStepPods(client, "tasks", "node-a")); err == nil {
		t.Fatal("a forbidden list probed as healthy")
	}
	if err := daemon.ProbeStepPods(context.Background(), daemon.NewNodeStepPods(fake.NewSimpleClientset(), "tasks", "node-a")); err != nil {
		t.Fatalf("an empty, readable node probed unhealthy: %v", err)
	}
}
