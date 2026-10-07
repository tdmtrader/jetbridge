package jetbridge

// A capture-selected pause Pod is given back once its outcome is the node's.
//
// The seal on the output daemon waits until every container of the producing
// Pod has terminated, or the Pod is gone. In exec mode the pause container
// sleeps for a day; the reaper keeps the Pods of a running build; and the build
// does not finish until its capture does. So nothing ended the pause container,
// the seal ran out its deadline, the capture failed and the Run never finished.
//
// What ends it now is Wait, once the node has acknowledged the outcome: a
// GRACEFUL Pod delete. These specs run the exec-mode path for real -- the pause
// Pod the Container itself builds, an executor, a real output daemon that
// admits, holds, records and seals -- and then let the seal complete only on
// the evidence production's seal reads.
//
// Why the termination is bridged rather than read in-process: the daemon's
// Kubernetes-backed answer (outputplane.NewNodePodTerminations) is "the Pod is
// gone from the node, or every container has terminated". Linking outputplane
// here is forbidden (it is an output role package; architecture_test.go), and a
// daemon process cannot read this test's fake API server. So the harness runs
// the daemon in its standalone mode, and podGoneFromNode below asks the fake API
// server the same question nodePodTerminations asks the real one, declaring the
// termination file only when it answers yes. The seal completing is then the
// daemon's own work, gated on nothing but the Pod having gone.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/executioncontrol"
	hangaroutput "github.com/concourse/concourse/hangar/output"
)

const releasePodUID = types.UID("cccccccc-cccc-4ccc-8ccc-cccccccccccc")

// podDeletes records every Pod delete the fake API server is asked for, with
// the options it was asked with.
type podDeletes struct{ options []metav1.DeleteOptions }

func recordPodDeletes(clientset *fake.Clientset) *podDeletes {
	recorded := &podDeletes{}
	clientset.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
		recorded.options = append(recorded.options, action.(k8stesting.DeleteActionImpl).DeleteOptions)

		return false, nil, nil
	})

	return recorded
}

// assertGraceful fails on a delete that skipped the Pod's termination: a zero
// grace period is a force delete, and so is a negative one.
func (recorded *podDeletes) assertGraceful(t *testing.T) {
	t.Helper()
	for _, options := range recorded.options {
		if options.GracePeriodSeconds != nil && *options.GracePeriodSeconds <= 0 {
			t.Errorf("the Pod was deleted with a grace period of %d: a forced delete",
				*options.GracePeriodSeconds)
		}
	}
}

// podGoneFromNode is nodePodTerminations' "gone" half, asked of the fake API
// server: no Pod with this UID is scheduled to node.
func podGoneFromNode(ctx context.Context, clientset *fake.Clientset, node string, uid types.UID) (bool, error) {
	pods, err := clientset.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + node,
	})
	if err != nil {
		return false, err
	}
	for _, pod := range pods.Items {
		if pod.UID == uid && pod.Spec.NodeName == node {
			return false, nil
		}
	}

	return true, nil
}

// exactPausePodFixture is one exec-mode step on a real output daemon: its
// container, the pause Pod that container built (bound and running, as the
// scheduler and the kubelet would leave it), and the control it runs under.
type exactPausePodFixture struct {
	harness   *outputDaemonHarness
	clientset *fake.Clientset
	container *Container
	control   *runtime.ExecutionControl
	identity  executioncontrol.Identity
	executor  *controlExecutor
}

