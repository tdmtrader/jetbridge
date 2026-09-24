package steps

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type liveStartup struct {
	worker        WorkerReady
	observer      kubernetes.Interface
	before, after *corev1.Pod
	stdout        *bytes.Buffer
	initName      string
	initLogs      string
	runtimeTrace  *execObservation
}

// One physical startup supplies the shared premise for six trace contracts.
// The gate is a real init process, not an API-status edit or a watch reply.
func LiveObservabilityDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		tracedStartupDefinition("a real init fails repeatedly before recovering", prepareLiveRetryInit),
		Transform[ExecStepRunning, SpansRecorded]("the failed real init recovers after repeated observations", func(in ExecStepRunning, _ Args) (SpansRecorded, error) { return recoverLiveRetryInit(in) }),
		tracedStartupDefinition("a traced real pod is waiting in its init container", func(d JetbridgeDB, c SpanCapture, r *brine.Recorder) (ExecStepRunning, error) {
			return prepareLiveStartup(d, c, r, false)
		}),
		tracedStartupDefinition("a traced real pod is waiting to unpack a missing input", func(d JetbridgeDB, c SpanCapture, r *brine.Recorder) (ExecStepRunning, error) {
			return prepareLiveStartup(d, c, r, true)
		}),
		tracedStartupDefinition("a traced real pod is held by a scheduling gate", prepareLiveImagePull),
		Transform[ExecStepRunning, SpansRecorded]("the real init container is released after the runtime starts watching", func(in ExecStepRunning, _ Args) (SpansRecorded, error) { return in.completeLiveStartup() }),
		Transform[ExecStepRunning, SpansRecorded]("the real input init fails before the runtime waits for startup", func(in ExecStepRunning, _ Args) (SpansRecorded, error) { return in.failLiveInputInit() }),
		Transform[ExecStepRunning, SpansRecorded]("the pod is released to pull its image while the runtime watches", func(in ExecStepRunning, _ Args) (SpansRecorded, error) { return in.completeLiveImagePull() }),
		CheckThat[SpansRecorded]("both startup observations preserve their lifecycle and actual node", checkLiveStartupNode),
	}
}

func tracedStartupDefinition(pattern string, prepare func(JetbridgeDB, SpanCapture, *brine.Recorder) (ExecStepRunning, error)) brine.StepDefinition {
	return brine.DefineMapUsing[brine.Empty, ExecStepRunning](pattern, []string{"jetbridge-db", "span-capture"},
		func(_ brine.Empty, _ brine.Params, rec *brine.Recorder, res brine.Resources) (ExecStepRunning, error) {
			database, ok := res.Get("jetbridge-db").(JetbridgeDB)
			if !ok {
				return ExecStepRunning{}, fmt.Errorf("missing real database")
			}
			capture, ok := res.Get("span-capture").(SpanCapture)
			if !ok {
				return ExecStepRunning{}, fmt.Errorf("missing real OTLP capture")
			}
			capture, err := capture.ready()
			if err != nil {
				return ExecStepRunning{}, err
			}
			return prepare(database, capture, rec)
		})
}

func prepareLiveStartup(database JetbridgeDB, capture SpanCapture, rec *brine.Recorder, missingInput bool) (ExecStepRunning, error) {
	w, err := newLiveRuntimeWorker(database, rec)
	if err != nil {
		return ExecStepRunning{}, err
	}
	const handle = "observed-startup"
	initial, initName, err := prepareLiveInitPod(w, handle, missingInput)
	if err != nil {
		return ExecStepRunning{}, err
	}
	return newLiveStartupProcess(w, capture, rec, initial, handle, initName)
}

