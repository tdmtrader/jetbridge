package jetbridge

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/agent/hangar"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestCheckpointSessionVolumeIsDaemonBackedAndMainOnly(t *testing.T) {
	c := newContainer("agent-42", db.ContainerMetadata{Type: db.ContainerTypeAgent}, runtime.ContainerSpec{
		Type:              db.ContainerTypeAgent,
		CheckpointCapture: true,
		Dir:               "/tmp/build",
		Inputs:            []runtime.Input{{DestinationPath: "/tmp/build/input"}},
		Outputs:           runtime.OutputPaths{"output": "/tmp/build/output"},
		ImageSpec:         runtime.ImageSpec{ImageURL: "busybox"},
		Sidecars:          []atc.SidecarConfig{{Name: "sidecar", Image: "busybox"}},
		SecretMounts:      []runtime.SecretMount{{SecretName: "platform", MountPath: "/run/concourse/platform"}},
	}, nil, nil, Config{ArtifactDaemonHostPath: "/var/lib/concourse/artifacts"}, "worker", nil, nil, NewDaemonSetBackend(Config{ArtifactDaemonHostPath: "/var/lib/concourse/artifacts"}, nil, nil), false)

	volume, mount, err := c.checkpointSessionVolume()
	if err != nil {
		t.Fatal(err)
	}
	if volume == nil || volume.HostPath == nil || volume.HostPath.Path != "/var/lib/concourse/artifacts/steps/agent-42/session" {
		t.Fatalf("checkpoint volume = %#v", volume)
	}
	if mount == nil || mount.MountPath != "/tmp/build/.concourse/session" || mount.ReadOnly {
		t.Fatalf("checkpoint mount = %#v", mount)
	}
	pod, err := c.buildPod(runtime.ProcessSpec{Dir: "/tmp/build"}, []string{"sleep"}, []string{"100"})
	if err != nil {
		t.Fatal(err)
	}
	if !podContainerHasMount(pod.Spec.Containers[0], checkpointSessionVolumeName) {
		t.Fatal("main container is missing checkpoint session mount")
	}
	if podContainerHasMount(pod.Spec.Containers[1], checkpointSessionVolumeName) {
		t.Fatal("sidecar received checkpoint session mount")
	}
	for _, init := range pod.Spec.InitContainers {
		if podContainerHasMount(init, checkpointSessionVolumeName) {
			t.Fatal("init container received checkpoint session mount")
		}
	}
}

func TestCheckpointRestoreGateIsPrivateAndPrecedesHermeticPreparation(t *testing.T) {
	ref, err := hangar.NewObjectRef(hangar.KindCheckpoint, hangar.Digest("sha256:"+strings.Repeat("a", 64)), 7)
	if err != nil {
		t.Fatal(err)
	}
	config := Config{ArtifactDaemonHostPath: "/var/lib/concourse/artifacts", ArtifactHelperImage: "helper@sha256:deadbeef"}
	backend := NewDaemonSetBackend(config, nil, nil)
	backend.daemonClient = &DaemonClient{scheme: "https"}
	c := newContainer("agent-42", db.ContainerMetadata{Type: db.ContainerTypeAgent}, runtime.ContainerSpec{
		Type: db.ContainerTypeAgent, Hermetic: true, CheckpointCapture: true, Dir: "/tmp/build",
		ImageSpec:         runtime.ImageSpec{ImageURL: "busybox"},
		CheckpointRestore: &runtime.CheckpointRestoreDescriptor{Archive: checkpoint.Archive{Ref: ref}, MaterializationID: "materialization-2", MaxBytes: 1024, MaxEntries: 16},
		Sidecars:          []atc.SidecarConfig{{Name: "sidecar", Image: "busybox"}},
	}, nil, nil, config, "worker", checkpointRestoreTestExecutor{}, nil, backend, false)
	pod, err := c.buildPod(runtime.ProcessSpec{Dir: "/tmp/build"}, []string{"sleep"}, []string{"100"})
	if err != nil {
		t.Fatal(err)
	}
	gate := checkpointRestoreContainerByName(pod.Spec.InitContainers, checkpointRestoreGateInitName)
	if gate.Name == "" || !podContainerHasMount(gate, checkpointRestoreGateVolumeName) {
		t.Fatalf("restore gate = %#v", gate)
	}
	if !gate.VolumeMounts[0].ReadOnly || podContainerHasMount(pod.Spec.Containers[0], checkpointRestoreGateVolumeName) || podContainerHasMount(pod.Spec.Containers[1], checkpointRestoreGateVolumeName) {
		t.Fatal("restore gate leaked to a regular container")
	}
	if len(pod.Spec.InitContainers) < 2 || pod.Spec.InitContainers[len(pod.Spec.InitContainers)-2].Name != checkpointRestoreGateInitName || pod.Spec.InitContainers[len(pod.Spec.InitContainers)-1].Name != "prepare-hermetic-workspace" {
		t.Fatalf("restore init order = %#v", pod.Spec.InitContainers)
	}
	volume := checkpointRestoreVolumeByName(pod.Spec.Volumes, checkpointRestoreGateVolumeName)
	if volume == nil || volume.HostPath == nil || volume.HostPath.Path != "/var/lib/concourse/artifacts/.checkpoint-restore-gates/agent-42/"+checkpoint.RestoreGateLeafName("materialization-2") || volume.HostPath.Type == nil || *volume.HostPath.Type != corev1.HostPathDirectoryOrCreate {
		t.Fatalf("restore gate volume = %#v", volume)
	}
}