func newExactPausePodFixture(t *testing.T, capture bool) *exactPausePodFixture {
	t.Helper()

	harness, err := startOutputDaemon()
	if err != nil {
		t.Fatalf("starting the output daemon: %v", err)
	}
	t.Cleanup(harness.Stop)

	identity := executioncontrol.Identity{
		ExecutionID: executioncontrol.ExecutionID(fmt.Sprintf("%08x-cccc-4ccc-8ccc-cccccccccccc", os.Getpid())),
		Fence:       1,
	}
	control := &runtime.ExecutionControl{
		Version:         runtime.ExecutionControlVersion,
		Phase:           runtime.ControlPhaseAdmitted,
		Identity:        identity,
		ActivationEpoch: harnessEpoch,
		Endpoint:        harness.Endpoint,
		Capability:      "base-capability",
	}
	if capture {
		if err := control.SelectCapture(runtime.DurableOutputCapture{
			Version:            runtime.DurableOutputCaptureVersion,
			Identity:           identity,
			ActivationEpoch:    harnessEpoch,
			Output:             "result",
			SourceControlGrant: "source-control-grant",
			CaptureDeadline:    time.Now().Add(time.Hour),
			Node:               "node-1",
			NodeUID:            harnessNodeUID,
		}); err != nil {
			t.Fatalf("selecting the capture: %v", err)
		}
	}

	clientset := fake.NewSimpleClientset(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1", UID: types.UID(harnessNodeUID)},
	})
	executor := &controlExecutor{run: func(int) error { return nil }}

	cfg := capturePodConfig(true)
	cfg.OutputActivationEpoch = int64(harnessEpoch)
	container := capturingContainer(t, cfg, false, control)
	container.clientset = clientset
	container.executor = executor
	container.outputControls = harness

	// The pause Pod, built by the Container exactly as Run builds it.
	processSpec := runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", "true"}}
	created, err := container.createPausePod(t.Context(), processSpec)
	if err != nil {
		t.Fatalf("creating the pause pod: %v", err)
	}
	if command := mainContainerCommand(created); len(command) != 3 || command[2] != pauseCommand {
		t.Fatalf("the step's main container is not the pause container: %q", command)
	}

	// What the API server, the scheduler and the kubelet add: a UID, a node,
	// and every container started -- init containers done, the pause
	// container sleeping.
	created.UID = releasePodUID
	created.Spec.NodeName = "node-1"
	created.Status.Phase = corev1.PodRunning
	for _, init := range created.Spec.InitContainers {
		created.Status.InitContainerStatuses = append(created.Status.InitContainerStatuses, corev1.ContainerStatus{
			Name:  init.Name,
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
		})
	}
	for _, regular := range created.Spec.Containers {
		created.Status.ContainerStatuses = append(created.Status.ContainerStatuses, corev1.ContainerStatus{
			Name: regular.Name, Ready: true,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		})
	}
	if _, err := clientset.CoreV1().Pods(cfg.Namespace).Update(t.Context(), created, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("binding and starting the pause pod: %v", err)
	}

	return &exactPausePodFixture{
		harness: harness, clientset: clientset, container: container,
		control: control, identity: identity, executor: executor,
	}
}

func mainContainerCommand(pod *corev1.Pod) []string {
	for _, container := range pod.Spec.Containers {
		if container.Name == mainContainerName {
			return container.Command
		}
	}

	return nil
}

// hold is the control init's half, in the only order a producer has: the
// execution is admitted with no Pod named, and the hold binds the Pod UID the
// init container reads off the Downward API.
func (fixture *exactPausePodFixture) hold(t *testing.T) {
	t.Helper()

	if _, err := fixture.harness.Client.Admit(t.Context(), executioncontrol.Envelope{
		ProtocolVersion: executioncontrol.ProtocolVersion,
		Identity:        fixture.identity,
		ActivationEpoch: harnessEpoch,
		NodeUID:         executioncontrol.NodeUID(harnessNodeUID),
		Capability:      "base-capability",
	}); err != nil {
		t.Fatalf("admitting: %v", err)
	}
	warrant, err := fixture.harness.Client.MintGrant(hangaroutput.CaptureFacet, "hold", fixture.identity)
	if err != nil {
		t.Fatalf("minting the hold grant: %v", err)
	}
	if _, err := postHold(fixture.harness.Endpoint, string(warrant), hangaroutput.CaptureHoldRequest{
		ProtocolVersion: hangaroutput.ProtocolVersion,
		Execution:       fixture.identity,
		Output:          "result",
		PodUID:          executioncontrol.PodUID(releasePodUID),
	}); err != nil {
		t.Fatalf("holding the capture's step directory: %v", err)
	}
}

