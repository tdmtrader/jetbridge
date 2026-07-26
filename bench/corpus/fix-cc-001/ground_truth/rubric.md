# Rubric — fix-cc-001

Primary grading is **mechanical** (see `case.yaml#grading`). This checklist is
the behavioral backstop: what a judge scores when the mechanical run is
ambiguous, and what separates a real fix from a diff that merely turns the
withheld specs green. Score intent, not diff similarity.

## What the change must do

1. **Instance vars win inside `set_pipeline`.** When the step supplies a name
   through `instance_vars` and the same name is also supplied by the step's
   `vars`, the value used to interpolate the pipeline configuration is the
   instance var's. This is the load-bearing requirement.
2. **Cover `var_files` too, not just `vars`.** The step has three static sources:
   `plan.Vars`, each file in `plan.VarFiles`, and `plan.InstanceVars`. Instance
   vars must beat *both* of the others. A change that only reorders instance vars
   relative to `plan.Vars`, leaving a `var_files` entry able to shadow an
   instance var, is a partial pass — the reference change covers both because it
   puts instance vars at the head of the list.
3. **Leave the relative order of the other sources alone.** `plan.Vars` before
   `plan.VarFiles` is existing behavior and is not part of this bug. Reordering
   them is an unrequested behavior change.
4. **Do not achieve it by changing shared resolution semantics.** `vars.MultiVars`
   returns the first source that has the key, and `vars.NewTemplateResolver`
   documents that the slice is tried in order. Those semantics are consumed by
   `atc/exec/task_config_source.go`, `atc/db/pipeline.go` and
   `fly/commands/internal/templatehelpers`. Inverting `MultiVars.Get` to
   last-wins, or reversing the slice inside the resolver, makes this one symptom
   go away by silently flipping precedence everywhere else. **Reject it** — it
   fails the task's explicit constraint about shared machinery, even if it were
   to pass some subset of the suites. Concretely, `fly`'s `yaml_template.go`
   already leans on first-wins: it iterates `--load-vars-from` files in reverse
   precisely so that files given later on the command line end up *earlier* in
   the slice. Flipping the resolver silently reverses documented `fly` behavior.
5. **Add a regression test** in the existing `atc/exec` Ginkgo suite that pins
   the *rendered configuration*, not merely that the step succeeded or that the
   instance var reached `atc.PipelineRef`. The bug never affected the pipeline's
   identity — `fly pipelines` was always right — so a test asserting only the ref
   would have passed throughout. A test that asserts the resolved task args (or
   the config handed to `SavePipeline` / compared by `Config.Diff`) is what pins
   it. Judge this from the pre-overlay capture of the agent's tree, per
   `case.yaml#grading` caveat 1.
6. **Explain the direction.** The fix is a move, and moving the block the *other*
   way is equally small and completely wrong. Credit reasoning that establishes
   which end of the slice wins — from `MultiVars.Get` returning on the first hit,
   or from the `NewTemplateResolver` doc comment, or from
   `vars/multi_vars_test.go`'s "return found value as soon as one source
   succeeds". An agent that asserts a direction without grounding it got the
   right answer for no reason; note that in the score.

## What the change must not do

- **No exported signature changes.** `FetchPipelineConfig`, `NewSetPipelineStep`,
  `vars.NewTemplateResolver`, `vars.NewMultiVars` keep their signatures.
- **No new dependencies.** `maps` is already imported at pre_state; the reference
  needs no import change at all.
- **No change to how instance vars are stored or displayed.** `atc.PipelineRef`,
  `SavePipeline`, the instance name shown by `fly pipelines` are all already
  correct and must stay untouched.
- **No collision detection.** The task explicitly does not ask for a warning or
  an error when a name is defined twice; adding one is scope creep and changes
  the output of pipelines that are working as intended today.
- **No defensive-copy regression.** The instance-var map is also the pipeline's
  identity; whatever the fix does, resolution must not be able to mutate it. The
  reference preserves this by carrying `maps.Clone` along with the moved block.
- **No behavior change when names do not collide.** Every existing `set_pipeline`
  step renders exactly as before.

## Acceptable variation

Any of these are fine if (1)–(6) hold:

- moving the `InstanceVars` append to the head of `staticVars` (what the human
  did — a six-line move),
- building the slice head-first some other way (e.g. constructing it as
  `[]vars.Variables{instanceVars}` up front, or prepending),
- an explicit merge that resolves collisions in favor of instance vars, provided
  it preserves the `vars`-before-`var_files` order among the remaining sources
  and does not mutate `plan.InstanceVars`.

Extracting a named helper or adding a clarifying comment is neutral. The
reference change itself adds one: *"add instance vars first so that they take
precedence during evaluation later"*.

## Mechanical caveats — read before scoring a red run

- **The overlay destroys the agent's own test.** Grading restores
  `atc/exec/set_pipeline_step_test.go` verbatim, so requirement (5) can never be
  judged from the graded tree. Score it from the pre-overlay capture. A useful
  check: apply the agent's test alone to the pre_state tree — a real regression
  test fails there. One that passes at pre_state pins nothing and is at best a
  partial pass on (5).
- **A new agent-authored test file can produce a false fail.** If the agent put
  its coverage in a *new* file in package `exec_test`, that file survives the
  overlay and may redeclare a fixture the restored file already defines — a
  compile error, not a defective fix. Move agent-added test files aside for the
  fail_to_pass leg, then judge them separately.
- **The failing specs are not named after the bug.** With the overlay applied at
  pre_state, what goes red are three long-standing specs in the *"when no diff"*
  context (`should log 'no changes to apply'`, `should send a set pipeline
  changed event`, `should update the job and build id`), because the wrongly
  rendered config no longer matches the fake's stored config. A grader that
  greps the output for an instance-var spec name will conclude, incorrectly,
  that nothing ran.

## Pricing the memorization risk

`memorization_risk: high`. This is a public upstream PR from November 2025,
inside the training window, and the answer is a six-line move whose commit
subject states it outright. A model may simply recall it. Credit the
*derivation* — the agent showing that `MultiVars` is first-wins and therefore
that the head of the slice is the winning end — over an unexplained correct
edit, and never report this case's result on its own as evidence of small-fix
capability.

## Reference

`ground_truth/reference.diff` — the merged human change
(`a2e2367cb8161ad47ba31311e2152a7c2ef6ebe0`): +6/−4 in
`atc/exec/set_pipeline_step.go`, nothing else.
`ground_truth/test.diff` and `ground_truth/withheld_tests/` — the author's
failing-test commit (`ef5857d889bf91bb7e2206095669fd0d47b99df2`), which is the
grading oracle and is never exposed. Use the reference as an existence proof of a
correct solution, not as the target to match.
