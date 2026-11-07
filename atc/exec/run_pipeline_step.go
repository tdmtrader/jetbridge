package exec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/tracing"
)

// RunPipelineObserveInterval is how long a waited run_pipeline step sleeps
// between two observations of its child run. A variable so specs can shorten
// it.
var RunPipelineObserveInterval = 10 * time.Second

// runPipelineCancelTimeout bounds the cancellation a step asks for on its way
// out of an ended context, which it makes under a context of its own.
const runPipelineCancelTimeout = 10 * time.Second

// RunPipelineStep admits one run of a template pipeline on the build's own
// team and, when the plan says wait, follows it to the end.
//
// Without wait the step's whole job is to make the admission happen and to
// say, in the build's event stream, which run it got. With wait it then polls
// the child run's status until it is terminal, and succeeds only if the child
// succeeded. Between polls it holds nothing: no container, no worker
// placement, no database connection -- each observation is one short call on
// the port. If the build's context ends while it waits (abort, `timeout:`,
// fail_fast) it asks for the child to be cancelled on its way out.
//
// A waited call stores the child's run id as this plan's result, which is how
// a later call's inputs name it: `inputs: {x: {from: <step>, result: r}}` is
// resolved by the planner to this plan's id, and read back here from state.
//
// Everything it needs about the calling build comes from StepMetadata, which
// the engine already filled in: the step reads no database of its own, and the
// principal it presents is assembled from those fields alone.
type RunPipelineStep struct {
	planID          atc.PlanID
	plan            atc.RunPipelinePlan
	metadata        StepMetadata
	delegateFactory RunPipelineStepDelegateFactory
	admitter        ChildRunAdmitter
}

func NewRunPipelineStep(
	planID atc.PlanID,
	plan atc.RunPipelinePlan,
	metadata StepMetadata,
	delegateFactory RunPipelineStepDelegateFactory,
	admitter ChildRunAdmitter,
) Step {
	return &RunPipelineStep{
		planID:          planID,
		plan:            plan,
		metadata:        metadata,
		delegateFactory: delegateFactory,
		admitter:        admitter,
	}
}

func (step *RunPipelineStep) Run(ctx context.Context, state RunState) (bool, error) {
	delegate := step.delegateFactory.RunPipelineStepDelegate(state)
	ctx, span := delegate.StartSpan(ctx, "run_pipeline", tracing.Attrs{
		"name": step.plan.Name,
	})

	ok, err := step.run(ctx, state, delegate)
	tracing.End(span, err)

	return ok, err
}

