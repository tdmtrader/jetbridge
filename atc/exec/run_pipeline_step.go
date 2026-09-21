package exec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/tracing"
)

// RunPipelineStep admits one run of a template pipeline on the build's own
// team, and stops there.
//
// It does not wait for the child run, does not observe its status and does not
// fail when it fails. Waiting, the causal edge and any second iteration belong
// to the run-contract track; this step's whole job is to make the admission
// happen and to say, in the build's event stream, which run it got.
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

	// The API route that creates a run is screened by the policy wrappa; this
	// step reaches no route, so a policy agent would never see a
	// build-initiated run unless it is asked here. It is asked with the params
	// exactly as they will be recorded: the references the author wrote, plus
	// the build's resolved local values. That is what set_pipeline shows for
	// its own config too, and it is the only honest thing to show -- a secret
	// the run never stores is not part of the call the agent is judging.
	//
	// A policy refusal errors the step rather than failing it, exactly as it
	// does for set_pipeline: the build did not fail on its own terms, it was
	// stopped.
	err = delegate.CheckRunPipelinePolicy(step.metadata.TeamName, resolvedPlan.Name, resolvedPlan.Params)
	if err != nil {
		return false, err
	}

	digest, err := runPipelineInputDigest(step.metadata.TeamName, resolvedPlan)
	if err != nil {
		return false, err
	}

	admitted, err := step.admitter.AdmitChildRun(ctx, ChildRunRequest{
		BuildID: step.metadata.BuildID,
		PlanID:  step.planID,

		Template: runs.TemplateRef{
			Team:     step.metadata.TeamName,
			Pipeline: atc.PipelineRef{Name: resolvedPlan.Name},
		},
		Params: resolvedPlan.Params,

		// The build acting for itself. It is authorized for its own team and
		// no other, which is the rule set_pipeline already applies to a build
		// mutating configs, and every field of it is metadata the engine
		// handed this step.
		Principal: runs.Principal{
			Build: &runs.BuildPrincipal{
				TeamName:     step.metadata.TeamName,
				PipelineName: step.metadata.PipelineName,
				JobName:      step.metadata.JobName,
				BuildName:    step.metadata.BuildName,
				BuildID:      step.metadata.BuildID,
			},
		},

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
		// That is what puts it in front of exec.LogError and exec.RetryError,
		// and what lets an aborted build read as aborted: the engine's finish
		// path tests errors.Is(err, context.Canceled), which a swallowed error
		// defeats. It is deliberately not written to stderr -- the engine
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

	if url := runPipelineRunURL(step.metadata, resolvedPlan.Name, admitted.Number); url != "" {
		fmt.Fprintf(stdout, "%s\n", url)
	}

	delegate.Finished(logger, true)

	return true, nil
}

// runPipelineCallDigestDomain separates this digest's inputs from any other
// use of sha256 in the tree, and versions them. A change to what the digest
// covers is a change to this string, so an old recorded digest can never
// silently compare equal to a new one computed differently.
const runPipelineCallDigestDomain = "run-pipeline-call/v1\x00"

// runPipelineDigestInputs is the sealed shape of a call, and the field order
// here is the canonical order: encoding/json emits struct fields in
// declaration order, so this struct *is* the canonicalization for the three
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
