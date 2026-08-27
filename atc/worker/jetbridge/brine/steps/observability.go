package steps

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/tracing"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// This file migrates the observability family (OE-*) from
// behavioral_runtime_spec_test.go. It is the hardest family in Group A and the
// one that shows what migration actually costs:
//
//   - It needs a PodExecutor double. The ginkgo suite's fakeExecExecutor lives
//     in volume_test.go (package jetbridge_test) and cannot be imported, so it
//     is re-implemented below. This is the "two fixtures to keep in step" cost
//     the proposal names — measured here at ~40 lines.
//   - It mutates PROCESS-GLOBAL tracing state via tracing.ConfigureTraceProvider.
//     Under ginkgo that was an AfterEach; here it is a scenario-scoped resource
//     whose Disposer restores it, which the machinery runs even under SIGTERM.
//   - One case requires the pod to transition WHILE Wait() blocks, so the step
//     schedules the transition on a goroutine exactly as the ginkgo test did.

// execStub stands in for the cluster's exec surface. It records NOTHING and is
// never asserted on: it consumes stdin so io.Pipe writers unblock and reports
// success, so that the scenario's assertions can be about spans the runtime
// emitted rather than about arguments it passed. A double that accumulates
// call history invites exactly the rule-3 violation this migration is meant to
// avoid, so it does not have any.
type execStub struct{}

func (execStub) ExecInPod(
	_ context.Context,
	_, _, _ string,
	_ []string,
	stdin io.Reader,
	_, _ io.Writer,
	_ bool,
	_ jetbridge.ExecAttrs,
) error {
	if stdin != nil {
		_, _ = io.Copy(io.Discard, stdin)
	}
	return nil
}

// SpanCapture is the scenario-scoped tracing resource: a recorder plus the
// restore of the global tracing flag.
type SpanCapture struct {
	Recorder *tracetest.SpanRecorder
}

// EventNames returns the event names recorded on the named span, and whether
// that span was seen at all.
func (s SpanCapture) EventNames(spanName string) ([]string, bool) {
	for _, span := range s.Recorder.Ended() {
		if span.Name() == spanName {
			names := make([]string, 0, len(span.Events()))
			for _, e := range span.Events() {
				names = append(names, e.Name)
			}
			return names, true
		}
	}
	return nil, false
}

// TracingResourceDefinition is scenario-scoped because the trace provider is
// process-global: two scenarios sharing one recorder would see each other's
// spans.
func TracingResourceDefinition() brine.ResourceDefinition {
	return brine.ResourceDefinition{
		Name:  "span-capture",
		Scope: brine.ScopeScenario,
		Factory: func(map[string]any) (any, error) {
			recorder := new(tracetest.SpanRecorder)
			tp := sdktrace.NewTracerProvider(
				sdktrace.WithSpanProcessor(recorder),
				sdktrace.WithSyncer(tracetest.NewInMemoryExporter()),
			)
			tracing.ConfigureTraceProvider(tp)
			return SpanCapture{Recorder: recorder}, nil
		},
		Disposer: func(any) error {
			// The ginkgo suite's AfterEach. Here the machinery owns it.
			tracing.Configured = false
			return nil
		},
	}
}