func (step *RunPipelineStep) run(ctx context.Context, state RunState, delegate RunPipelineStepDelegate) (bool, error) {
	logger := lagerctx.FromContext(ctx)
	logger = logger.Session("run-pipeline-step", lager.Data{
		"step-name": step.plan.Name,
		"job-id":    step.metadata.JobID,
	})

	delegate.Initializing(logger)

	// The params are resolved before anything else, and in particular before
	// the digest is taken: everything downstream -- the digest, the policy
	// check, the run header the port stores -- must see one and the same
	// statement of the call.
	//
	// Only the build's own local vars (`((.:name))`) are interpolated. Every
	// other reference travels verbatim, because the params are persisted on
	// the run header and served to anyone who can view the team: see
	// creds.RunPipelinePlan for why, and atc.RunConfig for the substitution
	// that lets `((vault/x))` resolve in the payload at build time instead.
	resolvedPlan, err := creds.NewRunPipelinePlan(state, step.plan).Evaluate()
	if err != nil {
		return false, err
	}

	stdout := delegate.Stdout()
	stderr := delegate.Stderr()

	delegate.Starting(logger)

	digest, err := runPipelineInputDigest(step.metadata.TeamName, resolvedPlan)
	if err != nil {
		return false, err
	}

	inputs, err := runPipelineInputs(state, resolvedPlan.Inputs)
	if err != nil {
		return false, err
	}

	// The build acting for itself. It is authorized for its own team and no
	// other, which is the rule set_pipeline already applies to a build
	// mutating configs, and every field of it is metadata the engine handed
	// this step.
	principal := runs.Principal{
		Build: &runs.BuildPrincipal{
			TeamName:     step.metadata.TeamName,
			PipelineName: step.metadata.PipelineName,
			JobName:      step.metadata.JobName,
			BuildName:    step.metadata.BuildName,
			BuildID:      step.metadata.BuildID,
		},
	}

	admitted, err := step.admitter.AdmitChildRun(ctx, ChildRunRequest{
		BuildID: step.metadata.BuildID,
		PlanID:  step.planID,

		Template: runs.TemplateRef{
			Team:     step.metadata.TeamName,
			Pipeline: atc.PipelineRef{Name: resolvedPlan.Name},
		},
		Params:    resolvedPlan.Params,
		Inputs:    inputs,
		Principal: principal,

		InputDigest: digest,
	})
	if err != nil {
		// A refusal is a fact about this pipeline's config or about the
		// template's state: the team is not the caller's, the template does
		// not exist or is not a template, it is paused or archived, the params
		// do not satisfy its declared schema, or a re-attach presented inputs
		// that have moved since the call was recorded. Retrying changes none
		// of it and the pipeline's author is the one who can, so the message
		// goes to stderr where they read it and the step fails.
		//
		// runs.IsRefusal owns that set, rather than a list here: the step
		// cannot name composition's refusal at all, and a second copy of the
		// vocabulary would go stale on the first one the port adds.
		if runs.IsRefusal(err) {
			fmt.Fprintf(stderr, "%s\n", err)
			delegate.Finished(logger, false)

			return false, nil
		}

		// Everything else is a fault, and the step errors rather than fails.
		// That is what puts it in front of exec.LogError, and what lets an
		// aborted build read as aborted: the engine's finish path tests
		// errors.Is(err, context.Canceled), which a swallowed error defeats. It is deliberately not written to stderr -- the engine
		// reports an errored step itself, and a build log is anonymously
		// readable on a public pipeline, which is no place for a driver's
		// message.
		return false, err
	}

	verb := "admitted"
	if admitted.Replayed {
		// A rerun of this build, or a second web node tracking it, arrives
		// here. Saying so is the difference between a log that looks like two
		// runs were started and one that shows the same run twice.
		verb = "re-attached to"
	}

	fmt.Fprintf(stdout, "%s run #%d of %s/%s\n",
		verb, admitted.Number, step.metadata.TeamName, resolvedPlan.Name)

	url := runPipelineRunURL(step.metadata, resolvedPlan.Name, admitted.Number)
	if url != "" {
		fmt.Fprintf(stdout, "%s\n", url)
	}

	if !step.plan.Wait {
		delegate.Finished(logger, true)

		return true, nil
	}

	// Stored before waiting, and on a re-attach as much as on an admission:
	// a replayed build starts with empty state, and a later call bound to
	// this one reads the run id from here.
	state.StoreResult(step.planID, admitted.RunID)

	status, err := step.waitForChildRun(ctx, admitted, principal, stdout, stderr)
	if err != nil {
		if runs.IsRefusal(err) {
			fmt.Fprintf(stderr, "%s\n", err)
			delegate.Finished(logger, false)

			return false, nil
		}

		return false, err
	}

	if status != atc.RunStatusSucceeded {
		// The child ran and did not succeed. That is this step failing, not
		// erroring: the run's own log says why, and the URL leads to it.
		fmt.Fprintf(stderr, "run #%d %s\n", admitted.Number, status)
		if url != "" {
			fmt.Fprintf(stderr, "%s\n", url)
		}
		delegate.Finished(logger, false)

		return false, nil
	}

	fmt.Fprintf(stdout, "run #%d succeeded\n", admitted.Number)
	delegate.Finished(logger, true)

	return true, nil
}

// waitForChildRun polls the child run until it is terminal and returns its
// status. Each observation is one call on the port, and nothing is held
// between them.
//
// When the build's context ends first, it asks for the child to be cancelled
// and returns the context's error, which is what lets the engine read an
// aborted build as aborted.
func (step *RunPipelineStep) waitForChildRun(
	ctx context.Context,
	child ChildRun,
	principal runs.Principal,
	stdout, stderr io.Writer,
) (atc.RunStatus, error) {
	ticker := time.NewTicker(RunPipelineObserveInterval)
	defer ticker.Stop()

	for {
		status, err := step.admitter.ObserveChildRun(ctx, ChildRunObservation{
			RunID:     child.RunID,
			Principal: principal,
		})
		if err != nil {
			if ctx.Err() != nil {
				return "", step.cancelChildRun(ctx, child, principal, stdout, stderr)
			}

			return "", err
		}

		if status.Terminal() {
			return status.Status, nil
		}

		select {
		case <-ctx.Done():
			return "", step.cancelChildRun(ctx, child, principal, stdout, stderr)
		case <-ticker.C:
		}
	}
}

// cancelChildRun asks for the child run to stop, because the build that is
// waiting for it has been told to, and returns the build's context error.
//
// The request is made under a context of its own, since the build's has
// already ended, and bounded so a port that does not answer cannot hold the
// build open. A child that was already stopping or had already finished
// answers nil, which is as good as accepted. A failed request is written to
// stderr and does not change what the step returns: the build was aborted
// either way.
func (step *RunPipelineStep) cancelChildRun(
	ctx context.Context,
	child ChildRun,
	principal runs.Principal,
	stdout, stderr io.Writer,
) error {
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runPipelineCancelTimeout)
	defer cancel()

	fmt.Fprintf(stdout, "cancelling run #%d\n", child.Number)

	err := step.admitter.CancelChildRun(cancelCtx, ChildRunCancellation{
		RunID:     child.RunID,
		Principal: principal,
		Reason:    "parent build aborted",
	})
	if err != nil {
		// The fault goes to the web log: a build log can be read by anyone a
		// public pipeline admits, and a driver error is not theirs to see.
		lagerctx.FromContext(ctx).Error("cancel-child-run", err, lager.Data{"run": child.RunID})
		fmt.Fprintf(stderr, "failed to cancel run #%d\n", child.Number)
	}

	return ctx.Err()
}

