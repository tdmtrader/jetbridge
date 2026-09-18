package steps

import (
	"context"
	"fmt"
	"strings"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
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

		// RF-05: actual pod-local storage eviction, not supplied status or
		// node-wide pressure. The kubelet owns the failure and its event.
		brine.DefineMap[LiveTaskPlan, StepOutcome](
			"the kubelet evicts a pod that exceeds its scratch volume limit",
			func(in LiveTaskPlan, _ brine.Params, rec *brine.Recorder) (StepOutcome, error) {
				return observeLiveVolumeEviction(in, rec)
			},
		),

		// RF-06: eviction, node failure, spot preemption, or a human with
		// kubectl. All arrive the same way.
		TransformUsing[WorkerReady, StepOutcome](
			"the runtime watches pod {string} until it is deleted",
			[]string{"real-cluster"},
			func(in WorkerReady, a Args, res brine.Resources) (StepOutcome, error) {
				cluster, ok := res.Get("real-cluster").(*realCluster)
				if !ok {
					return StepOutcome{}, fmt.Errorf("real-cluster resource is %T", res.Get("real-cluster"))
				}
				return observeProcessDeletion(in, a.String(0), cluster)
			},
		),

		// RF-10/RF-11: the diagnostics are the difference between a red build
		// a user can act on and one they have to escalate.
		//
		// Keeps its own body: it bounds the log it quotes, the way every
		// build-log check in this package does. A detail func adds to a
		// failure, it does not shrink the "got" the combinator prints, so
		// CheckContains would still dump a step's whole stderr — and passing a
		// TRUNCATED value to the getter would change the assertion, since text
		// past the cut would stop matching.
		CheckContains[StepOutcome]("the build log explains {string}",
			"the build log",
			func(in StepOutcome) (string, error) { return in.Stderr, nil }),

		CheckThat[StepOutcome]("the step is told the pod was deleted",
			func(in StepOutcome) error {
				if in.Err == nil {
					return fmt.Errorf("expected the step to fail, it succeeded")
				}
				if !strings.HasPrefix(in.Message, "pod deleted externally:") {
					return fmt.Errorf("expected the failure to say the pod was deleted, got %q", in.Message)
				}
				return nil
			}),
	}
}

// SeveredExecOutcome is what a step and its downstream see after the exec
// connection is cut mid-run.
type SeveredExecOutcome struct {
	Err       error
	Message   string
	Locator   *jetbridge.ArtifactLocator
	OutputKey string
	artifact  runtime.Artifact
	ctx       context.Context
	daemon    *liveArtifactDaemon
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

		brine.DefineMap[LiveTaskPlan, SeveredExecOutcome](
			"a task step whose connection to its pod is severed while it writes {string}",
			func(in LiveTaskPlan, p brine.Params, rec *brine.Recorder) (SeveredExecOutcome, error) {
				output, ok := p.GetString(0)
				if !ok {
					return SeveredExecOutcome{}, fmt.Errorf("expected output name")
				}
				return severLiveArtifact(in, rec, output)
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
				return requireUnpublishedLiveArtifact(in)
			}),
	}
}
