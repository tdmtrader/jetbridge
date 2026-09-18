package steps

import (
	"context"
	"fmt"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// ContainerLifecycleDefinitions migrates container_test.go's reuse, property
// and re-attach blocks — what happens to a step's container across a web
// restart, a repeated check, and a pod that has already finished.

// LeftoverPod is the state where a pod from a previous run is already on the
// cluster before the step starts.
type LeftoverPod struct {
	TeamID      int
	PreviousUID types.UID
	Namespace   string
	Worker      *jetbridge.Worker
	Clientset   kubernetes.Interface
	Ctx         context.Context
	Handle      string
	Metadata    db.ContainerMetadata
	PodName     string
}

// ReusedPod is the state after the step has run against that cluster.
type ReusedPod struct {
	PreviousUID types.UID
	Namespace   string
	Clientset   kubernetes.Interface
	Ctx         context.Context
	PodName     string
	Pod         *corev1.Pod
	Err         error
}

// ContainerProperties is the state for the property store.
type ContainerProperties struct {
	Container runtime.Container
	Props     map[string]string
	Err       error
}

func ContainerLifecycleDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[LiveTaskPlan, LeftoverPod]("the worker has a previous check pod in phase {string}",
			func(in LiveTaskPlan, p brine.Params, rec *brine.Recorder) (LeftoverPod, error) {
				phase, ok := p.GetString(0)
				if !ok {
					return LeftoverPod{}, fmt.Errorf("expected previous pod phase")
				}
				return prepareLivePreviousCheck(in, rec, phase)
			}),

		brine.DefineMap[LeftoverPod, ReusedPod](
			"the check runs again",
			func(in LeftoverPod, _ brine.Params, _ *brine.Recorder) (ReusedPod, error) {
				container, _, err := in.Worker.FindOrCreateContainer(
					in.Ctx,
					db.NewFixedHandleContainerOwner(in.Handle),
					in.Metadata,
					runtime.ContainerSpec{
						TeamID:    in.TeamID,
						Dir:       "/tmp/build/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///concourse/time-resource"},
					},
					nil,
				)
				if err != nil {
					return ReusedPod{}, fmt.Errorf("find or create container: %w", err)
				}

				out := ReusedPod{
					Namespace: in.Namespace, Clientset: in.Clientset,
					Ctx: in.Ctx, PodName: in.PodName, PreviousUID: in.PreviousUID,
				}
				if _, err := container.Run(in.Ctx,
					runtime.ProcessSpec{Path: "/opt/resource/check"},
					runtime.ProcessIO{Stdin: strings.NewReader("{}")},
				); err != nil {
					out.Err = err
					return out, nil
				}

				pod, err := in.Clientset.CoreV1().Pods(in.Namespace).Get(in.Ctx, in.PodName, metav1.GetOptions{})
				if err != nil {
					return ReusedPod{}, fmt.Errorf("get pod after run: %w", err)
				}
				out.Pod = pod
				return out, nil
			},
		),

		CheckThat[ReusedPod]("the check gets a new unfinished pod",
			func(in ReusedPod) error {
				if in.Err != nil {
					return fmt.Errorf("the step failed instead of replacing the pod: %v", in.Err)
				}
				if in.Pod == nil {
					return fmt.Errorf("no pod named %q exists after the run", in.PodName)
				}
				switch in.Pod.Status.Phase {
				case corev1.PodSucceeded, corev1.PodFailed:
					return fmt.Errorf("expected a live pod, %q is still %s — the dead pod was reused",
						in.PodName, in.Pod.Status.Phase)
				}
				if in.PreviousUID == "" || in.Pod.UID == "" || in.Pod.ResourceVersion == "" {
					return fmt.Errorf("pod identities must come from the API")
				}
				if in.Pod.UID == in.PreviousUID {
					return fmt.Errorf("the previous pod %q was reused rather than replaced", in.Pod.UID)
				}
				return nil
			}),

		// A container's properties are how the runtime remembers a step's
		// result in-process, which is what Attach reads before it asks
		// Kubernetes anything.
		Transform[WorkerReady, ContainerProperties](
			"the container records {string} as {string}",
			func(in WorkerReady, a Args) (ContainerProperties, error) {
				container, err := recoveryContainer(in, "props-handle")
				if err != nil {
					return ContainerProperties{}, err
				}
				if err := container.SetProperty(a.String(0), a.String(1)); err != nil {
					return ContainerProperties{}, fmt.Errorf("set property: %w", err)
				}
				props, err := container.Properties()
				return ContainerProperties{Container: container, Props: props, Err: err}, nil
			},
		),

		CheckStringFor[ContainerProperties]("reading it back yields {string} as {string}",
			"the recorded property",
			func(in ContainerProperties, key string) (string, error) {
				if in.Err != nil {
					return "", fmt.Errorf("reading properties failed: %v", in.Err)
				}
				got, found := in.Props[key]
				if !found {
					return "", fmt.Errorf("expected property %q, the container has %d properties", key, len(in.Props))
				}
				return got, nil
			}),
	}
}

