package steps

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// ContainerLifecycleDefinitions migrates container_test.go's reuse, property
// and re-attach blocks — what happens to a step's container across a web
// restart, a repeated check, and a pod that has already finished.

// LeftoverPod is the state where a pod from a previous run is already on the
// cluster before the step starts.
type LeftoverPod struct {
	Namespace string
	Worker    *jetbridge.Worker
	Clientset *fake.Clientset
	Ctx       context.Context
	Handle    string
	Metadata  db.ContainerMetadata
	PodName   string
}

// ReusedPod is the state after the step has run against that cluster.
type ReusedPod struct {
	Namespace string
	Clientset *fake.Clientset
	Ctx       context.Context
	PodName   string
	Pod       *corev1.Pod
	Err       error
}

// ContainerProperties is the state for the property store.
type ContainerProperties struct {
	Container runtime.Container
	Props     map[string]string
	Err       error
}

func ContainerLifecycleDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// PE-01. A check container's pause pod finishes when its sleep
		// expires. The next check must get a fresh pod — exec-ing into a dead
		// one fails in a way that looks like the resource is broken.
		brine.DefineMapUsing[brine.Empty, LeftoverPod](
			"a check step whose previous pod is {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, res brine.Resources) (LeftoverPod, error) {
				phase, ok := p.GetString(0)
				if !ok {
					return LeftoverPod{}, fmt.Errorf("expected a phase parameter")
				}
				database, ok := res.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return LeftoverPod{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
				}
				dbWorker, err := database.PersistNamedWorker("k8s-worker-1")
				if err != nil {
					return LeftoverPod{}, err
				}

				ctx := context.Background()
				namespace := "test-namespace"
				clientset := fake.NewSimpleClientset()
				worker := jetbridge.NewWorker(dbWorker, clientset, jetbridge.NewConfig(namespace, ""))
				worker.SetExecutor(execStub{})

				handle := "aaaa1111-bbbb-cccc-dddd-eeee2222ffff"
				metadata := db.ContainerMetadata{Type: db.ContainerTypeCheck, StepName: "my-time"}
				podName := jetbridge.GeneratePodName(metadata, handle)

				leftover := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: podName, Namespace: namespace,
						Labels: map[string]string{"concourse.ci/worker": "k8s-worker-1"},
					},
					Status: corev1.PodStatus{Phase: corev1.PodPhase(phase)},
				}
				if _, err := clientset.CoreV1().Pods(namespace).Create(ctx, leftover, metav1.CreateOptions{}); err != nil {
					return LeftoverPod{}, fmt.Errorf("create leftover pod: %w", err)
				}

				return LeftoverPod{
					Namespace: namespace, Worker: worker, Clientset: clientset,
					Ctx: ctx, Handle: handle, Metadata: metadata, PodName: podName,
				}, nil
			},
		),

		brine.DefineMap[LeftoverPod, ReusedPod](
			"the check runs again",
			func(in LeftoverPod, _ brine.Params, _ *brine.Recorder) (ReusedPod, error) {
				container, _, err := in.Worker.FindOrCreateContainer(
					in.Ctx,
					db.NewFixedHandleContainerOwner(in.Handle),
					in.Metadata,
					runtime.ContainerSpec{
						TeamID:    1,
						Dir:       "/tmp/build/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "docker:///concourse/time-resource"},
					},
					&noopDelegate{},
				)
				if err != nil {
					return ReusedPod{}, fmt.Errorf("find or create container: %w", err)
				}

				out := ReusedPod{
					Namespace: in.Namespace, Clientset: in.Clientset,
					Ctx: in.Ctx, PodName: in.PodName,
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

		brine.DefineCheck[ReusedPod](
			"the step gets a live pod, not the dead one",
			func(in ReusedPod, _ brine.Params, _ *brine.Recorder) error {
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
				return nil
			},
		),

		// A container's properties are how the runtime remembers a step's
		// result in-process, which is what Attach reads before it asks
		// Kubernetes anything.
		brine.DefineMapUsing[brine.Empty, ContainerProperties](
			"a container that has recorded {string} as {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, res brine.Resources) (ContainerProperties, error) {
				key, _ := p.GetString(0)
				value, ok := p.GetString(1)
				if !ok {
					return ContainerProperties{}, fmt.Errorf("expected a key and a value")
				}
				database, ok := res.Get("jetbridge-db").(JetbridgeDB)
				if !ok {
					return ContainerProperties{}, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
				}
				dbWorker, err := database.PersistNamedWorker("k8s-worker-1")
				if err != nil {
					return ContainerProperties{}, err
				}
				ctx := context.Background()
				clientset := fake.NewSimpleClientset()
				worker := jetbridge.NewWorker(dbWorker, clientset, jetbridge.NewConfig("test-namespace", ""))

				container, _, err := worker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner("props-handle"),
					db.ContainerMetadata{},
					runtime.ContainerSpec{ImageSpec: runtime.ImageSpec{ImageURL: "docker:///alpine"}},
					&noopDelegate{},
				)
				if err != nil {
					return ContainerProperties{}, fmt.Errorf("find or create container: %w", err)
				}
				if err := container.SetProperty(key, value); err != nil {
					return ContainerProperties{}, fmt.Errorf("set property: %w", err)
				}
				props, err := container.Properties()
				return ContainerProperties{Container: container, Props: props, Err: err}, nil
			},
		),

		brine.DefineCheck[ContainerProperties](
			"reading it back yields {string} as {string}",
			func(in ContainerProperties, p brine.Params, _ *brine.Recorder) error {
				key, _ := p.GetString(0)
				want, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected a key and a value")
				}
				if in.Err != nil {
					return fmt.Errorf("reading properties failed: %v", in.Err)
				}
				got, found := in.Props[key]
				if !found {
					return fmt.Errorf("expected property %q, the container has %d properties", key, len(in.Props))
				}
				if got != want {
					return fmt.Errorf("expected %q to be %q, got %q", key, want, got)
				}
				return nil
			},
		),
	}
}