// Shared physical init gate for trace observations and startup deadlines.
// No Kubernetes state is reported by the fixture; the real kubelet owns it.
func prepareLiveInitPod(w WorkerReady, handle string, missingInput bool) (*corev1.Pod, string, error) {
	metadata := db.ContainerMetadata{Type: db.ContainerTypeTask}
	name := jetbridge.GeneratePodName(metadata, handle)
	grace := int64(1)
	initName := "startup-init"
	initCommand := "printf ready > /tmp/gate-ready; while [ ! -f /tmp/release ]; do sleep 0.1; done; "
	var volumes []corev1.Volume
	var mounts []corev1.VolumeMount
	if missingInput {
		initName = "fetch-input-0"
		// This is an actual unpack failure in a pre-existing init container,
		// not the daemon's input-fetch implementation or a fabricated exit.
		initCommand += "exec tar -xf /inputs/missing.tar -C /workdir"
		for _, mount := range []corev1.VolumeMount{{Name: "input-archive", MountPath: "/inputs"}, {Name: "unpacked-input", MountPath: "/workdir"}} {
			mounts = append(mounts, mount)
			volumes = append(volumes, corev1.Volume{Name: mount.Name, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}})
		}
	} else {
		initCommand += "printf 'init released\\n'"
	}
	// Pre-existing pods are supported by the real runtime. Here their spec
	// provides a controllable init process and sidecar; kubelet owns all status.
	_, err := w.Clientset.CoreV1().Pods(w.Namespace).Create(w.Ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, TerminationGracePeriodSeconds: &grace,
			Volumes:        volumes,
			InitContainers: []corev1.Container{{Name: initName, Image: "busybox:1.37.0", Command: []string{"sh", "-ec", initCommand}, VolumeMounts: mounts}},
			Containers: []corev1.Container{
				{Name: "main", Image: "busybox:1.37.0", Command: []string{"sleep", "600"}},
				{Name: "observer-sidecar", Image: "busybox:1.37.0", Command: []string{"sleep", "600"}},
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, "", err
	}
	var initial *corev1.Pod
	for initial == nil {
		pod, err := w.Clientset.CoreV1().Pods(w.Namespace).Get(w.Ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, "", err
		}
		if pod.Status.Phase == corev1.PodPending && pod.Spec.NodeName != "" && pod.UID != "" && len(pod.Status.InitContainerStatuses) == 1 && pod.Status.InitContainerStatuses[0].State.Running != nil {
			for _, condition := range pod.Status.Conditions {
				if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionTrue {
					initial = pod
				}
			}
		}
		if initial == nil {
			select {
			case <-w.Ctx.Done():
				return nil, "", w.Ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	if err := w.Executor.ExecInPod(w.Ctx, w.Namespace, name, initName, []string{"test", "-f", "/tmp/gate-ready"}, nil, nil, nil, false, jetbridge.ExecAttrs{Purpose: "observe-real-init-gate"}); err != nil {
		return nil, "", err
	}
	fmt.Printf("actual startup pod %s/%s UID %s RV %s scheduled on %s; init process is running and main is Pending\n", w.Namespace, name, initial.UID, initial.ResourceVersion, initial.Spec.NodeName)
	return initial, initName, nil
}

// Only the runtime client signals its watch handshake. Independent observers
// retain the original client and cannot release the gate prematurely.
func newLiveStartupProcess(w WorkerReady, capture SpanCapture, rec *brine.Recorder, initial *corev1.Pod, handle, initName string) (ExecStepRunning, error) {
	metadata := db.ContainerMetadata{Type: db.ContainerTypeTask}
	name := initial.Name
	observer := w.Clientset
	ready := make(chan struct{})
	config, err := liveKubernetesConfig()
	if err != nil {
		return ExecStepRunning{}, err
	}
	trace := new(execObservation)
	config = trace.config(rest.CopyConfig(config))
	config.Wrap(func(next http.RoundTripper) http.RoundTripper {
		return &watchHandshakeTransport{next: next, path: "/api/v1/namespaces/" + w.Namespace + "/pods", selector: "metadata.name=" + name, ready: ready}
	})
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return ExecStepRunning{}, err
	}
	w.Config.PodStartupTimeout, w.Config.PodSchedulingTimeout = 45*time.Second, 45*time.Second
	w.Clientset = client
	w = w.rebuildWith(jetbridge.NewSPDYExecutor(client, config))
	container, _, err := w.Worker.FindOrCreateContainer(w.Ctx, db.NewFixedHandleContainerOwner(handle), metadata,
		runtime.ContainerSpec{Type: db.ContainerTypeTask, ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox:1.37.0"}}, nil)
	if err != nil {
		return ExecStepRunning{}, err
	}
	stdout := new(bytes.Buffer)
	process, err := container.Run(w.Ctx, runtime.ProcessSpec{Path: "sh", Args: []string{"-ec", "printf startup-observed"}}, runtime.ProcessIO{Stdout: stdout, Stderr: new(bytes.Buffer)})
	if err != nil {
		return ExecStepRunning{}, err
	}
	return ExecStepRunning{Namespace: w.Namespace, Clientset: client, Ctx: w.Ctx, Handle: name, Process: process, Capture: capture, watchReady: ready, recorder: rec,
		live: &liveStartup{worker: w, observer: observer, before: initial, stdout: stdout, initName: initName, runtimeTrace: trace}}, nil
}

func runLiveStartup(in ExecStepRunning, release func(context.Context) error) (out SpansRecorded, err error) {
	if in.live == nil {
		return out, fmt.Errorf("startup requires an actual gated pod")
	}
	ctx, cancel := context.WithCancel(in.Ctx)
	defer cancel()
	type completion struct {
		out SpansRecorded
		err error
	}
	done := make(chan completion, 1)
	joined := false
	in.Ctx = ctx
	go func() { out, err := waitAndCapture(in, 45*time.Second); done <- completion{out, err} }()
	defer func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				panic("live observed startup did not drain")
			}
		}
	}()
	select {
	case <-in.watchReady:
	case result := <-done:
		joined = true
		return out, fmt.Errorf("startup ended before watch handshake: %v %v", result.out.WaitErr, result.err)
	case <-ctx.Done():
		return out, ctx.Err()
	}
	// The handshake follows the runtime's initial Get of real Pending state.
	// Its eventual successful Wait must consume the subsequent Running state,
	// giving the deduplication assertion two genuine scheduled observations.
	if err := release(ctx); err != nil {
		return out, err
	}
	select {
	case result := <-done:
		joined = true
		out, err = result.out, result.err
	case <-ctx.Done():
		return out, ctx.Err()
	}
	return out, err
}

func (in ExecStepRunning) completeLiveStartup() (out SpansRecorded, err error) {
	l := in.live
	if l == nil {
		return out, fmt.Errorf("startup requires an actual gated pod")
	}
	metric.Metrics.K8sPodStartupDuration.Max()
	out, err = runLiveStartup(in, func(ctx context.Context) error {
		return l.worker.Executor.ExecInPod(ctx, in.Namespace, in.Handle, l.initName, []string{"touch", "/tmp/release"}, nil, nil, nil, false, jetbridge.ExecAttrs{Purpose: "release-real-init-gate"})
	})
	if err != nil {
		return out, err
	}
	if out.WaitErr != nil {
		return out, fmt.Errorf("real startup failed before trace checks: %w", out.WaitErr)
	}
	pod, err := in.Clientset.CoreV1().Pods(in.Namespace).Get(in.Ctx, in.Handle, metav1.GetOptions{})
	if err != nil {
		return out, err
	}
	l.after = pod
	out.live = l
	if pod.UID != l.before.UID || pod.ResourceVersion == l.before.ResourceVersion || pod.Status.Phase != corev1.PodRunning || pod.Spec.NodeName != l.before.Spec.NodeName {
		return out, fmt.Errorf("real startup did not preserve pod identity through Pending -> Running")
	}
	if len(pod.Status.InitContainerStatuses) != 1 || pod.Status.InitContainerStatuses[0].State.Terminated == nil || pod.Status.InitContainerStatuses[0].State.Terminated.ExitCode != 0 {
		return out, fmt.Errorf("actual init process did not exit zero")
	}
	sidecar := false
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == "observer-sidecar" && cs.State.Running != nil {
			sidecar = true
		}
	}
	if !sidecar {
		return out, fmt.Errorf("actual sidecar never reached Running")
	}
	if out.WaitErr == nil && l.stdout.String() != "startup-observed" {
		return out, fmt.Errorf("real task stdout missing: %q", l.stdout.String())
	}
	fmt.Printf("actual startup pod UID %s changed RV %s -> %s; init exited 0, sidecar running, task stdout %q\n", pod.UID, l.before.ResourceVersion, pod.ResourceVersion, l.stdout.String())
	// Observe the same pod again with all lifecycle facts already present on
	// the first Get. Wait still uses production exec; the supervisor replays
	// the completed task, so this does not need another pod or fake status.
	snapshot, err := waitAndCapture(in, 45*time.Second)
	if err != nil || snapshot.WaitErr != nil || snapshot.ExitStatus != 0 {
		return out, fmt.Errorf("already-running observation failed: wait=%v fixture=%v exit=%d", snapshot.WaitErr, err, snapshot.ExitStatus)
	}
	if l.stdout.String() != "startup-observedstartup-observed" {
		return out, fmt.Errorf("already-running observation did not replay actual task output: %q", l.stdout.String())
	}
	fmt.Printf("actual already-running startup observation reused pod UID %s on node %s and replayed task stdout\n", pod.UID, pod.Spec.NodeName)
	return out, nil
}

