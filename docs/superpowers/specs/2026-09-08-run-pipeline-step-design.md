# The `run_pipeline` step: composition's first caller

**Status:** Approved for implementation on `claude/agentic-v4`
**Date:** 2026-09-08
**Scope:** A build step that admits one child run of a template pipeline
through `atc/agent/composition`. Fire-and-forget. No causal edge, no waiting,
no output return, no second iteration.

## Why this and why now

`core` carries v4's first two commits: `atc/runs`, the run-admission port, and
`atc/agent/composition`, which admits a child run idempotently on
`(build_id, plan_id)`. Neither is constructed outside a suite file. The
boundary `architecture_test.go` defends has nothing on the far side of it.

This step is the smallest feature that makes both reachable from a pipeline,
and it does so with no model in the loop, so the wiring gets debugged before
anything agentic depends on it.

## The step

```yaml
jobs:
- name: release
  plan:
  - get: source
  - run_pipeline: version-upgrade      # a template on this build's own team
    params:
      ref: ((source.ref))
```

- `run_pipeline: <name>` names a template pipeline on the calling build's team.
  There is no `team:` field. Cross-team calls are out of scope; the port
  refuses them as `runs.ErrUnauthorized`, which the step reports.
- `params:` are the run's parameters, matched against the template's declared
  `params:` schema by the port. `((var))` references are interpolated with the
  build's credential variables the same way `set_pipeline`'s `vars:` are.
- The step succeeds when the run is admitted (or re-attached to). It does not
  wait for the child run, does not observe its status, and does not fail when
  it fails. That is the run-contract track's work, and it is not this one.
- On success the step writes one line to its stdout naming the admitted run
  (`admitted run #<number> of <team>/<pipeline>` plus its URL under the
  external URL) and whether it was a replay. That line is in the build's event
  stream, which is the whole of slice-1 visibility.
- A rerun of the build, or two web nodes tracking one build, re-attaches to
  the run the first admission got. That is `composition_calls`' unique index
  doing its job.
