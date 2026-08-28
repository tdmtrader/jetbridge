package steps

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ProcessGapDefinitions closes what mutating process.go exposed. Three of the
// four were missed by BOTH suites, and all three are the same failure in
// different clothes: a step reporting the wrong result for a pod that did not
// do what the happy path assumes.
func ProcessGapDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// RF-07, on the EXEC-mode path. Direct mode blocks in watcher.Next
		// and only re-checks the scheduling deadline when another watch event
		// arrives, so a pod nothing can schedule simply stops the loop
		// forever. waitForRunning polls on a timer and does time out — which
		// is the path a real get step takes anyway.
		brine.DefineMapUsing[brine.Empty, StepOutcome](
			"a resource step waits for a pod nothing in the cluster can schedule",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, _ brine.Params, _ *brine.Recorder, res brine.Resources) (StepOutcome, error) {
				// Impatient, so the deadline lands in seconds rather than the
				// five-minute default. Same values process_test.go used.
				cluster, err := NewCluster(res,
					WithExecutor(execStub{}),
					WithConfig(func(cfg *jetbridge.Config) {
						cfg.PodSchedulingTimeout = 3 * time.Second
						cfg.PodStartupTimeout = 2 * time.Second
					}),
				)
				if err != nil {
					return StepOutcome{}, err
				}
				ctx, namespace := cluster.Ctx, cluster.Namespace
				clientset, worker := cluster.Clientset, cluster.Worker
				handle := "unschedulable-handle"

				container, _, err := worker.FindOrCreateContainer(ctx,
					db.NewFixedHandleContainerOwner(handle),
					db.ContainerMetadata{Type: db.ContainerTypeGet},
					runtime.ContainerSpec{
						TeamID:    1,
						Dir:       "/tmp/build/get",
						ImageSpec: runtime.ImageSpec{ImageURL: "busybox"},
						Type:      db.ContainerTypeGet,
					},
					&noopDelegate{},
				)
				if err != nil {
					return StepOutcome{}, fmt.Errorf("find or create container: %w", err)
				}

				stderr := new(bytes.Buffer)
				process, err := container.Run(ctx,
					runtime.ProcessSpec{Path: "/opt/resource/in", Args: []string{"/tmp/build/get"}},
					runtime.ProcessIO{
						Stdin:  strings.NewReader("{}"),
						Stdout: new(bytes.Buffer),
						Stderr: stderr,
					},
				)
				if err != nil {
					return StepOutcome{}, fmt.Errorf("run container: %w", err)
				}

				pods := clientset.CoreV1().Pods(namespace)
				pod, err := pods.Get(ctx, handle, metav1.GetOptions{})
				if err != nil {
					return StepOutcome{}, fmt.Errorf("get pod: %w", err)
				}
				pod.Status.Phase = corev1.PodPending
				pod.Status.Conditions = []corev1.PodCondition{{
					Type:    corev1.PodScheduled,
					Status:  corev1.ConditionFalse,
					Reason:  "Unschedulable",
					Message: "0/3 nodes are available: insufficient cpu.",
				}}
				if _, err := pods.UpdateStatus(ctx, pod, metav1.UpdateOptions{}); err != nil {
					return StepOutcome{}, fmt.Errorf("update pod status: %w", err)
				}

				_, waitErr := process.Wait(ctx)
				msg := ""
				if waitErr != nil {
					msg = waitErr.Error()
				}
				return StepOutcome{Err: waitErr, Message: msg, Stderr: stderr.String()}, nil
			},
		),

		// A pod that reaches a terminal phase without a container status is
		// the case podExitCode falls back for: the kubelet pruned the status,
		// or the container never started at all. The fallback decides whether
		// a build passes, and both directions were uncovered — a Failed pod
		// defaulting to 0 turns a dead task into a green build.
		brine.DefineMap[StepRunning, StepOutcome](
			"the pod reaches {string} without ever reporting a container status",
			func(in StepRunning, p brine.Params, _ *brine.Recorder) (StepOutcome, error) {
				phase, ok := p.GetString(0)
				if !ok {
					return StepOutcome{}, fmt.Errorf("expected a pod phase parameter")
				}
				return in.settlePod(func(pod *corev1.Pod) {
					pod.Status.Phase = corev1.PodPhase(phase)
					pod.Status.ContainerStatuses = nil
				})
			},
		),

		// A non-zero exit is a RESULT, not an error: Process.Wait returns
		// ProcessResult{ExitStatus: 1} with a nil error for a failed task. An
		// assertion on Err alone cannot tell a failed step from a passing one,
		// which is how the first version of these scenarios passed while
		// asserting nothing.
		//
		// Worded distinctly from step-integration's "the step reports exit
		// status {int}", which sits on a different state.
		//
		// A step that failed outright has no exit status to compare at all, so
		// that case reaches the reader from the getter rather than as a
		// mismatch against a zero it never reported.
		CheckInt[StepOutcome]("the step's exit status is {int}",
			"the step's exit status",
			func(in StepOutcome) (int, error) {
				if in.Err != nil {
					return 0, fmt.Errorf("expected an exit status, the step failed outright with %q", in.Message)
				}
				return in.ExitStatus, nil
			}),

		// The step's result is the MAIN container's exit code, not whichever
		// container happens to have terminated first. The sidecar is listed
		// ahead of main here deliberately: reading "any terminated container"
		// then picks up the sidecar's 0 and reports a failed step as green.
		brine.DefineMap[StepRunning, StepOutcome](
			"a sidecar exits {int} before the main container exits {int}",
			func(in StepRunning, p brine.Params, _ *brine.Recorder) (StepOutcome, error) {
				sidecarCode, ok := p.GetInt(0)
				if !ok {
					return StepOutcome{}, fmt.Errorf("expected a sidecar exit code")
				}
				mainCode, ok := p.GetInt(1)
				if !ok {
					return StepOutcome{}, fmt.Errorf("expected a main exit code")
				}
				return in.settlePod(func(pod *corev1.Pod) {
					pod.Status.Phase = corev1.PodFailed
					pod.Status.ContainerStatuses = []corev1.ContainerStatus{
						{
							Name: "log-shipper",
							State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
								ExitCode: int32(sidecarCode),
							}},
						},
						{
							Name: "main",
							State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
								ExitCode: int32(mainCode),
							}},
						},
					}
				})
			},
		),
	}
}