func checkLiveStartupNode(in SpansRecorded) error {
	if in.live == nil || in.live.after == nil {
		return fmt.Errorf("no real pod node observation")
	}
	spans, err := in.Capture.spans()
	if err != nil {
		return err
	}
	observations := 0
	for _, span := range spans {
		if span.Name != "k8s.exec-process.wait-for-running" {
			continue
		}
		observations++
		counts := map[string]int{}
		nodeFound := false
		for _, event := range span.Events {
			counts[event.Name]++
			if event.Name != "pod.scheduled" {
				continue
			}
			for _, attribute := range event.Attributes {
				if attribute.Key != "node.name" {
					continue
				}
				if attribute.Value.StringValue != in.live.after.Spec.NodeName {
					return fmt.Errorf("scheduling event node %q differs from real pod node %q", attribute.Value.StringValue, in.live.after.Spec.NodeName)
				}
				nodeFound = true
			}
		}
		if !nodeFound {
			return fmt.Errorf("startup observation %d has no scheduling node", observations)
		}
		for _, event := range []string{"pod.scheduled", "pod.initialized", "init.container.completed", "sidecar.started", "pod.phase.running"} {
			if counts[event] != 1 {
				return fmt.Errorf("startup observation %d requires %s once, got %d", observations, event, counts[event])
			}
		}
	}
	if observations != 2 {
		return fmt.Errorf("expected watched startup and already-running snapshot, got %d wait spans", observations)
	}
	fmt.Printf("both actual startup observations record lifecycle facts and node %s\n", in.live.after.Spec.NodeName)
	return nil
}