// RecoveredStep is what a re-attaching web sees when it picks a step back up.
type RecoveredStep struct {
	AttachRefused bool
	ExitStatus    int
	Err           error
	Message       string
}

// AttachDefinitions covers PE-11/PE-12 — how a restarted web recovers a step's
// result instead of running it a second time.
//
// This is the single most consequential recovery path in the runtime: get it
// wrong and a web restart silently re-executes a completed step, in a
// workspace that already has its outputs.
func AttachDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		Transform[WorkerReady, RecoveredStep](
			"the pending pod has no recorded completion",
			func(in WorkerReady, _ Args) (RecoveredStep, error) {
				return recoverUnrecordedPod(in, "attach-unannotated")
			},
		),

		CheckThat[RecoveredStep]("the step cannot be recovered and must be run again",
			func(in RecoveredStep) error {
				if in.Err == nil {
					return fmt.Errorf(
						"expected re-attaching to fail so the engine re-runs the step; "+
							"it reported success with exit %d, which would mark an unfinished step complete",
						in.ExitStatus)
				}
				if !in.AttachRefused {
					return fmt.Errorf("Attach succeeded; a later Wait error is not a recovery refusal: %v", in.Err)
				}
				if !strings.Contains(in.Message, "no completion status") {
					return fmt.Errorf("expected Attach to refuse missing completion status, got %q", in.Message)
				}
				return nil
			}),
	}
}

// recoveryContainer uses the real API/database worker and its production transport.
// Recovery must not execute commands; the cases return a recorded result or refuse.
func recoveryContainer(in WorkerReady, handle string) (runtime.Container, error) {
	if in.ProducerExecutor == nil {
		return nil, fmt.Errorf("recovery worker has no production exec transport")
	}
	in.Executor = in.ProducerExecutor
	in = in.rebuild()
	container, _, err := in.Worker.FindOrCreateContainer(in.Ctx, db.NewFixedHandleContainerOwner(handle), db.ContainerMetadata{}, runtime.ContainerSpec{TeamID: in.TeamID, ImageSpec: runtime.ImageSpec{ImageURL: "docker:///alpine"}}, nil)
	if err != nil {
		return nil, fmt.Errorf("find or create recovery container: %w", err)
	}
	return container, nil
}