// ObservabilityDefinitions carries the OE family.
func ObservabilityDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// Empty -> ExecClusterReady. Needs both the database and the span
		// capture, so it declares both resources.
		brine.DefineMapUsing[brine.Empty, ExecClusterReady](
			"a jetbridge worker whose spans are recorded",
			[]string{"jetbridge-db", "span-capture"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (ExecClusterReady, error) {
				capture, ok := res.Get("span-capture").(SpanCapture)
				if !ok {
					return ExecClusterReady{}, fmt.Errorf("span-capture resource is %T", res.Get("span-capture"))
				}

				cluster, err := NewCluster(res, WithExecutor(execStub{}))
				if err != nil {
					return ExecClusterReady{}, err
				}
				namespace, clientset, worker := cluster.Namespace, cluster.Clientset, cluster.Worker

				return ExecClusterReady{
					Namespace: namespace,
					Worker:    worker,
					Clientset: clientset,
					Ctx:       context.Background(),
					Capture:   capture,
				}, nil
			},
		),

		// ExecClusterReady -> ExecStepRunning.
		brine.DefineMap[ExecClusterReady, ExecStepRunning](
			"an exec-mode task container {string} is running",
			func(in ExecClusterReady, p brine.Params, _ *brine.Recorder) (ExecStepRunning, error) {
				handle, ok := p.GetString(0)
				if !ok {
					return ExecStepRunning{}, fmt.Errorf("expected a container handle parameter")
				}

				container, _, err := in.Worker.FindOrCreateContainer(
					in.Ctx,
					db.NewFixedHandleContainerOwner(handle),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:    1,
						Dir:       "/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "busybox"},
						Type:      db.ContainerTypeTask,
					},
					&noopDelegate{},
				)
				if err != nil {
					return ExecStepRunning{}, fmt.Errorf("find or create container %q: %w", handle, err)
				}

				process, err := container.Run(in.Ctx,
					runtime.ProcessSpec{Path: "/bin/sh"},
					runtime.ProcessIO{
						Stdin:  bytes.NewBufferString(`{}`),
						Stdout: new(bytes.Buffer),
						Stderr: new(bytes.Buffer),
					},
				)
				if err != nil {
					return ExecStepRunning{}, fmt.Errorf("run container %q: %w", handle, err)
				}

				return ExecStepRunning{
					Namespace: in.Namespace,
					Clientset: in.Clientset,
					Ctx:       in.Ctx,
					Handle:    handle,
					Process:   process,
					Capture:   in.Capture,
				}, nil
			},
		),

		// The pod reaches Running with the Initialized condition already true,
		// before Wait observes it. OE-02.
		brine.DefineMap[ExecStepRunning, SpansRecorded](
			"the pod reports itself initialized and then running",
			func(in ExecStepRunning, _ brine.Params, _ *brine.Recorder) (SpansRecorded, error) {
				pods := in.Clientset.CoreV1().Pods(in.Namespace)
				pod, err := pods.Get(in.Ctx, in.Handle, metav1.GetOptions{})
				if err != nil {
					return SpansRecorded{}, fmt.Errorf("get pod %q: %w", in.Handle, err)
				}

				pod.Status.Phase = corev1.PodPending
				pod.Status.Conditions = []corev1.PodCondition{
					{Type: corev1.PodInitialized, Status: corev1.ConditionTrue},
				}
				if _, err := pods.UpdateStatus(in.Ctx, pod, metav1.UpdateOptions{}); err != nil {
					return SpansRecorded{}, fmt.Errorf("update pod status: %w", err)
				}

				pod.Status.Phase = corev1.PodRunning
				pod.Status.Conditions = append(pod.Status.Conditions, corev1.PodCondition{
					Type: corev1.PodReady, Status: corev1.ConditionTrue,
				})
				if _, err := pods.UpdateStatus(in.Ctx, pod, metav1.UpdateOptions{}); err != nil {
					return SpansRecorded{}, fmt.Errorf("update pod status: %w", err)
				}

				return waitAndCapture(in)
			},
		),

		// The container is pre-staged in ContainerCreating so the watcher's
		// initial sync sees it, then transitions out WHILE Wait blocks. OE-04.
		brine.DefineMap[ExecStepRunning, SpansRecorded](
			"the pod pulls its image and then starts while the step waits",
			func(in ExecStepRunning, _ brine.Params, _ *brine.Recorder) (SpansRecorded, error) {
				pods := in.Clientset.CoreV1().Pods(in.Namespace)
				pod, err := pods.Get(in.Ctx, in.Handle, metav1.GetOptions{})
				if err != nil {
					return SpansRecorded{}, fmt.Errorf("get pod %q: %w", in.Handle, err)
				}

				pod.Status.Phase = corev1.PodPending
				pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
					Name:  "main",
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}},
				}}
				if _, err := pods.UpdateStatus(in.Ctx, pod, metav1.UpdateOptions{}); err != nil {
					return SpansRecorded{}, fmt.Errorf("update pod status: %w", err)
				}

				// The transition must arrive as a watch event while Wait is
				// blocked, which is why this is a goroutine and not a second
				// step: the pod has to move mid-wait, and a step boundary
				// would serialize it.
				go func() {
					time.Sleep(20 * time.Millisecond)
					pod.Status.Phase = corev1.PodRunning
					pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
						Name:  "main",
						State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
					}}
					_, _ = pods.UpdateStatus(in.Ctx, pod, metav1.UpdateOptions{})
				}()

				return waitAndCapture(in)
			},
		),

		brine.DefineCheck[SpansRecorded](
			"the {string} span records the event {string}",
			func(in SpansRecorded, p brine.Params, _ *brine.Recorder) error {
				spanName, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a span name parameter")
				}
				eventName, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected an event name parameter")
				}
				if in.WaitErr != nil {
					return fmt.Errorf("the step failed before spans could be asserted: %w", in.WaitErr)
				}

				names, found := in.Capture.EventNames(spanName)
				if !found {
					return fmt.Errorf("expected a %q span, none was recorded", spanName)
				}
				for _, n := range names {
					if n == eventName {
						return nil
					}
				}
				return fmt.Errorf("expected the %q span to record %q, got [%s]",
					spanName, eventName, strings.Join(names, ", "))
			},
		),

		brine.DefineCheck[SpansRecorded](
			"the step exits {int}",
			func(in SpansRecorded, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetInt(0)
				if !ok {
					return fmt.Errorf("expected an exit status parameter")
				}
				if in.WaitErr != nil {
					return fmt.Errorf("expected exit %d, the step errored: %w", want, in.WaitErr)
				}
				if in.ExitStatus != want {
					return fmt.Errorf("expected exit %d, got %d", want, in.ExitStatus)
				}
				return nil
			},
		),
	}
}