func (in ExecStepRunning) failLiveInputInit() (SpansRecorded, error) {
	l := in.live
	if l == nil || l.initName != "fetch-input-0" {
		return SpansRecorded{}, fmt.Errorf("missing actual input-unpack init")
	}
	if err := l.worker.Executor.ExecInPod(in.Ctx, in.Namespace, in.Handle, l.initName, []string{"touch", "/tmp/release"}, nil, nil, nil, false, jetbridge.ExecAttrs{Purpose: "release-input-unpack"}); err != nil {
		return SpansRecorded{}, err
	}
	// Observe the actual failure before invoking Wait. The assertion must catch
	// a runtime that replaces this pod, not lose its premise to that mutation.
	for l.after == nil {
		pod, err := in.Clientset.CoreV1().Pods(in.Namespace).Get(in.Ctx, in.Handle, metav1.GetOptions{})
		if err != nil {
			return SpansRecorded{}, err
		}
		if pod.Status.Phase == corev1.PodFailed && len(pod.Status.InitContainerStatuses) == 1 {
			cs := pod.Status.InitContainerStatuses[0]
			if cs.Name == l.initName && cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0 {
				if pod.UID != l.before.UID || pod.ResourceVersion == l.before.ResourceVersion {
					return SpansRecorded{}, fmt.Errorf("failed init lost its original pod identity")
				}
				l.after = pod
			}
		}
		if l.after == nil {
			select {
			case <-in.Ctx.Done():
				return SpansRecorded{}, in.Ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	logs, err := in.Clientset.CoreV1().Pods(in.Namespace).GetLogs(in.Handle, &corev1.PodLogOptions{Container: l.initName}).Do(in.Ctx).Raw()
	if err != nil {
		return SpansRecorded{}, err
	}
	l.initLogs = string(logs)
	if !strings.Contains(l.initLogs, "/inputs/missing.tar") || !strings.Contains(l.initLogs, "No such file") {
		return SpansRecorded{}, fmt.Errorf("actual init did not report the missing archive: %q", l.initLogs)
	}
	fmt.Printf("actual input init %s in pod UID %s changed RV %s -> %s and exited %d; kubelet phase Failed; logs %q\n",
		l.initName, l.after.UID, l.before.ResourceVersion, l.after.ResourceVersion,
		l.after.Status.InitContainerStatuses[0].State.Terminated.ExitCode, l.initLogs)
	out, err := waitAndCapture(in, 45*time.Second)
	out.live = l
	out.Message = errorMessage(out.WaitErr)
	return out, err
}

// Naming the failed init is the original RF-14 assertion. For the live case,
// also preserve its actual exit/log evidence and the pod that produced it.
func checkLiveInitDiagnostics(in SpansRecorded) error {
	l := in.live
	if l == nil || l.after == nil || l.initLogs == "" {
		return fmt.Errorf("no actual failed-input evidence")
	}
	if err := l.runtimeTrace.requireRuntimeAttempts(l.worker.Namespace, l.after.Name, 0, 0); err != nil {
		return err
	}
	terminated := l.after.Status.InitContainerStatuses[0].State.Terminated
	for _, detail := range []string{"phase: Failed", fmt.Sprintf("exit=%d reason=%s", terminated.ExitCode, terminated.Reason), fmt.Sprintf("logs=%q", l.initLogs)} {
		if !strings.Contains(in.Message, detail) {
			return fmt.Errorf("init failure lost actual diagnostic %q: %s", detail, in.Message)
		}
	}
	pod, err := l.worker.Clientset.CoreV1().Pods(l.worker.Namespace).Get(l.worker.Ctx, l.after.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed init pod is no longer available: %w", err)
	}
	if pod.UID != l.after.UID || pod.Status.Phase != corev1.PodFailed {
		return fmt.Errorf("runtime replaced or changed the failed init pod: UID %s phase %s", pod.UID, pod.Status.Phase)
	}
	if l.stdout.Len() != 0 {
		return fmt.Errorf("task ran despite missing inputs: %q", l.stdout.String())
	}
	fmt.Printf("runtime retained failed init pod UID %s and reported its actual name, exit and logs without executing the task\n", pod.UID)
	return nil
}