// RecoveredStep is what a re-attaching web sees when it picks a step back up.
type RecoveredStep struct {
	ExitStatus int
	Err        error
	Message    string
}

// AttachDefinitions covers PE-11/PE-12 — how a restarted web recovers a step's
// result instead of running it a second time.
//
// This is the single most consequential recovery path in the runtime: get it
// wrong and a web restart silently re-executes a completed step, in a
// workspace that already has its outputs.
func AttachDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// PE-12 first branch: the in-process property store still remembers.
		brine.DefineMapUsing[brine.Empty, RecoveredStep](
			"a step the runtime still remembers finishing with exit code {int}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, res brine.Resources) (RecoveredStep, error) {
				code, ok := p.GetInt(0)
				if !ok {
					return RecoveredStep{}, fmt.Errorf("expected an exit code parameter")
				}
				container, ctx, err := attachableContainer(res, "attach-handle", nil)
				if err != nil {
					return RecoveredStep{}, err
				}
				if err := container.SetProperty("concourse:exit-status", fmt.Sprintf("%d", code)); err != nil {
					return RecoveredStep{}, fmt.Errorf("record exit status: %w", err)
				}
				return attachAndWait(ctx, container)
			},
		),

		// PE-12 second branch: the web restarted, so the property store is
		// empty and the pod annotation is the only surviving record.
		brine.DefineMapUsing[brine.Empty, RecoveredStep](
			"a web restart, and a pod annotated as having finished with exit code {int}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, res brine.Resources) (RecoveredStep, error) {
				code, ok := p.GetInt(0)
				if !ok {
					return RecoveredStep{}, fmt.Errorf("expected an exit code parameter")
				}
				handle := "attach-annotated"
				container, ctx, err := attachableContainer(res, handle, func(clientset *fake.Clientset) error {
					pod := &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name: handle, Namespace: "test-namespace",
							Annotations: map[string]string{
								"concourse.ci/exit-status": fmt.Sprintf("%d", code),
							},
						},
						Status: corev1.PodStatus{Phase: corev1.PodRunning},
					}
					_, err := clientset.CoreV1().Pods("test-namespace").
						Create(context.Background(), pod, metav1.CreateOptions{})
					return err
				})
				if err != nil {
					return RecoveredStep{}, err
				}
				return attachAndWait(ctx, container)
			},
		),

		// PE-12 last branch: nothing recorded the result, so re-attaching must
		// FAIL. Reporting success here would mark an unfinished step complete.
		brine.DefineMapUsing[brine.Empty, RecoveredStep](
			"a web restart, and a pod with no record of having finished",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (RecoveredStep, error) {
				handle := "attach-unannotated"
				container, ctx, err := attachableContainer(res, handle, func(clientset *fake.Clientset) error {
					pod := &corev1.Pod{
						ObjectMeta: metav1.ObjectMeta{Name: handle, Namespace: "test-namespace"},
						Status:     corev1.PodStatus{Phase: corev1.PodRunning},
					}
					_, err := clientset.CoreV1().Pods("test-namespace").
						Create(context.Background(), pod, metav1.CreateOptions{})
					return err
				})
				if err != nil {
					return RecoveredStep{}, err
				}
				return attachAndWait(ctx, container)
			},
		),

		brine.DefineCheck[RecoveredStep](
			"the step is recovered as having exited {int}",
			func(in RecoveredStep, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetInt(0)
				if !ok {
					return fmt.Errorf("expected an exit code parameter")
				}
				if in.Err != nil {
					return fmt.Errorf("expected the step to be recovered as exit %d, got error: %v", want, in.Err)
				}
				if in.ExitStatus != want {
					return fmt.Errorf("expected exit %d, got %d", want, in.ExitStatus)
				}
				return nil
			},
		),

		brine.DefineCheck[RecoveredStep](
			"the step cannot be recovered and must be run again",
			func(in RecoveredStep, _ brine.Params, _ *brine.Recorder) error {
				if in.Err == nil {
					return fmt.Errorf(
						"expected re-attaching to fail so the engine re-runs the step; "+
							"it reported success with exit %d, which would mark an unfinished step complete",
						in.ExitStatus)
				}
				return nil
			},
		),
	}
}