func waitAndCapture(in ExecStepRunning) (SpansRecorded, error) {
	result, waitErr := in.Process.Wait(in.Ctx)
	return SpansRecorded{
		Capture:    in.Capture,
		ExitStatus: result.ExitStatus,
		WaitErr:    waitErr,
	}, nil
}

// ObservabilityExtraDefinitions covers the rest of the OE family: what the
// span says about scheduling, init containers, sidecars and phase changes.
//
// The consumer is whoever opens a trace because a step took eight minutes.
// The events are the timeline that tells them WHERE the time went — waiting
// for a node, pulling an image, running an init container — so each scenario
// asserts a moment a reader would look for.
func ObservabilityExtraDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// OE-01
		brine.DefineMap[ExecStepRunning, SpansRecorded](
			"the pod is placed on node {string} and then starts",
			func(in ExecStepRunning, p brine.Params, _ *brine.Recorder) (SpansRecorded, error) {
				node, ok := p.GetString(0)
				if !ok {
					return SpansRecorded{}, fmt.Errorf("expected a node parameter")
				}
				return in.stageThenRun(func(pod *corev1.Pod) {
					pod.Spec.NodeName = node
					pod.Status.Phase = corev1.PodPending
					pod.Status.Conditions = []corev1.PodCondition{
						{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
					}
				})
			},
		),

		// OE-05 / OE-06: an init container is where artifact staging happens,
		// so its outcome is the difference between "the step failed" and "the
		// step never got its inputs".
		brine.DefineMap[ExecStepRunning, SpansRecorded](
			"an init container finishes with exit code {int} and the pod then starts",
			func(in ExecStepRunning, p brine.Params, _ *brine.Recorder) (SpansRecorded, error) {
				code, ok := p.GetInt(0)
				if !ok {
					return SpansRecorded{}, fmt.Errorf("expected an exit code parameter")
				}
				return in.stageThenRun(func(pod *corev1.Pod) {
					pod.Status.Phase = corev1.PodPending
					pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{
						Name: "artifact-init",
						State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
							ExitCode: int32(code), Reason: "Completed",
						}},
					}}
				})
			},
		),

		// OE-07
		brine.DefineMap[ExecStepRunning, SpansRecorded](
			"a sidecar {string} reaches running and the pod then starts",
			func(in ExecStepRunning, p brine.Params, _ *brine.Recorder) (SpansRecorded, error) {
				name, ok := p.GetString(0)
				if !ok {
					return SpansRecorded{}, fmt.Errorf("expected a sidecar name parameter")
				}
				return in.stageThenRun(func(pod *corev1.Pod) {
					pod.Status.Phase = corev1.PodPending
					pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
						Name:  name,
						State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
					}}
				})
			},
		),

		// OE-09: the same condition observed twice must not produce two
		// events, or a trace of a slow step becomes unreadable noise.
		brine.DefineMap[ExecStepRunning, SpansRecorded](
			"the pod is placed on node {string}, observed twice, and then starts",
			func(in ExecStepRunning, p brine.Params, _ *brine.Recorder) (SpansRecorded, error) {
				node, ok := p.GetString(0)
				if !ok {
					return SpansRecorded{}, fmt.Errorf("expected a node parameter")
				}
				pods := in.Clientset.CoreV1().Pods(in.Namespace)
				pod, err := pods.Get(in.Ctx, in.Handle, metav1.GetOptions{})
				if err != nil {
					return SpansRecorded{}, fmt.Errorf("get pod: %w", err)
				}
				pod.Spec.NodeName = node
				pod.Status.Phase = corev1.PodPending
				pod.Status.Conditions = []corev1.PodCondition{
					{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
				}
				if _, err := pods.UpdateStatus(in.Ctx, pod, metav1.UpdateOptions{}); err != nil {
					return SpansRecorded{}, fmt.Errorf("update pod: %w", err)
				}
				// Observed a second time in the same condition.
				if _, err := pods.UpdateStatus(in.Ctx, pod, metav1.UpdateOptions{}); err != nil {
					return SpansRecorded{}, fmt.Errorf("re-update pod: %w", err)
				}
				go func(p *corev1.Pod) {
					time.Sleep(20 * time.Millisecond)
					p.Status.Phase = corev1.PodRunning
					p.Status.ContainerStatuses = []corev1.ContainerStatus{{
						Name: "main", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
					}}
					_, _ = pods.UpdateStatus(in.Ctx, p, metav1.UpdateOptions{})
				}(pod.DeepCopy())
				return waitAndCapture(in)
			},
		),

		brine.DefineCheck[SpansRecorded](
			"the {string} span records the event {string} exactly once",
			func(in SpansRecorded, p brine.Params, _ *brine.Recorder) error {
				spanName, _ := p.GetString(0)
				eventName, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected a span and an event name")
				}
				names, found := in.Capture.EventNames(spanName)
				if !found {
					return fmt.Errorf("expected a %q span, none was recorded", spanName)
				}
				n := 0
				for _, e := range names {
					if e == eventName {
						n++
					}
				}
				if n != 1 {
					return fmt.Errorf("expected %q exactly once on the %q span, found it %d times (all: %s)",
						eventName, spanName, n, strings.Join(names, ", "))
				}
				return nil
			},
		),

		// OE-01's node.name is the attribute that makes the event actionable —
		// "waited for scheduling" is not useful without "onto which node".
		brine.DefineCheck[SpansRecorded](
			"the {string} event names the node {string}",
			func(in SpansRecorded, p brine.Params, _ *brine.Recorder) error {
				eventName, _ := p.GetString(0)
				want, ok := p.GetString(1)
				if !ok {
					return fmt.Errorf("expected an event and a node name")
				}
				for _, span := range in.Capture.Recorder.Ended() {
					for _, e := range span.Events() {
						if e.Name != eventName {
							continue
						}
						for _, a := range e.Attributes {
							if string(a.Key) == "node.name" {
								if a.Value.AsString() == want {
									return nil
								}
								return fmt.Errorf("expected %q to name node %q, it named %q",
									eventName, want, a.Value.AsString())
							}
						}
						return fmt.Errorf("the %q event carries no node.name attribute", eventName)
					}
				}
				return fmt.Errorf("no %q event was recorded", eventName)
			},
		),
	}
}

