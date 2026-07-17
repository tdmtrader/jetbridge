package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/harvest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/tracing"
	"github.com/concourse/concourse/vars"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const harvestProcessID = "harvest"

type HarvestStepOption func(*HarvestStep)

// WithHarvestTicketsStore wires the single-writer ticket store so the
// exec can transition running→needs_review/errored after the runner
// exits (contracts §2.1 — harvest is the primary needs_review writer).
func WithHarvestTicketsStore(s tickets.Store) HarvestStepOption {
	return func(h *HarvestStep) { h.ticketsStore = s }
}

// WithHarvestRunVerifier wires the same server-side linkage gate the
// agent step uses: a plan-carried ticket id transitions ONLY when the
// (verified) pipeline run was dispatched for it.
func WithHarvestRunVerifier(v AgentRunVerifier) HarvestStepOption {
	return func(h *HarvestStep) { h.runVerifier = v }
}

// HarvestStep is the terminal delivery step (contracts §2.8.1), v0:
// verify the committed workspace and push the stable ticket branch via
// harvest-runner, then transition the ticket. Gates and the judge are
// the full harvest-step workstream; plans declaring them are refused
// here, never silently skipped.
type HarvestStep struct {
	planID            atc.PlanID
	plan              atc.HarvestPlan
	metadata          StepMetadata
	containerMetadata db.ContainerMetadata
	workerPool        Pool
	delegateFactory   TaskDelegateFactory
	defaultTimeout    time.Duration
	agentImage        string
	ticketsStore      tickets.Store
	runVerifier       AgentRunVerifier
}