func attachableContainer(res brine.Resources, handle string, seed func(*fake.Clientset) error) (runtime.Container, context.Context, error) {
	database, ok := res.Get("jetbridge-db").(JetbridgeDB)
	if !ok {
		return nil, nil, fmt.Errorf("jetbridge-db resource is %T", res.Get("jetbridge-db"))
	}
	dbWorker, err := database.PersistNamedWorker("k8s-worker-1")
	if err != nil {
		return nil, nil, err
	}
	ctx := context.Background()
	clientset := fake.NewSimpleClientset()
	if seed != nil {
		if err := seed(clientset); err != nil {
			return nil, nil, fmt.Errorf("seed cluster: %w", err)
		}
	}
	worker := jetbridge.NewWorker(dbWorker, clientset, jetbridge.NewConfig("test-namespace", ""))
	worker.SetExecutor(execStub{})

	container, _, err := worker.FindOrCreateContainer(
		ctx,
		db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{},
		runtime.ContainerSpec{ImageSpec: runtime.ImageSpec{ImageURL: "docker:///alpine"}},
		&noopDelegate{},
	)
	if err != nil {
		return nil, nil, fmt.Errorf("find or create container: %w", err)
	}
	return container, ctx, nil
}

func attachAndWait(ctx context.Context, container runtime.Container) (RecoveredStep, error) {
	process, err := container.Attach(ctx, "some-process-id", runtime.ProcessIO{})
	if err != nil {
		return RecoveredStep{Err: err, Message: err.Error()}, nil
	}
	result, waitErr := process.Wait(ctx)
	msg := ""
	if waitErr != nil {
		msg = waitErr.Error()
	}
	return RecoveredStep{ExitStatus: result.ExitStatus, Err: waitErr, Message: msg}, nil
}

// SidecarLogs is the state after a step with sidecars has run and its output
// has been collected.
type SidecarLogs struct {
	Stdout        string
	SidecarWriter string
	Err           error
}

