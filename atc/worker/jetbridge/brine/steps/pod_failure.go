package steps

import (
	"fmt"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PodFailureDefinitions covers the ways a step's pod can die, migrated from
// process_test.go's failure-detection and diagnostics blocks.
//
// The consumer here is the person reading a red build. They need two things:
// the failure named accurately enough to act on (an OOM is a memory problem, a
// CrashLoopBackOff after an OOM is still a memory problem), and enough
// diagnostic detail in the log to tell whether the fault is theirs or the
// cluster's. Everything below asserts one of those two.

func PodFailureDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		// RF-01: an OOM kill in the CURRENT termination state, as opposed to
		// the restart-cycle case the priority scenarios already cover.
		brine.DefineMap[StepRunning, StepOutcome](
			"the main container is killed for using too much memory",
			func(in StepRunning, _ brine.Params, _ *brine.Recorder) (StepOutcome, error) {
				return in.settlePod(func(pod *corev1.Pod) {
					pod.Status.Phase = corev1.PodRunning
					pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
						Name: "main",
						State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
							Reason: "OOMKilled", ExitCode: 137,
						}},
					}}
				})
			},
		),

		// RF-05: the node reclaimed the pod. The build did not fail; the
		// cluster took it away, and the log has to say so or the user blames
		// their own pipeline.
		brine.DefineMap[StepRunning, StepOutcome](
			"the node evicts the pod",
			func(in StepRunning, _ brine.Params, _ *brine.Recorder) (StepOutcome, error) {
				return in.settlePod(func(pod *corev1.Pod) {
					pod.Status.Phase = corev1.PodFailed
					pod.Status.Reason = "Evicted"
					pod.Status.Message = "The node was low on resource: memory"
				})
			},
		),

		// RF-07: nothing in the cluster can host this pod. Without a message
		// the step simply hangs until it times out.
		brine.DefineMap[StepRunning, StepOutcome](
			"no node can accept the pod",
			func(in StepRunning, _ brine.Params, _ *brine.Recorder) (StepOutcome, error) {
				return in.settlePod(func(pod *corev1.Pod) {
					pod.Status.Phase = corev1.PodPending
					pod.Status.Conditions = []corev1.PodCondition{{
						Type:    corev1.PodScheduled,
						Status:  corev1.ConditionFalse,
						Reason:  "Unschedulable",
						Message: "0/3 nodes are available: insufficient cpu",
					}}
				})
			},
		),

		// RF-06: eviction, node failure, spot preemption, or a human with
		// kubectl. All arrive the same way.
		brine.DefineMap[StepRunning, StepOutcome](
			"the pod is deleted from the cluster",
			func(in StepRunning, _ brine.Params, _ *brine.Recorder) (StepOutcome, error) {
				pods := in.Clientset.CoreV1().Pods(in.Namespace)
				if err := pods.Delete(in.Ctx, in.Handle, metav1.DeleteOptions{}); err != nil {
					return StepOutcome{}, fmt.Errorf("delete pod %q: %w", in.Handle, err)
				}
				_, waitErr := in.Process.Wait(in.Ctx)
				msg := ""
				if waitErr != nil {
					msg = waitErr.Error()
				}
				return StepOutcome{Err: waitErr, Message: msg, Stderr: in.Stderr.String()}, nil
			},
		),

		// RF-10/RF-11: the diagnostics are the difference between a red build
		// a user can act on and one they have to escalate.
		brine.DefineCheck[StepOutcome](
			"the build log explains {string}",
			func(in StepOutcome, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a text parameter")
				}
				if !strings.Contains(in.Stderr, want) {
					return fmt.Errorf("expected the build log to explain %q; it said %q", want, truncate(in.Stderr, 400))
				}
				return nil
			},
		),

		brine.DefineCheck[StepOutcome](
			"the failure explains {string}",
			func(in StepOutcome, p brine.Params, _ *brine.Recorder) error {
				want, ok := p.GetString(0)
				if !ok {
					return fmt.Errorf("expected a text parameter")
				}
				if in.Err == nil {
					return fmt.Errorf("expected the step to fail explaining %q, it succeeded", want)
				}
				if !strings.Contains(in.Message, want) {
					return fmt.Errorf("expected the failure to explain %q, got %q", want, in.Message)
				}
				return nil
			},
		),

		brine.DefineCheck[StepOutcome](
			"the step is told the pod was deleted",
			func(in StepOutcome, _ brine.Params, _ *brine.Recorder) error {
				if in.Err == nil {
					return fmt.Errorf("expected the step to fail, it succeeded")
				}
				if !strings.Contains(in.Message, "deleted") {
					return fmt.Errorf("expected the failure to say the pod was deleted, got %q", in.Message)
				}
				return nil
			},
		),
	}
}

// settlePod applies a status mutation and then waits, returning what the step
// saw. It is the shared shape of every failure scenario.
func (in StepRunning) settlePod(mutate func(*corev1.Pod)) (StepOutcome, error) {
	pods := in.Clientset.CoreV1().Pods(in.Namespace)
	pod, err := pods.Get(in.Ctx, in.Handle, metav1.GetOptions{})
	if err != nil {
		return StepOutcome{}, fmt.Errorf("get pod %q: %w", in.Handle, err)
	}
	mutate(pod)
	if _, err := pods.UpdateStatus(in.Ctx, pod, metav1.UpdateOptions{}); err != nil {
		return StepOutcome{}, fmt.Errorf("update pod status: %w", err)
	}
	_, waitErr := in.Process.Wait(in.Ctx)
	msg := ""
	if waitErr != nil {
		msg = waitErr.Error()
	}
	return StepOutcome{Err: waitErr, Message: msg, Stderr: in.Stderr.String()}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
