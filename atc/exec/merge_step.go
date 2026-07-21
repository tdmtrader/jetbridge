package exec

import (
	"context"
	"encoding/json"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/concourse/concourse/agent/api/outcomes"
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/harvest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/tracing"
)

const mergeProcessID = "merge"

// MergeStep runs the platform-owned merge (design 2026-07-20 §2). Because
// the PLATFORM performs the merge, the outcome it records is a FACT — the
// step knows the resulting sha because it produced it — rather than
// something the outcome watcher infers from a mirror afterwards.
type MergeStep struct {
	planID            atc.PlanID
	plan              atc.MergePlan
	metadata          StepMetadata
	containerMetadata db.ContainerMetadata
	workerPool        Pool
	delegateFactory   TaskDelegateFactory
	defaultTimeout    time.Duration
	agentImage        string

	ticketsStore  tickets.Store
	outcomesStore outcomes.Store
}

type MergeStepOption func(*MergeStep)

func WithMergeTicketsStore(s tickets.Store) MergeStepOption {
	return func(step *MergeStep) { step.ticketsStore = s }
}

func WithMergeOutcomesStore(s outcomes.Store) MergeStepOption {
	return func(step *MergeStep) { step.outcomesStore = s }
}

func NewMergeStep(
	planID atc.PlanID,
	plan atc.MergePlan,
	metadata StepMetadata,
	containerMetadata db.ContainerMetadata,
	workerPool Pool,
	delegateFactory TaskDelegateFactory,
	defaultTimeout time.Duration,
	agentImage string,
	opts ...MergeStepOption,
) Step {
	s := &MergeStep{
		planID:            planID,
		plan:              plan,
		metadata:          metadata,
		containerMetadata: containerMetadata,
		workerPool:        workerPool,
		delegateFactory:   delegateFactory,
		defaultTimeout:    defaultTimeout,
		agentImage:        agentImage,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (step *MergeStep) Run(ctx context.Context, state RunState) (bool, error) {
	start := time.Now()
	delegate := step.delegateFactory.TaskDelegate(state)
	ctx, span := delegate.StartSpan(ctx, "merge", tracing.Attrs{
		"name": step.plan.Name,
	})

	ok, err := step.run(ctx, state, delegate)
	tracing.End(span, err)
	metric.RecordStepDuration(ctx, "merge", step.plan.Name, time.Since(start))

	return ok, err
}

func (step *MergeStep) run(ctx context.Context, state RunState, delegate TaskDelegate) (bool, error) {
	logger := lagerctx.FromContext(ctx)
	logger = tracing.LoggerWithSpan(ctx, logger)
	logger = logger.Session("merge-step", lager.Data{
		"step-name": step.plan.Name,
		"ticket-id": step.plan.TicketID,
		"branch":    step.plan.Branch,
		"target":    step.plan.TargetBranch,
	})

	workdir := "/tmp/build/merge"

	cfg, err := json.Marshal(struct {
		Repo         string             `json:"Repo"`
		TicketID     int                `json:"TicketID"`
		Branch       string             `json:"Branch"`
		TargetBranch string             `json:"TargetBranch"`
		Method       string             `json:"Method"`
		Message      string             `json:"Message"`
		Push         bool               `json:"Push"`
		GatePolicy   harvest.GatePolicy `json:"gate_policy"`
	}{
		Repo: step.plan.Repo, TicketID: step.plan.TicketID,
		Branch: step.plan.Branch, TargetBranch: step.plan.TargetBranch,
		Method: methodOrDefault(step.plan.Method), Message: step.plan.Message,
		Push: step.plan.Push, GatePolicy: step.plan.GatePolicy,
	})
	if err != nil {
		return false, err
	}

	env := []string{
		"MERGE_CONFIG=" + string(cfg),
		"MERGE_WORKSPACE_DIR=" + artifactPath(workdir, step.plan.Workspace, ""),
		"AGENT_PLAN_ID=" + string(step.planID),
	}
	for k, v := range step.plan.Env {
		env = append(env, k+"="+v)
	}

	containerSpec := runtime.ContainerSpec{
		TeamID:   step.metadata.TeamID,
		TeamName: step.metadata.TeamName,
		JobID:    step.metadata.JobID,
		StepName: step.plan.Name,

		ImageSpec: runtime.ImageSpec{ImageURL: step.agentImage},
		Env:       env,
		Type:      step.containerMetadata.Type,
		Dir:       workdir,
	}

	if step.plan.Push {
		// §8.3: the per-repo git credential mounts read-only on the main
		// container only. The target is the TICKET's branch (jetbridge for
		// platform work, with CI promoting to main), so this needs no more
		// privilege than harvest already holds.
		containerSpec.SecretMounts = []runtime.SecretMount{{
			SecretName: harvest.GitCredSecretName(step.plan.Repo),
			MountPath:  "/var/run/agent/git",
		}}
		containerSpec.Env = append(containerSpec.Env, "MERGE_CREDS_DIR=/var/run/agent/git")
	}

	repository := state.ArtifactRepository()
	artifact, fromCache, found := repository.ArtifactFor(build.ArtifactName(step.plan.Workspace))
	if !found {
		return false, MissingInputsError{[]string{step.plan.Workspace}}
	}
	containerSpec.Inputs = []runtime.Input{{
		Artifact:        artifact,
		DestinationPath: artifactPath(workdir, step.plan.Workspace, ""),
		FromCache:       fromCache,
	}}

	tracing.Inject(ctx, &containerSpec)

	owner := db.NewBuildStepContainerOwner(step.metadata.BuildID, step.planID, step.metadata.TeamID)

	if err := delegate.BeforeSelectWorker(logger); err != nil {
		return false, err
	}
	chosenWorker, err := step.workerPool.FindOrSelectWorker(
		ctx, owner, containerSpec, worker.Spec{TeamID: step.metadata.TeamID},
	)
	if err != nil {
		return false, err
	}

	ctx, cancel, err := MaybeTimeout(ctx, step.plan.Timeout, step.defaultTimeout)
	if err != nil {
		return false, err
	}
	defer cancel()
	ctx = lagerctx.NewContext(ctx, logger)

	delegate.SelectedWorker(logger, chosenWorker.Name())

	container, _, err := chosenWorker.FindOrCreateContainer(ctx, owner, step.containerMetadata, containerSpec, delegate)
	if err != nil {
		return false, err
	}

	oteltrace.SpanFromContext(ctx).AddEvent("step.starting")
	delegate.Starting(logger)

	process, err := attachOrRun(ctx, container, runtime.ProcessSpec{
		ID:   mergeProcessID,
		Path: "merge-runner",
		Dir:  workdir,
	}, runtime.ProcessIO{
		Stdout: delegate.Stdout(),
		Stderr: delegate.Stderr(),
	})
	if err != nil {
		return false, err
	}

	result, runErr := process.Wait(ctx)
	if runErr != nil {
		// A dead pod or broken exec stream never reaches the exit taxonomy;
		// without this the ticket would sit in needs_review with a dead run
		// (the live finding harvest hit on ticket #12).
		logger.Error("merge-process-failed", runErr)
		delegate.Errored(logger, "merge process failed: "+runErr.Error())
		return false, runErr
	}

	// Exit taxonomy mirrors harvest-runner: 0 ok, 1 fail (conflict, failing
	// gate, or a target that moved), 2 platform error.
	switch result.ExitStatus {
	case 0:
		if step.plan.Push {
			step.recordMerged(ctx, logger)
		}
		delegate.Finished(logger, ExitStatus(result.ExitStatus))
		return true, nil
	case 1:
		// A legitimate non-landing. The ticket STAYS in needs_review — the
		// branch is still a candidate once the conflict or failure is fixed.
		delegate.Finished(logger, ExitStatus(result.ExitStatus))
		return false, nil
	default:
		delegate.Errored(logger, "merge errored (exit 2)")
		return false, nil
	}
}

// recordMerged writes the merge as FACT. The outcome row closes with the
// sha the platform itself produced, and the ticket transitions through the
// single-writer Transition. No mirror inference is involved, which is the
// entire point of the design.
//
// Both writes are best-effort and detached from the step context: the merge
// has already landed on the remote, so failing the step here would report a
// merge that demonstrably happened as failed.
func (step *MergeStep) recordMerged(ctx context.Context, logger lager.Logger) {
	if step.plan.TicketID <= 0 {
		return
	}
	detached := context.WithoutCancel(ctx)
	_ = detached

	if step.ticketsStore != nil {
		err := step.ticketsStore.Transition(
			step.plan.TicketID,
			tickets.StateNeedsReview,
			tickets.StateMerged,
			tickets.TransitionMeta{},
		)
		if err != nil {
			// A human may have raced us to a disposition; the merge fact is
			// still recorded on the outcome row below.
			logger.Info("merge-transition-skipped", lager.Data{"error": err.Error()})
		}
	}

	if step.outcomesStore != nil {
		if err := step.outcomesStore.RecordMerge(step.plan.TicketID, outcomes.MergeResult{
			State: outcomes.Merged,
		}); err != nil {
			logger.Info("merge-outcome-record-failed", lager.Data{"error": err.Error()})
		}
	}
}

func methodOrDefault(m string) string {
	if m == "" {
		return "merge"
	}
	return m
}