func TestCheckpointRecoveryGateStateUsesVerificationOnlyAfterGateSuccess(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{InitContainers: []corev1.Container{{Name: "fetch"}, {Name: checkpointRestoreGateInitName}, {Name: "prepare-hermetic-workspace"}}},
		Status: corev1.PodStatus{Phase: corev1.PodPending, InitContainerStatuses: []corev1.ContainerStatus{
			{Name: "fetch", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}},
			{Name: checkpointRestoreGateInitName, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}},
		}},
	}
	mode, err := checkpointRecoveryGateState(pod)
	if err != nil || mode != checkpointRecoveryGateRestore {
		t.Fatalf("safe running gate = %d, %v", mode, err)
	}
	pod.Status.InitContainerStatuses[1].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}
	mode, err = checkpointRecoveryGateState(pod)
	if err != nil || mode != checkpointRecoveryGateVerify {
		t.Fatalf("completed gate = %d, %v", mode, err)
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: mainContainerName, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
	pod.Status.Phase = corev1.PodRunning
	if _, err := checkpointRecoveryGateState(pod); err != nil {
		t.Fatalf("completed gate permits marker verification despite started readers: %v", err)
	}
	pod.Status.InitContainerStatuses[1].State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
	if _, err := checkpointRecoveryGateState(pod); err == nil {
		t.Fatal("running gate accepted started regular container")
	}
}

func TestCheckpointMaterializerPinsOnePodAndUsesVerificationOnlyOnCompletedGate(t *testing.T) {
	for name, completed := range map[string]bool{"initial restore": false, "crash reentry": true} {
		t.Run(name, func(t *testing.T) {
			clientset := fake.NewSimpleClientset()
			config := Config{Namespace: "test", PodStartupTimeout: time.Second, ArtifactDaemonHostPath: "/artifacts", ArtifactHelperImage: "helper"}
			backend := NewDaemonSetBackend(config, nil, nil)
			backend.daemonClient = &DaemonClient{scheme: "https"}
			restore := &checkpointRestoreFake{}
			backend.restoreClient = restore
			c := checkpointRestoreTestContainer(t, clientset, config, backend, true)
			pod := checkpointRestoreTestPod(c, completed)
			if _, err := clientset.CoreV1().Pods(config.Namespace).Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			restore.after = func() {
				complete := func() {
					current, _ := clientset.CoreV1().Pods(config.Namespace).Get(context.Background(), c.podName, metav1.GetOptions{})
					current.Status.Phase = corev1.PodRunning
					current.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: checkpointRestoreGateInitName, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}, {Name: "prepare-hermetic-workspace", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}}
					_, _ = clientset.CoreV1().Pods(config.Namespace).Update(context.Background(), current, metav1.UpdateOptions{})
				}
				if completed {
					complete()
				} else {
					go func() { time.Sleep(100 * time.Millisecond); complete() }()
				}
			}
			if err := c.MaterializeBeforeLaunch(context.Background(), runtime.ProcessSpec{Dir: "/tmp/build"}); err != nil {
				t.Fatal(err)
			}
			if restore.restoreCalls != 0+boolToInt(!completed) || restore.verifyCalls != boolToInt(completed) || restore.node != "node-a" {
				t.Fatalf("restore=%d verify=%d node=%q", restore.restoreCalls, restore.verifyCalls, restore.node)
			}
		})
	}
}