- The step's input digest is `sha256("run-pipeline-call/v1\x00" +
  canonicalJSON({team, pipeline, params}))` over the *interpolated* params,
  computed by the step. A re-attach whose digest differs from the recorded one
  is refused by composition and the step fails with that error verbatim. That
  is the designed behaviour: the call's identity is the node, its inputs are
  recorded once.

## Names, exactly

| Layer | Name |
|---|---|
| YAML key | `run_pipeline` |
| `atc.Step` config | `atc.RunPipelineStep{Name string \`json:"run_pipeline"\`; Params RunParams \`json:"params,omitempty"\`}` |
| `atc.StepVisitor` | `VisitRunPipeline(*RunPipelineStep) error` |
| `atc.Plan` field | `RunPipeline *RunPipelinePlan \`json:"run_pipeline,omitempty"\`` |
| `atc.RunPipelinePlan` | `{Name string \`json:"name"\`; Params RunParams \`json:"params,omitempty"\`}` |
| `atc.StepPrecedence` entry | `Key: "run_pipeline"`, placed directly after `set_pipeline` |
| `atc.StepRecursor` hook | `OnRunPipeline func(*RunPipelineStep) error` |
| Public plan | `RunPipelinePlan.Public()` exposes `name` only; params are never public |
| creds evaluator | `creds.NewRunPipelinePlan(vars.Variables, atc.RunPipelinePlan).Evaluate() (atc.RunPipelinePlan, error)` |
| exec step | `atc/exec/run_pipeline_step.go`: `NewRunPipelineStep(planID, plan, metadata, delegateFactory, admitter ChildRunAdmitter) Step` |
| exec delegate | `RunPipelineStepDelegateFactory` / `RunPipelineStepDelegate` (embeds `BuildStepDelegate`, nothing more in slice 1) |
| exec port | `exec.ChildRunAdmitter` (below) |
| engine | `CoreStepFactory.RunPipelineStep(atc.Plan, StepMetadata, DelegateFactory) exec.Step`; `stepperFactory.buildRunPipelineStep`; `DelegateFactory.RunPipelineStepDelegate` |
| runs port | `runs.Principal` gains the build form (below) |
| wiring | `atc/atccmd` becomes the one entry in `architecture_test.go`'s `wiringPoints` |
| web | `Concourse.BuildStepRunPipeline StepName` decoded from `run_pipeline`; StepTree header `run_pipeline:` |

## The exec-side port

`atc/exec` does not import the agentic layer. It declares what it needs:

```go
// ChildRunAdmitter admits one child run for one node of one build. The only
// production implementation is atc/agent/composition, adapted at the
// composition root; exec names neither.
type ChildRunAdmitter interface {
    AdmitChildRun(context.Context, ChildRunRequest) (ChildRun, error)
}

type ChildRunRequest struct {
    BuildID     int
    PlanID      atc.PlanID
    Template    runs.TemplateRef
    Params      atc.RunParams
    Principal   runs.Principal
    InputDigest string
}

type ChildRun struct {
    RunID    int
    Number   int   // see "What the port returns"
    Replayed bool
}
```

`atc/exec` importing `atc/runs` is core importing core; both are classified
core by `architecture_test.go`.

The adapter from `exec.ChildRunRequest` to `composition.Request` lives in
`atc/atccmd`, next to where `runs.NewAdmitter` and `composition.NewService`
are constructed. It is the only place core names `atc/agent/composition`, and
`wiringPoints` says so with this document as the reason.

### What the port returns

`composition.Result` carries `RunID` and `Replayed` only; the run's number is
not on it, and `runs.Run` (which has `Number`) is visible only inside the
before-commit hook. The stdout line wants the number, because that is what
`fly runs` and the URL use.

Decision: `composition.Result` gains `Number int`, populated from the
`runs.Run` the hook receives on first admission, and from a `SELECT` on the
replay path. The replay path already reads `composition_iterations`; the
number lives on `pipeline_runs`, which is a core table. Composition may not
read core tables. So instead the replay path returns `RunID` only, and the
exec step resolves the number through **core's own read path**: add a
`Number` to `runs.Run` (already there) and a single read operation on the
port, `Admitter.LookupRun(ctx, Tx, runID) (Run, error)`. That is the read
operation `runs.go` says "there is not one yet" about. It reads
`pipeline_runs` by id and nothing else.

So: `composition.Result{RunID, Number, Replayed}`, with `Number` filled on
both paths, the replay path calling `LookupRun` inside its own transaction.

## The build principal

`runs.Principal{Claims}` authorizes through the accessor against the team's
auth config. A build has no claims. Slice 1 extends the port:

```go
type Principal struct {
    // Claims is a verified token's claims. Exactly one of Claims and Build
    // is set.
    Claims map[string]any

    // Build is a build acting for itself. It is authorized for its own
    // team and no other, which is the rule set_pipeline already applies to
    // a build mutating pipelines on its own team.
    Build *BuildPrincipal
}

type BuildPrincipal struct {
    TeamName     string
    PipelineName string
    JobName      string
    BuildName    string
    BuildID      int
}
```

`authorize`:
- both set or neither set → `ErrPrincipalAmbiguous` (new, in errors.go).
- `Build != nil` → authorized iff `strings.EqualFold(teamName, Build.TeamName)`;
  otherwise `ErrUnauthorized`. `createdBy` is
  `"build:" + TeamName + "/" + PipelineName + "/" + JobName + "#" + BuildName`
  (for a one-off build, `PipelineName`/`JobName` are empty and it is
  `"build:" + TeamName + "/#" + BuildName`, but one-off builds cannot run this
  step: the planner is only reached through job configs). `isAdmin` is false.
  `team` is resolved from the same `GetTeamsInTx` read.
- The team read stays a single `GetTeamsInTx`, so the connection budget test
  is unchanged.

The exec step builds the principal from `StepMetadata` and never reads the
database itself.

## Validation

`StepValidator.VisitRunPipeline`:
- context `.run_pipeline(<name>)`
- `ValidateIdentifier(name)` like set_pipeline; empty name is an error
  ("no pipeline specified").
- no other checks. The template's existence and its param schema are
  admission-time facts, not config-time ones, exactly as `set_pipeline` does
  not verify the file exists.

## What is out of scope and where it goes

| Not here | Belongs to |
|---|---|
| `CausedByRun` on the child | run-contract track |
| waiting on the child run, propagating its result | run-contract track |
| iteration ordinal > 1, loops, retries re-admitting | run-contract track |
| cross-team calls, `team:` field | not planned |
| a build event type for the admission | not needed while the stdout line suffices |
| fly `--watch` for the child | not planned |

## Tests that must exist

- `atc`: step unmarshal/marshal round-trip; precedence; validator errors and
  warnings; recursor hook; `Public()` hides params.
- `atc/builds`: planner emits `RunPipelinePlan` with params passed through.
- `atc/creds`: evaluator interpolates nested `((var))`s in params.
- `atc/runs`: build principal authorizes own team, refuses other team
  (case-insensitively for the match; `ErrUnauthorized` for a different
  team), refuses ambiguous principal; `LookupRun` finds an admitted run and
  refuses an unknown id; db_free_consumer still builds without atc/db.
- `atc/agent/composition`: `Result.Number` on first admission and on replay.
- `atc/exec`: with a counterfeiter `ChildRunAdmitter` — success line on
  stdout, replayed line differs, refusals surface on stderr and fail the step
  (`Finished(false)`), interpolation happens before the digest, digest is
  stable across param key order.
- `atc/engine`: builder dispatches `plan.RunPipeline` to the core factory.
- root: `architecture_test.go` passes with `atc/atccmd` as a wiring point;
  `composition_boundary_test.go` passes.
- web: `make test-elm` passes; the decoder test covers `run_pipeline`.