// SidecarLogDefinitions covers SC-07 — where a sidecar's output ends up.
//
// The ginkgo tests assert that `GetLogs` was REQUESTED for the sidecar by
// name, which is a call, not an effect. What a user experiences is whether the
// database container's log appears in their build output at all, and whether
// it is distinguishable from the step's own. So these scenarios assert the
// bytes arrive, and where.
func SidecarLogDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[ContainerDraft, SidecarLogs](
			"the step runs with a dedicated log stream for sidecar {string}",
			func(in ContainerDraft, p brine.Params, _ *brine.Recorder) (SidecarLogs, error) {
				name, ok := p.GetString(0)
				if !ok {
					return SidecarLogs{}, fmt.Errorf("expected a sidecar name")
				}
				sidecarBuf := new(bytes.Buffer)
				return runWithSidecarIO(in, runtime.ProcessIO{
					Stdout:         new(bytes.Buffer),
					SidecarWriters: map[string]io.Writer{name: sidecarBuf},
				}, sidecarBuf)
			},
		),

		brine.DefineMap[ContainerDraft, SidecarLogs](
			"the step runs with nowhere separate to put sidecar output",
			func(in ContainerDraft, _ brine.Params, _ *brine.Recorder) (SidecarLogs, error) {
				return runWithSidecarIO(in, runtime.ProcessIO{Stdout: new(bytes.Buffer)}, nil)
			},
		),

		brine.DefineCheck[SidecarLogs](
			"the sidecar's output arrives on its own stream",
			func(in SidecarLogs, _ brine.Params, _ *brine.Recorder) error {
				if in.SidecarWriter == "" {
					return fmt.Errorf(
						"expected the sidecar's log on its dedicated stream; nothing arrived, so a user watching " +
							"that sidecar would see an empty pane")
				}
				return nil
			},
		),

		brine.DefineCheck[SidecarLogs](
			"the sidecar's output is folded into the build log, labelled {string}",
			func(in SidecarLogs, p brine.Params, _ *brine.Recorder) error {
				label, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a label parameter")
				}
				if !strings.Contains(in.Stdout, label) {
					return fmt.Errorf(
						"expected the sidecar's output labelled %q in the build log so it is distinguishable "+
							"from the step's own; the log was %q", label, truncate(in.Stdout, 300))
				}
				return nil
			},
		),
	}
}

func runWithSidecarIO(in ContainerDraft, io0 runtime.ProcessIO, sidecarBuf *bytes.Buffer) (SidecarLogs, error) {
	container, _, err := in.Worker.FindOrCreateContainer(
		in.Ctx,
		db.NewFixedHandleContainerOwner(in.Handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{
			TeamID:    1,
			Dir:       in.Dir,
			ImageSpec: runtime.ImageSpec{ImageURL: in.ImageURL},
			Sidecars:  in.Sidecars,
		},
		&noopDelegate{},
	)
	if err != nil {
		return SidecarLogs{}, fmt.Errorf("find or create container: %w", err)
	}

	process, err := container.Run(in.Ctx, runtime.ProcessSpec{Path: "/bin/sh"}, io0)
	if err != nil {
		return SidecarLogs{}, fmt.Errorf("run container: %w", err)
	}

	pods := in.Clientset.CoreV1().Pods(in.Namespace)
	pod, err := pods.Get(in.Ctx, in.Handle, metav1.GetOptions{})
	if err != nil {
		return SidecarLogs{}, fmt.Errorf("get pod: %w", err)
	}
	pod.Status.Phase = corev1.PodSucceeded
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  "main",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
	}}
	if _, err := pods.UpdateStatus(in.Ctx, pod, metav1.UpdateOptions{}); err != nil {
		return SidecarLogs{}, fmt.Errorf("update pod: %w", err)
	}

	_, waitErr := process.Wait(in.Ctx)

	out := SidecarLogs{Err: waitErr}
	if b, ok := io0.Stdout.(*bytes.Buffer); ok {
		out.Stdout = b.String()
	}
	if sidecarBuf != nil {
		out.SidecarWriter = sidecarBuf.String()
	}
	return out, nil
}