func TestCheckpointMaterializerCreatesOneFreshUnscheduledPodThenWaitsForGate(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	clientset.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, apiruntime.Object, error) {
		action.(k8stesting.CreateAction).GetObject().(*corev1.Pod).UID = types.UID("fresh-pod-uid")
		return false, nil, nil
	})
	config := Config{Namespace: "test", PodStartupTimeout: time.Second, PodSchedulingTimeout: time.Second, ArtifactDaemonHostPath: "/artifacts", ArtifactHelperImage: "helper"}
	backend := NewDaemonSetBackend(config, nil, nil)
	backend.daemonClient = &DaemonClient{scheme: "https"}
	restore := &checkpointRestoreFake{}
	backend.restoreClient = restore
	c := checkpointRestoreTestContainer(t, clientset, config, backend, false)
	restore.after = func() { go checkpointRestoreCompletePod(clientset, c, 100*time.Millisecond) }
	go func() {
		time.Sleep(50 * time.Millisecond)
		pod, _ := clientset.CoreV1().Pods(config.Namespace).Get(context.Background(), c.podName, metav1.GetOptions{})
		pod.Spec.NodeName = "node-a"
		pod.Status.Phase = corev1.PodPending
		pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: checkpointRestoreGateInitName, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
		_, _ = clientset.CoreV1().Pods(config.Namespace).Update(context.Background(), pod, metav1.UpdateOptions{})
	}()
	if err := c.MaterializeBeforeLaunch(context.Background(), runtime.ProcessSpec{Dir: "/tmp/build"}); err != nil {
		t.Fatal(err)
	}
	creates, deletes := 0, 0
	for _, action := range clientset.Actions() {
		if action.GetResource().Resource == "pods" && action.GetVerb() == "create" {
			creates++
		}
		if action.GetResource().Resource == "pods" && action.GetVerb() == "delete" {
			deletes++
		}
	}
	if creates != 1 || deletes != 0 || restore.restoreCalls != 1 || restore.node != "node-a" {
		t.Fatalf("creates=%d deletes=%d restore=%d node=%q", creates, deletes, restore.restoreCalls, restore.node)
	}
}

func checkpointRestoreCompletePod(clientset *fake.Clientset, c *Container, delay time.Duration) {
	time.Sleep(delay)
	pod, _ := clientset.CoreV1().Pods(c.config.Namespace).Get(context.Background(), c.podName, metav1.GetOptions{})
	pod.Status.Phase = corev1.PodRunning
	pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: checkpointRestoreGateInitName, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}, {Name: "prepare-hermetic-workspace", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}}
	_, _ = clientset.CoreV1().Pods(c.config.Namespace).Update(context.Background(), pod, metav1.UpdateOptions{})
}

func TestCheckpointRecoveryExecFailureIsNotRetryable(t *testing.T) {
	cause := errors.New("container not found")
	ordinary := checkpointRecoveryExecError(nil, cause)
	var retryable runtime.RetryableError
	if !errors.As(ordinary, &retryable) {
		t.Fatal("ordinary transient exec error lost retryability")
	}
	recovery := &Container{containerSpec: runtime.ContainerSpec{CheckpointRestore: &runtime.CheckpointRestoreDescriptor{}}}
	if got := checkpointRecoveryExecError(recovery, cause); got != cause || errors.As(got, &retryable) {
		t.Fatalf("recovery exec error = %v, retryable=%t", got, errors.As(got, &retryable))
	}
}

type checkpointRestoreFake struct {
	mu                        sync.Mutex
	restoreCalls, verifyCalls int
	node                      string
	after                     func()
}

