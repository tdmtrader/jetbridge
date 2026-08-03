package jetbridge

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestExecProcessCheckpointCaptureQuiescesExactPodAndResumesIdempotently(t *testing.T) {
	pod := checkpointTestPod("agent-42", "uid-42", "main", "sidecar")
	executor := &checkpointTestExecutor{}
	process := checkpointTestProcess(fake.NewClientset(pod), executor)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	lease, err := process.AcquireCheckpointCapture(ctx, 4096)
	if err != nil {
		t.Fatal(err)
	}
	target := lease.CaptureTarget()
	if target.ContainerHandle != "agent-42" || target.PodName != "exact-pod" || target.PodUID != "uid-42" || target.NodeName != "node-a" || target.Archive.MaxBytes != 4096 {
		t.Fatalf("capture target = %#v", target)
	}
	if got := executor.held(); got != 2 {
		t.Fatalf("held helpers = %d, want 2", got)
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := executor.released(); got != 2 {
		t.Fatalf("released helpers = %d, want 2", got)
	}
}

func TestExecProcessCheckpointCaptureRejectsUnsafePodBeforeExec(t *testing.T) {
	tests := []func(*corev1.Pod){
		func(pod *corev1.Pod) { pod.Labels[typeLabelKey] = "task" },
		func(pod *corev1.Pod) { pod.Status.Phase = corev1.PodFailed },
		func(pod *corev1.Pod) { now := metav1.Now(); pod.DeletionTimestamp = &now },
		func(pod *corev1.Pod) { pod.Spec.NodeName = "" },
		func(pod *corev1.Pod) {
			pod.Spec.EphemeralContainers = []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug"}}}
		},
		func(pod *corev1.Pod) { shared := true; pod.Spec.ShareProcessNamespace = &shared },
		func(pod *corev1.Pod) { pod.Status.ContainerStatuses = pod.Status.ContainerStatuses[:1] },
	}
	for _, mutate := range tests {
		pod := checkpointTestPod("agent-42", "uid-42", "main", "sidecar")
		mutate(pod)
		executor := &checkpointTestExecutor{}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := checkpointTestProcess(fake.NewClientset(pod), executor).AcquireCheckpointCapture(ctx, 4096)
		cancel()
		if err == nil {
			t.Fatal("accepted unsafe checkpoint pod")
		}
		if executor.calls() != 0 {
			t.Fatalf("unsafe pod started %d helpers", executor.calls())
		}
	}
}

func TestExecProcessCheckpointCaptureUnwindsPartialAndProtocolFailures(t *testing.T) {
	t.Run("later container failure", func(t *testing.T) {
		executor := &checkpointTestExecutor{failContainer: "sidecar"}
		process := checkpointTestProcess(fake.NewClientset(checkpointTestPod("agent-42", "uid-42", "main", "sidecar")), executor)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, err := process.AcquireCheckpointCapture(ctx, 4096); err == nil {
			t.Fatal("accepted a partially acquired process lease")
		}
		if got := executor.released(); got != 1 {
			t.Fatalf("released helpers = %d, want 1", got)
		}
		executor.failContainer = ""
		lease, err := process.AcquireCheckpointCapture(ctx, 4096)
		if err != nil {
			t.Fatalf("partial failure retained reservation: %v", err)
		}
		if err := lease.Release(context.Background()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("mutated protocol", func(t *testing.T) {
		executor := &checkpointTestExecutor{protocol: "READY 1\nMUTATED\n"}
		process := checkpointTestProcess(fake.NewClientset(checkpointTestPod("agent-42", "uid-42", "main")), executor)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, err := process.AcquireCheckpointCapture(ctx, 4096); err == nil {
			t.Fatal("accepted mutated helper protocol")
		}
		if got := executor.released(); got != 1 {
			t.Fatalf("mutated helper was not resumed: %d", got)
		}
	})
}

func TestExecProcessCheckpointCaptureRejectsIdentityChangeAndDuplicateLease(t *testing.T) {
	t.Run("identity changes after ready", func(t *testing.T) {
		pod := checkpointTestPod("agent-42", "uid-42", "main")
		client := fake.NewClientset(pod)
		changed := make(chan struct{})
		getCount := 0
		client.Fake.PrependReactor("get", "pods", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
			getCount++
			count := getCount
			if count == 2 {
				<-changed
			}
			return false, nil, nil
		})
		executor := &checkpointTestExecutor{afterReady: func() {
			changedPod := pod.DeepCopy()
			changedPod.UID = types.UID("uid-replaced")
			_ = client.Tracker().Update(corev1.SchemeGroupVersion.WithResource("pods"), changedPod, "test")
			close(changed)
		}}
		process := checkpointTestProcess(client, executor)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, err := process.AcquireCheckpointCapture(ctx, 4096); err == nil {
			t.Fatal("accepted changed pod identity")
		}
		if got := executor.released(); got != 1 {
			t.Fatalf("identity-changed helper was not resumed: %d", got)
		}
	})

	t.Run("duplicate active lease", func(t *testing.T) {
		executor := &checkpointTestExecutor{}
		process := checkpointTestProcess(fake.NewClientset(checkpointTestPod("agent-42", "uid-42", "main")), executor)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		lease, err := process.AcquireCheckpointCapture(ctx, 4096)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := process.AcquireCheckpointCapture(ctx, 4096); err == nil {
			t.Fatal("accepted duplicate active lease")
		}
		if got := executor.calls(); got != 1 {
			t.Fatalf("duplicate lease launched helper count %d", got)
		}
		if err := lease.Release(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestExecProcessCheckpointCaptureCanceledAndBoundedRelease(t *testing.T) {
	process := checkpointTestProcess(fake.NewClientset(checkpointTestPod("agent-42", "uid-42", "main")), &checkpointTestExecutor{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	cancel()
	if _, err := process.AcquireCheckpointCapture(ctx, 4096); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled acquisition error = %v", err)
	}

	hold := make(chan struct{})
	executor := &checkpointTestExecutor{holdUntil: hold}
	process = checkpointTestProcess(fake.NewClientset(checkpointTestPod("agent-42", "uid-42", "main")), executor)
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	lease, err := process.AcquireCheckpointCapture(ctx, 4096)
	if err != nil {
		t.Fatal(err)
	}
	concrete := lease.(*checkpointCaptureLease)
	concrete.releaseTimeout = 10 * time.Millisecond
	started := time.Now()
	if err := lease.Release(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unbounded release error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("release was not bounded: %s", elapsed)
	}
	if _, err := process.AcquireCheckpointCapture(ctx, 4096); err == nil {
		t.Fatal("timed out release freed reservation before helper exit")
	}
	close(hold)
	deadline := time.Now().Add(time.Second)
	for {
		next, err := process.AcquireCheckpointCapture(ctx, 4096)
		if err == nil {
			if releaseErr := next.Release(context.Background()); releaseErr != nil {
				t.Fatal(releaseErr)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("helper exit did not free reservation: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestExecProcessCheckpointCaptureReleaseUsesOwnBoundedResumeWindow(t *testing.T) {
	releaseStarted := make(chan struct{}, 1)
	releaseGate := make(chan struct{})
	executor := &checkpointTestExecutor{releaseStarted: releaseStarted, releaseGate: releaseGate}
	process := checkpointTestProcess(fake.NewClientset(checkpointTestPod("agent-42", "uid-42", "main")), executor)
	acquireCtx, cancelAcquire := context.WithTimeout(context.Background(), time.Second)
	defer cancelAcquire()
	lease, err := process.AcquireCheckpointCapture(acquireCtx, 4096)
	if err != nil {
		t.Fatal(err)
	}
	expiredCtx, cancelExpired := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancelExpired()
	<-expiredCtx.Done()
	released := make(chan error, 1)
	go func() { released <- lease.Release(expiredCtx) }()
	select {
	case <-releaseStarted:
	case <-time.After(time.Second):
		t.Fatal("release did not close the checkpoint helper input")
	}
	close(releaseGate)
	select {
	case err := <-released:
		if err != nil {
			t.Fatalf("release reused the expired caller context: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("release did not finish within its own bound")
	}
}

func TestExecProcessCheckpointCaptureRequiresDeadlineAndAutoReleases(t *testing.T) {
	pod := checkpointTestPod("agent-42", "uid-42", "main")
	releaseStarted := make(chan struct{}, 1)
	releaseGate := make(chan struct{})
	executor := &checkpointTestExecutor{releaseStarted: releaseStarted, releaseGate: releaseGate}
	process := checkpointTestProcess(fake.NewClientset(pod), executor)
	if _, err := process.AcquireCheckpointCapture(context.Background(), 4096); err == nil {
		t.Fatal("accepted checkpoint capture without a deadline")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	lease, err := process.AcquireCheckpointCapture(ctx, 4096)
	if err != nil {
		t.Fatal(err)
	}
	concrete := lease.(*checkpointCaptureLease)
	select {
	case <-releaseStarted:
	case <-time.After(time.Second):
		t.Fatal("deadline did not begin releasing checkpoint helper")
	}
	close(releaseGate)
	select {
	case <-concrete.done:
	case <-time.After(time.Second):
		t.Fatal("deadline release did not finish")
	}
	if concrete.releaseErr != nil {
		t.Fatalf("deadline release reused the expired acquisition context: %v", concrete.releaseErr)
	}
	if got := executor.released(); got != 1 {
		t.Fatalf("deadline released helpers = %d, want 1", got)
	}
	ctx, nextCancel := context.WithTimeout(context.Background(), time.Second)
	defer nextCancel()
	lease, err = process.AcquireCheckpointCapture(ctx, 4096)
	if err != nil {
		t.Fatalf("automatic release retained active handle: %v", err)
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointQuiescenceExitsAfterResumingOnTerminationSignals(t *testing.T) {
	if strings.Contains(checkpointQuiescenceShell, "trap 'resume' 0 HUP INT TERM") {
		t.Fatal("checkpoint signal trap resumes processes but leaves the quiescence helper running")
	}
	for _, required := range []string{
		"trap 'resume' 0",
		"trap 'exit 129' HUP",
		"trap 'exit 130' INT",
		"trap 'exit 143' TERM",
	} {
		if !strings.Contains(checkpointQuiescenceShell, required) {
			t.Errorf("checkpoint quiescence signal handling lacks %q", required)
		}
	}
}

func TestCheckpointQuiescenceEnrollsProcessBeforeStoppingIt(t *testing.T) {
	enroll := strings.Index(checkpointQuiescenceShell, `PAIRS="$PAIRS $pair"`)
	stop := strings.Index(checkpointQuiescenceShell, `kill -STOP "$pid"`)
	if enroll < 0 || stop < 0 {
		t.Fatal("checkpoint quiescence shell lacks process enrollment or STOP")
	}
	if enroll > stop {
		t.Fatal("checkpoint quiescence can stop a process before EXIT cleanup knows to resume it")
	}
}

func checkpointTestProcess(clientset *fake.Clientset, executor PodExecutor) *execProcess {
	config := Config{Namespace: "test", ArtifactDaemonHostPath: "/var/lib/concourse/artifacts"}
	container := newContainer("agent-42", db.ContainerMetadata{Type: db.ContainerTypeAgent}, runtime.ContainerSpec{
		Type: db.ContainerTypeAgent, CheckpointCapture: true, Hermetic: true, Dir: "/tmp/build",
	}, nil, clientset, config, "worker", executor, nil, NewDaemonSetBackend(config, nil, nil), false)
	return &execProcess{podName: "exact-pod", clientset: clientset, config: config, container: container, executor: executor}
}

func checkpointTestPod(handle, uid string, names ...string) *corev1.Pod {
	containers := make([]corev1.Container, 0, len(names))
	statuses := make([]corev1.ContainerStatus, 0, len(names))
	for _, name := range names {
		containers = append(containers, corev1.Container{Name: name, Image: "busybox"})
		statuses = append(statuses, corev1.ContainerStatus{Name: name, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}})
	}
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "exact-pod", Namespace: "test", UID: types.UID(uid), Labels: map[string]string{handleLabelKey: handle, typeLabelKey: "agent", hermeticLabelKey: "true"}}, Spec: corev1.PodSpec{NodeName: "node-a", Containers: containers}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: statuses}}
}

type checkpointTestExecutor struct {
	mutex                               sync.Mutex
	callCount, heldCount, releasedCount int
	failContainer                       string
	protocol                            string
	afterReady                          func()
	holdUntil                           <-chan struct{}
	releaseStarted                      chan<- struct{}
	releaseGate                         <-chan struct{}
}

func (executor *checkpointTestExecutor) ExecInPod(_ context.Context, _ string, _ string, container string, _ []string, stdin io.Reader, stdout, _ io.Writer, _ bool, _ ExecAttrs) error {
	executor.mutex.Lock()
	executor.callCount++
	fail := executor.failContainer == container
	protocol := executor.protocol
	afterReady := executor.afterReady
	holdUntil := executor.holdUntil
	releaseStarted := executor.releaseStarted
	releaseGate := executor.releaseGate
	if holdUntil != nil {
		executor.holdUntil = nil
	}
	executor.heldCount++
	executor.mutex.Unlock()
	if fail {
		return errors.New("helper failed")
	}
	if protocol == "" {
		protocol = "READY 1\n"
	}
	if _, err := io.WriteString(stdout, protocol); err != nil {
		return err
	}
	if afterReady != nil {
		afterReady()
	}
	if holdUntil != nil {
		<-holdUntil
		executor.mutex.Lock()
		executor.releasedCount++
		executor.mutex.Unlock()
		return nil
	}
	_, err := io.Copy(io.Discard, stdin)
	if releaseStarted != nil {
		releaseStarted <- struct{}{}
	}
	if releaseGate != nil {
		<-releaseGate
	}
	executor.mutex.Lock()
	executor.releasedCount++
	executor.mutex.Unlock()
	return err
}
func (executor *checkpointTestExecutor) calls() int {
	executor.mutex.Lock()
	defer executor.mutex.Unlock()
	return executor.callCount
}
func (executor *checkpointTestExecutor) held() int {
	executor.mutex.Lock()
	defer executor.mutex.Unlock()
	return executor.heldCount
}
func (executor *checkpointTestExecutor) released() int {
	executor.mutex.Lock()
	defer executor.mutex.Unlock()
	return executor.releasedCount
}