// recoverUnrecordedPod observes the API-default Pending state without writing
// status. Literal unreported-phase policy is covered by TestExecRecoveryPolicy.
func recoverUnrecordedPod(in WorkerReady, handle string) (RecoveredStep, error) {
	pods := in.Clientset.CoreV1().Pods(in.Namespace)
	pod, err := pods.Create(in.Ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: handle, Namespace: in.Namespace, Labels: map[string]string{"concourse.ci/worker": in.Worker.Name()}},
		Spec:       corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "main", Image: "alpine", Command: []string{"sh", "-c", "sleep 86400"}}}},
	}, metav1.CreateOptions{})
	if err != nil {
		return RecoveredStep{}, fmt.Errorf("create recovery pod: %w", err)
	}
	if pod.UID == "" || pod.ResourceVersion == "" {
		return RecoveredStep{}, fmt.Errorf("recovery pod has no API identity")
	}

	observed, err := pods.Get(in.Ctx, handle, metav1.GetOptions{})
	if err != nil {
		return RecoveredStep{}, fmt.Errorf("read reported recovery pod: %w", err)
	}
	if observed.UID != pod.UID || observed.Status.Phase != corev1.PodPending {
		return RecoveredStep{}, fmt.Errorf("recovery input was not persisted: UID %q, phase %q", observed.UID, observed.Status.Phase)
	}
	if observed.ResourceVersion != pod.ResourceVersion {
		return RecoveredStep{}, fmt.Errorf("Pending recovery pod changed after creation")
	}
	fmt.Printf("actual API-default recovery: pod %s/%s UID %s unchanged RV %s phase Pending; no status write\n", in.Namespace, handle, observed.UID, observed.ResourceVersion)
	container, err := recoveryContainer(in, handle)
	if err != nil {
		return RecoveredStep{}, err
	}
	return attachAndWait(in.Ctx, container)
}
func attachAndWait(ctx context.Context, container runtime.Container) (RecoveredStep, error) {
	process, err := container.Attach(ctx, "some-process-id", runtime.ProcessIO{})
	if err != nil {
		return RecoveredStep{AttachRefused: true, Err: err, Message: err.Error()}, nil
	}
	result, waitErr := process.Wait(ctx)
	msg := errorMessage(waitErr)
	return RecoveredStep{ExitStatus: result.ExitStatus, Err: waitErr, Message: msg}, nil
}

// The kubelet supplies the previous check pod's terminal state and logs.
// The following Run/assertion still tests replacement, not resource execution.
func prepareLivePreviousCheck(in LiveTaskPlan, rec *brine.Recorder, phase string) (LeftoverPod, error) {
	code := 0
	switch corev1.PodPhase(phase) {
	case corev1.PodSucceeded:
	case corev1.PodFailed:
		code = 1
	default:
		return LeftoverPod{}, fmt.Errorf("expected a terminal previous pod, got %q", phase)
	}
	w, err := newLiveRuntimeWorker(in.Database, rec)
	if err != nil {
		return LeftoverPod{}, err
	}
	const handle = "aaaa1111-bbbb-cccc-dddd-eeee2222ffff"
	metadata := db.ContainerMetadata{Type: db.ContainerTypeCheck, StepName: "my-time"}
	name := jetbridge.GeneratePodName(metadata, handle)
	grace := int64(1)
	pods := w.Clientset.CoreV1().Pods(w.Namespace)
	original, err := pods.Create(w.Ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"concourse.ci/worker": w.Worker.Name(), "concourse.ci/type": "check"}},
		Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, TerminationGracePeriodSeconds: &grace, Containers: []corev1.Container{
			{Name: "main", Image: "busybox:1.37.0", Command: []string{"sh", "-ec", fmt.Sprintf("printf 'previous-check-finished\\n'; exit %d", code)}},
		}},
	}, metav1.CreateOptions{})
	if err != nil {
		return LeftoverPod{}, err
	}
	pod, log, err := awaitLivePodExit(w.Ctx, w.Clientset, original, code, "previous-check-finished\n")
	if err != nil {
		return LeftoverPod{}, err
	}
	c := pod.Status.ContainerStatuses[0]
	fmt.Printf("actual previous check: pod %s/%s UID %s RV %s node %s phase %s container %s exit=%d reason=%s log=%q\n", w.Namespace, name, pod.UID, pod.ResourceVersion, pod.Spec.NodeName, pod.Status.Phase, c.ContainerID, code, c.State.Terminated.Reason, log)
	return LeftoverPod{Namespace: w.Namespace, Worker: w.Worker, Clientset: w.Clientset, Ctx: w.Ctx, TeamID: w.TeamID, Handle: handle, Metadata: metadata, PodName: name, PreviousUID: pod.UID}, nil
}