// stageThenRun applies a pre-Wait pod state, then transitions the pod to
// Running on a goroutine so Wait can complete. The staged state has to be
// visible to the watcher's initial sync, which is why it is not a second step.
func (in ExecStepRunning) stageThenRun(stage func(*corev1.Pod)) (SpansRecorded, error) {
	pods := in.Clientset.CoreV1().Pods(in.Namespace)
	pod, err := pods.Get(in.Ctx, in.Handle, metav1.GetOptions{})
	if err != nil {
		return SpansRecorded{}, fmt.Errorf("get pod %q: %w", in.Handle, err)
	}
	stage(pod)
	if _, err := pods.UpdateStatus(in.Ctx, pod, metav1.UpdateOptions{}); err != nil {
		return SpansRecorded{}, fmt.Errorf("update pod status: %w", err)
	}
	go func(p *corev1.Pod) {
		time.Sleep(20 * time.Millisecond)
		p.Status.Phase = corev1.PodRunning
		p.Status.ContainerStatuses = append(p.Status.ContainerStatuses, corev1.ContainerStatus{
			Name: "main", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		})
		_, _ = pods.UpdateStatus(in.Ctx, p, metav1.UpdateOptions{})
	}(pod.DeepCopy())
	return waitAndCapture(in)
}

// ObservabilityMetricDefinitions covers OE-08 and OE-10 — the phase timeline
// and the operator-facing metrics.
//
// Metrics are the only signal an operator has BEFORE anyone opens a build.
// A cluster that has started failing to pull images, or whose pods have got
// slow to start, shows up here first or not at all.
func ObservabilityMetricDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// OE-08 / OE-10: the ordinary successful startup, which is what makes
		// the phase timeline and the startup-duration gauge meaningful.
		brine.DefineMap[ExecStepRunning, SpansRecorded](
			"the pod is pending and then starts normally",
			func(in ExecStepRunning, _ brine.Params, _ *brine.Recorder) (SpansRecorded, error) {
				metric.Metrics.K8sPodStartupDuration.Max() // reset max-tracking
				return in.stageThenRun(func(pod *corev1.Pod) {
					pod.Status.Phase = corev1.PodPending
				})
			},
		),

		brine.DefineCheck[SpansRecorded](
			"a pod startup duration was recorded",
			func(in SpansRecorded, _ brine.Params, _ *brine.Recorder) error {
				if in.WaitErr != nil {
					return fmt.Errorf("the step failed before startup could be timed: %v", in.WaitErr)
				}
				if got := metric.Metrics.K8sPodStartupDuration.Max(); got <= 0 {
					return fmt.Errorf("expected a positive startup duration, got %v — "+
						"an operator watching this gauge would see nothing", got)
				}
				return nil
			},
		),
	}
}