func NewHarvestStep(
	planID atc.PlanID,
	plan atc.HarvestPlan,
	metadata StepMetadata,
	containerMetadata db.ContainerMetadata,
	workerPool Pool,
	delegateFactory TaskDelegateFactory,
	defaultTimeout time.Duration,
	agentImage string,
	opts ...HarvestStepOption,
) Step {
	s := &HarvestStep{
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

func (step *HarvestStep) Run(ctx context.Context, state RunState) (bool, error) {
	start := time.Now()
	delegate := step.delegateFactory.TaskDelegate(state)
	ctx, span := delegate.StartSpan(ctx, "harvest", tracing.Attrs{
		"name": step.plan.Name,
	})

	ok, err := step.run(ctx, state, delegate)
	tracing.End(span, err)
	metric.RecordStepDuration(ctx, "harvest", step.plan.Name, time.Since(start))

	return ok, err
}

func (step *HarvestStep) run(ctx context.Context, state RunState, delegate TaskDelegate) (bool, error) {
	logger := lagerctx.FromContext(ctx)
	logger = tracing.LoggerWithSpan(ctx, logger)
	logger = logger.Session("harvest-step", lager.Data{
		"step-name": step.plan.Name,
		"job-id":    step.metadata.JobID,
	})

	oteltrace.SpanFromContext(ctx).AddEvent("step.initializing")
	delegate.Initializing(logger)

	if step.agentImage == "" {
		return false, errors.New("harvest step requires the web node to be started with --agent-step-image")
	}

	// v0 refusals: fail closed on config the runner cannot enforce (the
	// dogfood ticket #5 pattern — never a silent skip).
	if len(step.plan.GatePolicy.Gates) > 0 || step.plan.GatePolicy.OnGateFailure != "" {
		return false, errors.New("harvest v0 has no gate engine: remove gate_policy (full harvest-step workstream, wave 3)")
	}
	if step.plan.Judge != nil {
		return false, errors.New("harvest v0 has no judge: remove the judge block (full harvest-step workstream, wave 3)")
	}
	if step.plan.DevMCP != nil {
		return false, errors.New("harvest v0 runs no gates, so dev_mcp has no consumer: remove it")
	}
	if step.plan.Push && step.plan.Branch == "" {
		return false, errors.New("harvest push requires a branch")
	}

	// Env is static-only, same rule as agent steps (§2.8): values are
	// render/dispatch-time literals; anything still carrying a var ref
	// was never resolved and must fail closed.
	planEnv := make(map[string]string, len(step.plan.Env))
	for k, v := range step.plan.Env {
		if refs := vars.ExtractVarRefs(v); len(refs) > 0 {
			return false, fmt.Errorf("harvest env %s contains unresolved var reference ((%s))", k, refs[0].String())
		}
		planEnv[k] = v
	}

	// F30 run-id fallback: the renderer cannot place ((run_id)) in the
	// int pipeline_run_id field, so identity travels as env.
	runID := step.plan.PipelineRunID
	if runID == 0 {
		runID, _ = strconv.Atoi(planEnv["AGENT_PIPELINE_RUN_ID"])
	}

	workdir := step.containerMetadata.WorkingDirectory

	cfg := harvest.Config{
		StepName:      step.plan.Name,
		Workspace:     step.plan.Workspace,
		Repo:          step.plan.Repo,
		TargetBranch:  step.plan.TargetBranch,
		TicketID:      step.plan.TicketID,
		PipelineRunID: runID,
		Branch:        step.plan.Branch,
		Push:          step.plan.Push,
	}
	cfgPayload, err := json.Marshal(cfg)
	if err != nil {
		return false, err
	}

	env := step.metadata.TaskEnv()
	for k, v := range planEnv {
		env = append(env, k+"="+v)
	}
	env = append(env,
		"HARVEST_CONFIG="+string(cfgPayload),
		"HARVEST_WORKSPACE_DIR="+artifactPath(workdir, step.plan.Workspace, ""),
	)

	containerSpec := runtime.ContainerSpec{
		TeamID:   step.metadata.TeamID,
		TeamName: step.metadata.TeamName,
		JobID:    step.metadata.JobID,
		StepName: step.plan.Name,

		ImageSpec: runtime.ImageSpec{ImageURL: step.agentImage},
		Env:       env,
		Type:      step.containerMetadata.Type,

		Dir: workdir,
	}

	if step.plan.Push {
		// §8.3: the per-repo git credential mounts read-only on the main
		// container only; sidecars (none in v0) can never see it.
		containerSpec.SecretMounts = []runtime.SecretMount{{
			SecretName: harvest.GitCredSecretName(step.plan.Repo),
			MountPath:  "/var/run/agent/git",
		}}
		containerSpec.Env = append(containerSpec.Env, "HARVEST_CREDS_DIR=/var/run/agent/git")
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

	processSpec := runtime.ProcessSpec{
		ID:   harvestProcessID,
		Path: "harvest-runner",
		Dir:  workdir,
	}
	process, err := attachOrRun(ctx, container, processSpec, runtime.ProcessIO{
		Stdout: delegate.Stdout(),
		Stderr: delegate.Stderr(),
	})
	if err != nil {
		return false, err
	}

	result, runErr := process.Wait(ctx)
	if runErr != nil {
		if errors.Is(runErr, context.DeadlineExceeded) {
			step.transitionTicket(ctx, logger, "errored", "", "harvest timed out")
			delegate.Errored(logger, TimeoutLogMessage)
			return false, nil
		}
		return false, runErr
	}

	// §2.8.1 exit taxonomy → ticket lifecycle (§2.1): 0/1 land in
	// needs_review (branch recorded only on ok+push), 2 errors the ticket.
	switch result.ExitStatus {
	case 0:
		branch := ""
		if step.plan.Push {
			branch = step.plan.Branch
		}
		step.transitionTicket(ctx, logger, "needs_review", branch, "")
	case 1:
		step.transitionTicket(ctx, logger, "needs_review", "", "")
	default:
		step.transitionTicket(ctx, logger, "errored", "", fmt.Sprintf("harvest platform error (exit %d) — see the harvest step log", result.ExitStatus))
	}

	delegate.Finished(logger, ExitStatus(result.ExitStatus))
	return result.ExitStatus == 0, nil
}

// transitionTicket moves the plan's ticket through the single writer,
// guarded exactly like the agent step's admission: the plan-carried
// ticket id counts only when the server-verified run was dispatched for
// it (plan env is attacker-writable YAML). Stale/racing transitions are
// benign — the run-completion reconciler and manual close-out are
// legitimate concurrent writers.
func (step *HarvestStep) transitionTicket(ctx context.Context, logger lager.Logger, to string, branch, errorDetail string) {
	ticketID := step.plan.TicketID
	if ticketID <= 0 || step.ticketsStore == nil {
		return
	}
	runID := step.plan.PipelineRunID
	if runID == 0 {
		runID, _ = strconv.Atoi(step.plan.Env["AGENT_PIPELINE_RUN_ID"])
	}

	if step.runVerifier == nil {
		logger.Info("skipping-ticket-transition", lager.Data{"reason": "no run verifier wired"})
		return
	}
	owned, err := step.runVerifier.RunBelongsToPipeline(runID, step.metadata.PipelineID)
	if err != nil || !owned {
		logger.Info("skipping-ticket-transition", lager.Data{"reason": "run not verified for this pipeline", "run-id": runID})
		return
	}
	linked, err := step.runVerifier.TicketBelongsToRun(ticketID, runID)
	if err != nil || !linked {
		logger.Info("skipping-ticket-transition", lager.Data{"reason": "ticket not dispatched as this run", "ticket-id": ticketID, "run-id": runID})
		return
	}

	meta := tickets.TransitionMeta{Branch: branch, ErrorDetail: errorDetail}
	err = step.ticketsStore.Transition(ticketID, tickets.StateRunning, tickets.State(to), meta)
	switch {
	case err == nil:
		logger.Info("ticket-transitioned", lager.Data{"ticket-id": ticketID, "to": to, "branch": branch})
	case errors.Is(err, tickets.ErrStaleTransition), errors.Is(err, tickets.ErrTicketNotFound):
		// benign: a concurrent writer (reconciler, manual close-out) won
		logger.Info("ticket-transition-superseded", lager.Data{"ticket-id": ticketID, "to": to, "error": err.Error()})
	default:
		logger.Error("ticket-transition-failed", err, lager.Data{"ticket-id": ticketID, "to": to})
	}
}
