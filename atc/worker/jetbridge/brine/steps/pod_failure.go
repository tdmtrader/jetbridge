package steps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
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
		//
		// Keeps its own body: it bounds the log it quotes, the way every
		// build-log check in this package does. The generic message would dump
		// a step's whole stderr into the failure.
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

		CheckContains[StepOutcome]("the failure explains {string}",
			"the failure",
			func(in StepOutcome) (string, error) {
				if in.Err == nil {
					return "", fmt.Errorf("expected the step to fail, it succeeded")
				}
				return in.Message, nil
			}),

		CheckThat[StepOutcome]("the step is told the pod was deleted",
			func(in StepOutcome) error {
				if in.Err == nil {
					return fmt.Errorf("expected the step to fail, it succeeded")
				}
				if !strings.Contains(in.Message, "deleted") {
					return fmt.Errorf("expected the failure to say the pod was deleted, got %q", in.Message)
				}
				return nil
			}),
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
	result, waitErr := in.Process.Wait(in.Ctx)
	msg := ""
	if waitErr != nil {
		msg = waitErr.Error()
	}
	return StepOutcome{
		Err: waitErr, Message: msg, Stderr: in.Stderr.String(),
		ExitStatus: result.ExitStatus,
	}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// SeveredExecOutcome is what a step and its downstream see after the exec
// connection is cut mid-run.
type SeveredExecOutcome struct {
	Err       error
	Message   string
	Locator   *jetbridge.ArtifactLocator
	OutputKey string
}

// severingExec is a real PodExecutor whose connection dies mid-step, the way a
// web restart or an API-server rollout kills a long SPDY stream.
type severingExec struct{}

func (severingExec) ExecInPod(
	_ context.Context, _, _, _ string, _ []string,
	_ io.Reader, _, _ io.Writer, _ bool, _ jetbridge.ExecAttrs,
) error {
	return errors.New("error dialing backend: EOF")
}

// SeveredExecDefinitions covers F23 — what must NOT happen when the exec
// connection to a running step is severed.
//
// The step's process is still alive in the pod, still writing its outputs. If
// the runtime published an artifact location anyway, an on_failure or on_error
// hook could stream out a HALF-WRITTEN artifact and get no error at all. The
// missing location is what makes the hook fail fast instead, so the assertion
// is about an absence — and absences are exactly what a spy assertion cannot
// distinguish from "the call happened with different arguments".
func SeveredExecDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{

		brine.DefineMapUsing[brine.Empty, SeveredExecOutcome](
			"a task step whose connection to its pod is severed while it writes {string}",
			[]string{"jetbridge-db"},
			func(_ brine.Empty, p brine.Params, _ *brine.Recorder, res brine.Resources) (SeveredExecOutcome, error) {
				output, ok := p.GetString(0)
				if !ok {
					return SeveredExecOutcome{}, fmt.Errorf("expected an output name")
				}
				cluster, err := NewCluster(res,
					WithExecutor(severingExec{}),
				)
				if err != nil {
					return SeveredExecOutcome{}, err
				}
				ctx, clientset, worker := cluster.Ctx, cluster.Clientset, cluster.Worker
				handle := "severed-handle"
				locator := jetbridge.NewArtifactLocator()
				worker.SetArtifactLocator(locator)

				container, _, err := worker.FindOrCreateContainer(
					ctx,
					db.NewFixedHandleContainerOwner(handle),
					db.ContainerMetadata{Type: db.ContainerTypeTask},
					runtime.ContainerSpec{
						TeamID:    1,
						Dir:       "/tmp/build/workdir",
						ImageSpec: runtime.ImageSpec{ImageURL: "busybox"},
						Outputs:   runtime.OutputPaths{output: "/tmp/build/workdir/" + output},
					},
					&noopDelegate{},
				)
				if err != nil {
					return SeveredExecOutcome{}, fmt.Errorf("find or create container: %w", err)
				}

				process, err := container.Run(ctx,
					runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", "echo hi"}},
					runtime.ProcessIO{Stdout: new(bytes.Buffer), Stderr: new(bytes.Buffer)},
				)
				if err != nil {
					return SeveredExecOutcome{}, fmt.Errorf("run step: %w", err)
				}

				pods := clientset.CoreV1().Pods("test-namespace")
				pod, err := pods.Get(ctx, handle, metav1.GetOptions{})
				if err != nil {
					return SeveredExecOutcome{}, fmt.Errorf("get pod: %w", err)
				}
				pod.Status.Phase = corev1.PodRunning
				pod.Status.Conditions = []corev1.PodCondition{
					{Type: corev1.PodReady, Status: corev1.ConditionTrue},
				}
				if _, err := pods.UpdateStatus(ctx, pod, metav1.UpdateOptions{}); err != nil {
					return SeveredExecOutcome{}, fmt.Errorf("update pod: %w", err)
				}

				_, waitErr := process.Wait(ctx)
				msg := ""
				if waitErr != nil {
					msg = waitErr.Error()
				}
				return SeveredExecOutcome{
					Err: waitErr, Message: msg, Locator: locator,
					OutputKey: handle + "-output-" + output,
				}, nil
			},
		),

		CheckThat[SeveredExecOutcome]("the step fails rather than reporting success",
			func(in SeveredExecOutcome) error {
				if in.Err == nil {
					return fmt.Errorf("expected the severed step to fail; it reported success")
				}
				if !strings.Contains(in.Message, "exec in pod") {
					return fmt.Errorf("expected the failure to name the exec, got %q", in.Message)
				}
				return nil
			}),

		CheckThat[SeveredExecOutcome]("the half-written artifact cannot be located by a later step",
			func(in SeveredExecOutcome) error {
				if _, found := in.Locator.Locate(in.OutputKey); found {
					return fmt.Errorf(
						"the torn artifact %q is locatable — an on_failure hook could stream out a "+
							"half-written artifact and get NO error, which is the failure this guards",
						in.OutputKey)
				}
				return nil
			}),
	}
}