func (fixture *exactPausePodFixture) wait(t *testing.T) runtime.ProcessResult {
	t.Helper()

	process := newExecProcess("proc-1", fixture.container.podName, fixture.clientset,
		fixture.container.config, fixture.container, fixture.executor,
		runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", "true"}},
		runtime.ProcessIO{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}, nil)
	result, err := process.Wait(t.Context())
	if err != nil {
		t.Fatalf("the step failed: %v", err)
	}

	return result
}

func TestACaptureSelectedExecPodIsDeletedGracefullySoTheSealCompletes(t *testing.T) {
	fixture := newExactPausePodFixture(t, true)
	deletes := recordPodDeletes(fixture.clientset)
	fixture.hold(t)

	// The control: before the step runs, the producing Pod is on the node and
	// its pause container is running. This is the state the seal used to wait
	// on for a day.
	gone, err := podGoneFromNode(t.Context(), fixture.clientset, "node-1", releasePodUID)
	if err != nil {
		t.Fatal(err)
	}
	if gone {
		t.Fatal("the producing Pod is gone before its step ran; the spec proves nothing")
	}

	if result := fixture.wait(t); result.ExitStatus != 0 {
		t.Fatalf("the step exited %d", result.ExitStatus)
	}
	if fixture.executor.count() != 1 {
		t.Fatalf("the command was exec'd %d times", fixture.executor.count())
	}

	// The outcome is the node's ...
	classified, err := fixture.harness.Client.Classify(t.Context(), fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	if !classified.Classification.Authoritative() || classified.Acknowledgement == nil ||
		classified.Acknowledgement.Outcome == nil {
		t.Fatalf("the node holds no acknowledged outcome: %+v", classified)
	}

	// ... and the pause Pod was given back, once, gracefully.
	if len(deletes.options) != 1 {
		t.Fatalf("the pause Pod was deleted %d times, want once", len(deletes.options))
	}
	deletes.assertGraceful(t)
	if grace := deletes.options[0].GracePeriodSeconds; grace != nil {
		t.Errorf("the pause Pod's delete overrode its termination grace period with %d", *grace)
	}
	if _, err := fixture.clientset.CoreV1().Pods("test-ns").Get(t.Context(), "capture-pod",
		metav1.GetOptions{}); err == nil {
		t.Fatal("the pause Pod is still there after its acknowledged outcome")
	}

	// A web that restarts now finds no Pod and no annotation. It recovers the
	// result from the capture node's ledger, and runs nothing again.
	fixture.container.properties = map[string]string{}
	attached, err := fixture.container.Attach(t.Context(), "proc-1",
		runtime.ProcessIO{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	if err != nil {
		t.Fatalf("a restarted web could not attach to the released step: %v", err)
	}
	recovered, err := attached.Wait(t.Context())
	if err != nil || recovered.ExitStatus != 0 {
		t.Fatalf("the released step recovered as %+v, %v; want exit 0 from the ledger", recovered, err)
	}
	if fixture.executor.count() != 1 {
		t.Fatalf("recovering the released step exec'd its command again (%d execs)", fixture.executor.count())
	}

	// The termination reaches the daemon the way nodePodTerminations would
	// carry it: the Pod is gone from the node.
	gone, err = podGoneFromNode(t.Context(), fixture.clientset, "node-1", releasePodUID)
	if err != nil {
		t.Fatal(err)
	}
	if !gone {
		t.Fatal("the producing Pod is still on the node; the seal would wait for its deadline")
	}
	if err := os.WriteFile(filepath.Join(fixture.harness.TerminationsDir, string(releasePodUID)), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	// And the seal completes: the daemon's own background job observed the
	// termination and canonicalized the held step directory.
	seal := hangaroutput.CaptureSealRequest{
		ProtocolVersion: hangaroutput.ProtocolVersion,
		Execution:       fixture.identity,
		Output:          "result",
		PodUID:          executioncontrol.PodUID(releasePodUID),
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		sealed, err := fixture.harness.Client.Seal(t.Context(), seal)
		if err == nil {
			if sealed.Marker.PodUID != executioncontrol.PodUID(releasePodUID) {
				t.Fatalf("the seal names Pod %s, want %s", sealed.Marker.PodUID, releasePodUID)
			}
			if sealed.Marker.State != hangaroutput.StepSealed {
				t.Fatalf("the sealed marker is %s", sealed.Marker.State)
			}

			break
		}
		if !errors.Is(err, hangaroutput.ErrConflict) && !errors.Is(err, hangaroutput.ErrSealInProgress) {
			t.Fatalf("sealing: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("the seal did not complete after the Pod was gone: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// The other half: an exact execution that selected no capture keeps today's
// behaviour. Its pause Pod outlives Wait (fly hijack can still exec into it)
// and the reaper collects it once its build is over.
func TestAnExactExecPodWithNoCaptureOutlivesItsCommand(t *testing.T) {
	fixture := newExactPausePodFixture(t, false)
	deletes := recordPodDeletes(fixture.clientset)

	if result := fixture.wait(t); result.ExitStatus != 0 {
		t.Fatalf("the step exited %d", result.ExitStatus)
	}
	if len(deletes.options) != 0 {
		t.Fatalf("a step with no capture had its pause Pod deleted %d times", len(deletes.options))
	}
	if _, err := fixture.clientset.CoreV1().Pods("test-ns").Get(t.Context(), "capture-pod",
		metav1.GetOptions{}); err != nil {
		t.Fatalf("a step with no capture lost its pause Pod: %v", err)
	}
}

// The reaper is the retry. A capture-selected Pod with an exit status -- which
// only an acknowledged outcome writes -- is deleted gracefully even while its
// build runs, because that build waits on the Pod's termination. Every other
// retained Pod stays, and one already terminating is not asked again.
func TestTheReaperGracefullyReleasesARetainedCapturedPod(t *testing.T) {
	terminating := metav1.Now()
	pods := []*corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "captured", Namespace: "test-ns", Annotations: map[string]string{
			exitStatusAnnotationKey: "0", captureStepAnnotation: "steps/x.capture",
		}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ordinary", Namespace: "test-ns", Annotations: map[string]string{
			exitStatusAnnotationKey: "0",
		}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "already-going", Namespace: "test-ns", DeletionTimestamp: &terminating,
			Finalizers: []string{"test"}, Annotations: map[string]string{
				exitStatusAnnotationKey: "0", captureStepAnnotation: "steps/y.capture",
			}}},
	}
	clientset := fake.NewSimpleClientset()
	var retained []metav1.ObjectMeta
	for _, pod := range pods {
		if _, err := clientset.CoreV1().Pods("test-ns").Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		retained = append(retained, pod.ObjectMeta)
	}
	deletes := recordPodDeletes(clientset)

	reaper := NewReaper(lagertest.NewTestLogger("reaper"), clientset, Config{Namespace: "test-ns"}, nil, nil)
	reaper.releaseCapturedPods(t.Context(), reaper.logger, retained)

	if len(deletes.options) != 1 {
		t.Fatalf("the reaper issued %d deletes, want 1 (the captured Pod only)", len(deletes.options))
	}
	deletes.assertGraceful(t)
	if _, err := clientset.CoreV1().Pods("test-ns").Get(t.Context(), "captured", metav1.GetOptions{}); err == nil {
		t.Error("the retained captured Pod was not released")
	}
	if _, err := clientset.CoreV1().Pods("test-ns").Get(t.Context(), "ordinary", metav1.GetOptions{}); err != nil {
		t.Errorf("an ordinary retained Pod was deleted: %v", err)
	}
}