// InitContainerDefinitions covers RF-14 — what a step is told when the init
// container that stages its inputs fails.
//
// This is a distinct failure from "the step failed": the step never ran at
// all. Without the init container's name, state and logs in the error, the
// user sees a red build with no output and nothing to act on.
func InitContainerDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMap[ExecStepRunning, SpansRecorded](
			"the init container {string} fails before the step can start",
			func(in ExecStepRunning, p brine.Params, _ *brine.Recorder) (SpansRecorded, error) {
				name, ok := p.GetString(0)
				if !ok {
					return SpansRecorded{}, fmt.Errorf("expected an init container name")
				}
				pods := in.Clientset.CoreV1().Pods(in.Namespace)
				pod, err := pods.Get(in.Ctx, in.Handle, metav1.GetOptions{})
				if err != nil {
					return SpansRecorded{}, fmt.Errorf("get pod: %w", err)
				}
				// PodSucceeded rather than PodFailed keeps waitForRunning out
				// of the pause-pod recreate branch, so the init diagnostics
				// are reported directly — the same choice the original makes.
				pod.Status.Phase = corev1.PodSucceeded
				pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{
					Name: name, Image: "alpine:latest",
					State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
						ExitCode: 1, Reason: "Error",
					}},
				}}
				if _, err := pods.UpdateStatus(in.Ctx, pod, metav1.UpdateOptions{}); err != nil {
					return SpansRecorded{}, fmt.Errorf("update pod: %w", err)
				}
				result, waitErr := in.Process.Wait(in.Ctx)
				msg := ""
				if waitErr != nil {
					msg = waitErr.Error()
				}
				return SpansRecorded{
					Capture: in.Capture, ExitStatus: result.ExitStatus,
					WaitErr: waitErr, Message: msg,
				}, nil
			},
		),

		brine.DefineCheck[SpansRecorded](
			"the step is told which init container failed, naming {string}",
			func(in SpansRecorded, p brine.Params, _ *brine.Recorder) error {
				name, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected an init container name")
				}
				if in.WaitErr == nil {
					return fmt.Errorf("expected the step to fail because its init container did; it succeeded")
				}
				if !strings.Contains(in.Message, name) {
					return fmt.Errorf(
						"expected the failure to name the init container %q so the user knows the step never ran; got %q",
						name, in.Message)
				}
				return nil
			},
		),
	}
}