// runPipelineInputs binds each of the plan's inputs to the child run its
// `from` call admitted, which that call stored as its result. The planner
// only lets `from` name an earlier waited call, so a missing result means
// that guarantee broke, and the step errors rather than admit a child with an
// input silently missing.
func runPipelineInputs(state RunState, plans map[string]atc.RunPipelineInputPlan) (map[string]atc.RunInputSource, error) {
	if len(plans) == 0 {
		return nil, nil
	}

	inputs := make(map[string]atc.RunInputSource, len(plans))
	for name, input := range plans {
		var runID int
		if !state.Result(input.From, &runID) {
			return nil, fmt.Errorf("run_pipeline input %q: no run recorded for plan %s", name, input.From)
		}

		inputs[name] = atc.RunInputSource{RunID: runID, Result: input.Result}
	}

	return inputs, nil
}

// runPipelineCallDigestDomain separates this digest's inputs from any other
// use of sha256 in the tree, and versions them. A change to what the digest
// covers is a change to this string, so an old recorded digest can never
// silently compare equal to a new one computed differently.
const runPipelineCallDigestDomain = "run-pipeline-call/v1\x00"

// runPipelineDigestInputs is the sealed shape of a call, and the field order
// here is the canonical order: encoding/json emits struct fields in
// declaration order, so this struct *is* the canonicalization for the
// top-level values.
//
// Params needs no such treatment, because encoding/json sorts map keys when it
// marshals a map. That is what makes the digest independent of the order the
// author wrote the params in, or of the order the YAML decoder happened to
// hand them over in, without this file sorting anything itself.
type runPipelineDigestInputs struct {
	Team     string        `json:"team"`
	Pipeline string        `json:"pipeline"`
	Params   atc.RunParams `json:"params"`

	// Inputs are the plan's bindings -- the call each input is from, by plan
	// id, and the result it names -- not the run ids they resolve to, which
	// core's replay compares on its own. Omitted when empty, so a call
	// without inputs digests exactly as it did before inputs existed and
	// needs no new domain.
	Inputs map[string]atc.RunPipelineInputPlan `json:"inputs,omitempty"`
}

// runPipelineInputDigest computes the digest recorded with the call.
//
// It is taken over the params as the run will record them: the references the
// author wrote, plus whatever the build's own local vars resolved to. That is
// what "seals what was asked for" means here. Two builds of the same job whose
// `((.:name))` values differ are two different calls, and a re-attach that
// finds a different digest is refused rather than answered with the earlier
// run.
//
// A credential rotation does not change the digest, because it does not change
// what was asked for: `((vault/x))` is still `((vault/x))`, resolved afresh by
// the payload on every build. That is the same rule a pipeline's config hash
// already follows -- rotating a secret does not make a pipeline's config
// different -- and it is what keeps a rerun of a build re-attaching to its own
// run instead of being refused for a change nobody made.
func runPipelineInputDigest(team string, plan atc.RunPipelinePlan) (string, error) {
	canonical, err := json.Marshal(runPipelineDigestInputs{
		Team:     team,
		Pipeline: plan.Name,
		Params:   plan.Params,
		Inputs:   plan.Inputs,
	})
	if err != nil {
		return "", err
	}

	sum := sha256.Sum256(append([]byte(runPipelineCallDigestDomain), canonical...))

	return hex.EncodeToString(sum[:]), nil
}

// runPipelineRunURL addresses the admitted run the way the web does: a run
// lives at runs/<number> under its template's team-and-pipeline path, which is
// the form web/elm/src/Routes.elm builds and the form the run API mirrors. The
// build URL in StepMetadata.Env is assembled the same way, from the same
// external URL, and like it this leaves the segments unescaped -- team and
// pipeline names are validated identifiers.
//
// An ATC with no external URL configured has nothing to point at, and an empty
// string tells the caller to print no line rather than half of one.
func runPipelineRunURL(metadata StepMetadata, pipeline string, number int) string {
	if metadata.ExternalURL == "" {
		return ""
	}

	return fmt.Sprintf("%s/teams/%s/pipelines/%s/runs/%d",
		metadata.ExternalURL, metadata.TeamName, pipeline, number)
}