func (f *checkpointRestoreFake) RestoreCheckpoint(_ context.Context, node string, request checkpoint.RestoreRequest) (checkpoint.RestoreResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restoreCalls++
	f.node = node
	if f.after != nil {
		f.after()
	}
	return checkpointRestoreResult(request), nil
}
func (f *checkpointRestoreFake) VerifyCheckpointRestore(_ context.Context, node string, request checkpoint.RestoreRequest) (checkpoint.RestoreResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verifyCalls++
	f.node = node
	if f.after != nil {
		f.after()
	}
	return checkpointRestoreResult(request), nil
}
func checkpointRestoreResult(request checkpoint.RestoreRequest) checkpoint.RestoreResult {
	return checkpoint.RestoreResult{Object: hangar.Attributes{Ref: request.Archive.Ref, CompressedBytes: 1, UncompressedBytes: 1}, MaterializationID: request.MaterializationID, PodUID: request.PodUID}
}
func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func checkpointRestoreTestContainer(t *testing.T, clientset *fake.Clientset, config Config, backend *DaemonSetBackend, reused bool) *Container {
	t.Helper()
	ref, err := hangar.NewObjectRef(hangar.KindCheckpoint, hangar.Digest("sha256:"+strings.Repeat("b", 64)), 3)
	if err != nil {
		t.Fatal(err)
	}
	return newContainer("agent-42", db.ContainerMetadata{Type: db.ContainerTypeAgent}, runtime.ContainerSpec{Type: db.ContainerTypeAgent, Hermetic: true, CheckpointCapture: true, Dir: "/tmp/build", ImageSpec: runtime.ImageSpec{ImageURL: "busybox"}, CheckpointRestore: &runtime.CheckpointRestoreDescriptor{Archive: checkpoint.Archive{Ref: ref}, MaterializationID: "materialization-2", MaxBytes: 1024, MaxEntries: 16}}, nil, clientset, config, "worker", checkpointRestoreTestExecutor{}, nil, backend, reused)
}

func checkpointRestoreTestPod(c *Container, completed bool) *corev1.Pod {
	gate := corev1.ContainerStatus{Name: checkpointRestoreGateInitName}
	if completed {
		gate.State.Terminated = &corev1.ContainerStateTerminated{ExitCode: 0}
	} else {
		gate.State.Running = &corev1.ContainerStateRunning{}
	}
	statuses := []corev1.ContainerStatus{gate}
	phase := corev1.PodPending
	if completed {
		statuses = append(statuses, corev1.ContainerStatus{Name: "prepare-hermetic-workspace", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}})
		phase = corev1.PodRunning
	}
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: c.podName, Namespace: c.config.Namespace, UID: types.UID("pod-uid"), Annotations: map[string]string{checkpointRestoreAnnotationKey: checkpointRestoreAnnotation(c.handle, *c.containerSpec.CheckpointRestore)}}, Spec: corev1.PodSpec{NodeName: "node-a", InitContainers: []corev1.Container{{Name: checkpointRestoreGateInitName}, {Name: "prepare-hermetic-workspace"}}}, Status: corev1.PodStatus{Phase: phase, InitContainerStatuses: statuses}}
}

type checkpointRestoreTestExecutor struct{ PodExecutor }

func checkpointRestoreContainerByName(containers []corev1.Container, name string) corev1.Container {
	for _, container := range containers {
		if container.Name == name {
			return container
		}
	}
	return corev1.Container{}
}

func checkpointRestoreVolumeByName(volumes []corev1.Volume, name string) *corev1.Volume {
	for index := range volumes {
		if volumes[index].Name == name {
			return &volumes[index]
		}
	}
	return nil
}

func podContainerHasMount(container corev1.Container, name string) bool {
	for _, mount := range container.VolumeMounts {
		if mount.Name == name {
			return true
		}
	}
	return false
}

func TestCheckpointSessionVolumeFailsClosedWithoutDaemonOrOnLogicalCollision(t *testing.T) {
	base := runtime.ContainerSpec{Type: db.ContainerTypeAgent, CheckpointCapture: true, Dir: "/tmp/build"}
	withoutDaemon := newContainer("agent-42", db.ContainerMetadata{Type: db.ContainerTypeAgent}, base, nil, nil, Config{}, "worker", nil, nil, nil, false)
	if _, _, err := withoutDaemon.checkpointSessionVolume(); err == nil || !strings.Contains(err.Error(), "DaemonSet") {
		t.Fatalf("non-daemon checkpoint capture error = %v", err)
	}
	colliding := base
	colliding.Outputs = runtime.OutputPaths{"session": "/tmp/build/output"}
	withDaemon := newContainer("agent-42", db.ContainerMetadata{Type: db.ContainerTypeAgent}, colliding, nil, nil, Config{ArtifactDaemonHostPath: "/var/lib/concourse/artifacts"}, "worker", nil, nil, NewDaemonSetBackend(Config{ArtifactDaemonHostPath: "/var/lib/concourse/artifacts"}, nil, nil), false)
	if _, _, err := withDaemon.checkpointSessionVolume(); err == nil {
		t.Fatal("accepted session host-root collision")
	}
}
