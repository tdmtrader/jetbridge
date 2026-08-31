Feature: What each kind of step promises

  A pipeline is written in steps, and each kind of step makes a promise its
  author relies on: a get fetches the version it was told to, a put publishes
  what it created and nothing when it did not, set_pipeline does not undo
  somebody else's newer change, a task says which artifact it could not find,
  and a retry stops when it has succeeded while an abort stops now.

  Source: atc/exec — get_step_test.go, put_step_test.go,
  set_pipeline_step_test.go, task_step_test.go, retry_step_test.go,
  retry_error_step_test.go and on_abort_test.go.

  WHY THIS FILE IS SHORT, WHICH IS THE FINDING.

  The migration's engine is "replace the recording double with a working one
  and assert the round trip", and in atc/exec most of that payoff had been
  collected before brine existed: the suite already runs on real PostgreSQL,
  with the real engine delegates, reading real build_events back out. So there
  was little left to collect, and every scenario below had to earn its place
  on the sentence alone.

  A CORRECTION TO WHAT THAT PARAGRAPH FIRST SAID. It said atc/exec "has no
  recording double left to replace". The tree says otherwise:
  worker_pool_test.go:69 scriptedPool records the arguments handed to the
  pool, get_step_test.go:49 recordingGetDelegate records the ORDER of delegate
  calls, and get_step_test.go:1115 recordingLockFactory counts lock
  acquisitions. The true, narrower statement is that each of those records
  something PostgreSQL cannot show — which is also why the dispositions below
  decline the assertions built on them, and why steps/step_execution.go's own
  execStepPool exists: it is one of those doubles replaced with a working one,
  answering the cache lookup from what a scenario said is true.

  Most of atc/exec's assertions do not. They are true and worth keeping and
  they are not sentences: "sets the worker spec with teamID", "adds
  state-transition span events", "calls Errored but not Finished", "gets the
  container owner from the delegate". A Gherkin line for any of those is a
  worse Go test with a longer name. The disposition comments at the foot of
  each section say which ones and why, and those tests stay exactly where they
  are.

  WHAT WAS WORTH BUILDING: A RESOURCE THAT ANSWERS.

  The suite scripts the resource process with a canned reply —
  `ProcessStub{Output: someVersion}` — so the version that comes back is a
  constant the test supplied, it comes back no matter what was asked for, and
  nothing there reads the request the step wrote on stdin.

  WHAT THAT DOES NOT MEAN, because this file first said it did. It does not
  mean "a get that had lost its version pin entirely would pass" in ginkgo. It
  would not. get_step_test.go:312 reads the resource cache row back out of
  resource_cache_uses and asserts its version. MEASURED: with the
  `getPlan.Version != nil` arm of NewVersionSourceFromPlan disabled through a
  build overlay (go build -overlay, production untouched),
  `go test -overlay=... ./atc/exec/ -run TestExec -args
  -ginkgo.focus="constructs the resource cache correctly"` reports "Ran 1 of
  563 Specs ... 1 Failed", against "1 Passed" unmutated. The pin is pinned
  there, one layer below the wire.

  What the answering resource does buy is a resource that can say NO. It
  holds versions and answers only for a version it holds, refusing anything
  else the way a real `in` script refuses a ref that is not in the repository.
  So "pinned to v2" can fail on the step itself rather than only on a row read
  afterwards, a put followed by a get is a round trip — the put adds the
  version to the catalogue the get then reads — rather than two constants
  compared, and the cache-hit scenario can hold nothing at all, which is a
  discriminator no canned reply can express.

  # ==========================================================================
  # Getting a version
  # ==========================================================================

  # The central promise of a get. The resource holds two versions and is asked
  # for one of them, so the answer names which one was requested; a step that
  # dropped the pin would ask for the empty version, which this resource does
  # not hold, so the get would be refused and the scenario would go red on its
  # very first row. Measured: making NewVersionSourceFromPlan ignore
  # getPlan.Version does exactly that.
  #
  # TWO consumers of the version are checked, and they are written by
  # different lines of get_step.go: the finish event the build page renders,
  # and the resource_caches row that decides whether the next build
  # re-fetches.
  #
  # A CORRECTION. This comment used to count the artifact repository as a
  # third consumer of the version. It is not one. The artifact row resolved
  # through the repository's KEYS, which are the plan's name — no version
  # travels with them, and the row would have passed for any version the
  # resource happened to hold. It went red under the pin mutation only because
  # the step failed outright. What a get's artifact CAN say is the other half
  # of its result, fromCache, so that is what the last row says now: fetched
  # here, taken from the worker's cache in the cache-hit scenario below.
  #
  # Reddened by: NewVersionSourceFromPlan ignoring getPlan.Version — measured,
  # the get asks for the empty version, the resource refuses it, and the
  # scenario stops red on its first Then. The last row is separately reddened
  # by get_step.go registering the artifact with `true` in place of fromCache —
  # measured, and that mutation reddens nothing else in this file.
  Scenario: A get step fetches the version its plan pinned
    Given a build of a job whose pipeline has the resource "some-resource"
    And the resource holds version "v1"
    And the resource holds version "v2"
    When the get step runs, pinned to version "v2"
    Then the step succeeded
    And the build fetched version "v2"
    And the build holds a resource cache for version "v2"
    And the build's artifact "some-resource" was fetched rather than taken from a cache

  # `put: some-resource` in a job is followed by an implicit get of what the
  # put just created — that is how the artifact a later task consumes comes
  # into existence. The get names no version of its own; it names the PLAN the
  # put ran under, and reads the result out of the run state.
  #
  # The resource makes this a real round trip: the put's `out` script adds
  # "v3" to the catalogue, and the get's `in` script would refuse "v3" if it
  # were not there. So the get could not succeed on a version the put did not
  # actually create, and could not succeed at all if the version failed to
  # travel between the two steps.
  #
  # The publication half is the other consumer of the same version and is
  # written by a different collaborator — the put delegate's SaveOutput — so a
  # break in either half fails one row and not the other.
  #
  # Reddened by: DynamicVersionSource returning an empty version instead of the
  # one it found (the get half), or by SaveOutput being skipped for a resource
  # the plan names (the publication half). Both measured; each reddens one row
  # and leaves the other alone.
  Scenario: The version a put created is published, and is the one the get after it fetches
    Given a build of a job whose pipeline has the resource "some-resource"
    When the build puts version "v3" and then gets what the put created
    Then the step succeeded
    And the build published version "v3"
    And the build fetched version "v3"
    And the build holds a resource cache for version "v3"

  # A cache already on the chosen worker must not be fetched again. Going back
  # to the resource for bytes that are already on the node is the whole cost
  # of a get with none of its purpose, and on a busy pipeline it is most of
  # the gets.
  #
  # The resource holds NOTHING in this scenario. That is the discriminator and
  # it is stronger than counting the script's invocations: an implementation
  # that ran the script would be refused, because there is no version "v2" to
  # serve, so "succeeded" is unreachable except by using the cache.
  #
  # The cache lookup is answered by the VERSION of the cache being asked
  # about, which is why the third row is here — the message the operator sees
  # is only correct if the cache that was found is a cache of what they asked
  # for.
  #
  # The last row is what separates this scenario from the one at the top of
  # the file, and it is not the artifact's NAME: that is the plan's name in
  # both, whichever path ran. It is the fromCache flag the get registers
  # alongside the artifact, which is the only thing about a get's result that
  # says whether the bytes were fetched or were already on the node.
  #
  # Reddened by: anything that makes the cache probe ask about the wrong
  # version — measured with NewVersionSourceFromPlan ignoring the pin, which
  # sends the lookup after the empty version, misses the cache, runs the script
  # and is refused. The last row is separately reddened by get_step.go
  # registering the artifact with `false` in place of fromCache — measured,
  # and that mutation reddens nothing else in this file.
  Scenario: A cache the worker already holds is served without running the resource script again
    Given a build of a job whose pipeline has the resource "some-resource"
    And the chosen worker already holds a cache of version "v2"
    When the get step runs, pinned to version "v2"
    Then the step succeeded
    And the build log mentions "found existing resource cache"
    And the build fetched version "v2"
    And the build's artifact "some-resource" came from a cache on the worker

  # A resource that says no is a FAILED build, not an ERRORED one. The
  # difference is the one an operator reads off the colour of the step: red
  # means the pipeline did something wrong, and the other colour means the
  # platform did. Returning an error here would tell them their cluster broke
  # when in fact a version they asked for does not exist.
  #
  # The third row is an absence, and its precondition is the second: the
  # finish event with exit status 1 says the script really ran and really
  # answered, so "no artifact" is a statement about what the get registered
  # rather than about a build that never started. A downstream task that
  # consumed a half-fetched artifact would be the defect.
  #
  # Reddened by: `return false, err` replacing the exit-status branch in
  # get_step.go, or by RegisterArtifact moving out from under
  # `if processResult.ExitStatus == 0`.
  Scenario: A get the resource refuses is a failed build, and hands nothing downstream
    Given a build of a job whose pipeline has the resource "some-resource"
    And the resource holds version "v1"
    When the get step runs, pinned to version "v9"
    Then the step failed rather than erroring
    And the build reported the get finishing with exit status 1
    And the build's artifacts do not include "some-resource"

  # A get that runs out of time reports WHY. The step itself returns a
  # failure — so the build goes red rather than being wedged — but the reason
  # is written to the build as an error event, which is the only place the
  # operator can find out that it was the timeout and not the resource.
  #
  # The absence in the last row is witnessed by the row above it: a step that
  # both errored and finished would tell the build page two contradictory
  # things about the same step, and the finish event is the one that carries
  # an exit status the UI would render as a real result.
  #
  # Reddened by: the DeadlineExceeded branch calling Finished as well
  # (measured: the last row goes red), or by dropping delegate.Errored
  # (measured: the error row goes red). Both halves are live.
  Scenario: A get that outruns its timeout says so, and never reports a finish
    Given a build of a job whose pipeline has the resource "some-resource"
    And the resource holds version "v2"
    And the resource script never answers
    And the get step is allowed "200ms" to finish
    When the get step runs, pinned to version "v2"
    Then the step failed rather than erroring
    And the build log records the error "timeout exceeded"
    And the build never reported the get finishing

  # DISPOSITION — "runs with the correct ContainerSpec", "sets the worker spec
  # with teamID", "gets the container owner from the delegate" and "emits a
  # BeforeSelectWorker event" are assertions about arguments in flight, not
  # about anything the build ends up holding. They are good Go tests and a
  # Gherkin sentence for them would only be longer.
  #
  # DISPOSITION — "retries lock acquisition until successful" and "never
  # reaches for the resource get lock" count acquisitions on a recording lock
  # factory. The outcome those protect — one get per version per worker rather
  # than a stampede — is not observable from a single scenario at all.
  #
  # DISPOSITION — the image-ref scenarios ("registers the image ref URL with
  # the tag", "does not register an image ref") vary a string-building rule
  # over the source and version maps. That is a table, and imageURLFromGetPlan
  # is a pure function; Gherkin makes it worse.

  # ==========================================================================
  # Publishing a version
  # ==========================================================================

  # The put's own version of the rule above, and the case that matters most:
  # this resource script gets HALFWAY. It names the version it was creating and
  # then exits non-zero, which is what a `docker push` that wrote a tag and
  # then lost the registry looks like. Publishing that version would put
  # something on the resource page that does not exist in the world, and every
  # job triggering on that resource would then run against it.
  #
  # The exit-status row is the precondition for the publication row: it says
  # the `out` script really ran and really answered, so "published nothing" is
  # about what the step declined to record rather than about a put that never
  # happened.
  #
  # MEASURED, AND WORTH KNOWING: the publication row is defended TWICE, and no
  # single change reddens IT. put_step.go returns early on a non-zero exit
  # before it reaches SaveOutput; one layer down, atc/resource's `run` declines
  # to decode a failed script's stdout at all, so there is nothing to publish
  # even if the early return goes. Measured three ways: dropping the resource
  # guard alone leaves the publication row green; dropping put_step's early
  # return alone reddens the two rows above it and STILL leaves the publication
  # row green; dropping both publishes "v3" and reddens it. So that row is a
  # standing guard on a defence in depth, not this scenario's discriminator —
  # which is why it is written last and the two rows that can fail on their own
  # are written first.
  #
  # Reddened by (this scenario's own discriminator): the
  # `if processResult.ExitStatus != 0` early return in put_step.go being
  # dropped — the step then reports success, and the finish it records carries
  # exit status 0.
  Scenario: A put that named a version and then failed publishes nothing
    Given a build of a job whose pipeline has the resource "some-resource"
    And the resource names a version and then fails
    When the put step runs, publishing version "v3"
    Then the step failed rather than erroring
    And the build reported the put finishing with exit status 4
    And the build published nothing at all

  # DISPOSITION — "detects inputs from params" / "passes all inputs" /
  # "passes specified inputs" are put_inputs.go, a pure function over the
  # artifact repository with its own table-driven test. Nothing about the
  # database or the build changes between the cases.

  # ==========================================================================
  # Setting a pipeline
  # ==========================================================================

  # The scenario a `set_pipeline` author eventually meets. Two builds of the
  # same job are in flight; the newer one wins the race and writes the
  # pipeline; the older one then finishes and must NOT put its older config
  # back. Rolling back would be silent — the build goes green either way — and
  # the pipeline everybody is looking at would be one commit stale with
  # nothing saying so.
  #
  # This is produced rather than injected. The ginkgo case wraps the build in
  # a type whose SavePipeline returns db.ErrSetByNewerBuild, which pins the
  # step's handling of a sentinel and says nothing about when the sentinel
  # arises. Here a genuinely later build of the same job really does set the
  # pipeline first, and everything after that — the parent_build_id predicate
  # in atc/db, the error it raises, the warning, the green step — is
  # production's own.
  #
  # The step SUCCEEDS. That is deliberate and it is the subtle half: a build
  # that was overtaken has nothing to apologise for, so failing it would turn
  # a routine race into a red pipeline.
  #
  # Reddened by: dropping the parent_build_id predicate from savePipeline in
  # atc/db/team.go. Measured: the older build's config is then applied, the
  # warning never appears, and the step's own stdout shows it removing
  # "newer-job".
  Scenario: A pipeline a newer build already set is not rolled back by an older one
    Given a build of a job in the "some-team" team that sets pipelines
    And a newer build of the same job already set "some-pipeline" to the job "newer-job"
    When the step sets "some-pipeline" to the job "older-job"
    Then the step succeeded
    And the build log mentions "the pipeline was not saved because it was already saved by a newer build"
    And the pipeline now has the job "newer-job"

  # The commonest outcome of all: nothing changed. It has to be cheap and it
  # has to be visible — an author watching a build wants to know their
  # set_pipeline did nothing on purpose — and it must still re-attribute the
  # pipeline to the build that just ran, because the pipeline page's "set by"
  # link is how anyone finds the build that owns a pipeline.
  #
  # The middle row is an equality against the config version read before the
  # step ran, not an absence: a pipeline saved again with identical content
  # still takes a new version number, so this fails if the step writes.
  #
  # Reddened by: the no-diff branch being skipped. Measured: the step then
  # writes, logs "setting pipeline" instead of "no changes to apply.", and the
  # config version moves.
  Scenario: Setting a pipeline to the config it already has changes nothing, and says so
    Given a build of a job in the "some-team" team that sets pipelines
    And the pipeline "some-pipeline" already has the job "some-job"
    When the step sets "some-pipeline" to the job "some-job"
    Then the step succeeded
    And the build log mentions "no changes to apply."
    And the pipeline was not written again
    And the pipeline records this build as the one that set it

  # A pipeline that does not validate is the author's mistake, so the step
  # fails rather than erroring, and the reason goes to the build log where
  # they will look for it. The pipeline that is already deployed keeps
  # running: a bad commit must not take a working pipeline down with it.
  #
  # The last two rows are what makes this more than a message check. The
  # pipeline exists, with a known job, before the step runs.
  #
  # Reddened by: the validation result being ignored. Measured: the step then
  # succeeds, and the deployed pipeline is replaced by the jobless one.
  Scenario: An invalid pipeline fails the step and leaves the deployed one alone
    Given a build of a job in the "some-team" team that sets pipelines
    And the pipeline "some-pipeline" already has the job "some-job"
    When the step sets "some-pipeline" from a file with no jobs in it
    Then the step failed rather than erroring
    And the build log mentions "invalid pipeline:"
    And the build log mentions "pipeline must contain at least one job"
    And the pipeline now has the job "some-job"
    And the pipeline was not written again

  # Cross-team set_pipeline is the main team's privilege and nobody else's,
  # and the reason is that it is a complete bypass of team isolation: a job in
  # one team would otherwise be able to replace another team's pipeline with
  # anything, including a pipeline that reads that team's credentials.
  #
  # Both rows are positive. The other team's pipeline exists with a known job
  # before the step runs, so the refused row says the pipeline is still
  # THEIRS rather than saying nothing is there — an assertion that would pass
  # on a scenario whose fixture never ran.
  #
  # Reddened by: the `permitted` check dropping either arm. Dropping the
  # currentTeam.Admin() arm reddens the main-team row; dropping the whole
  # check reddens the other row, and the other team's pipeline changes hands.
  Scenario Outline: Setting another team's pipeline is the main team's privilege — <case>
    Given a build of a job in the "<team>" team that sets pipelines
    And the team "other-team" already has the pipeline "some-pipeline" with the job "their-job"
    When the step sets the "other-team" pipeline "some-pipeline" to the job "our-job"
    Then <verdict>
    And the other team's pipeline has the job "<survivor>"

    Examples:
      | case                  | team      | verdict                                                                       | survivor   |
      | the main team may     | main      | the step succeeded                                                            | our-job    |
      | another team may not  | some-team | the step was refused, saying "only main team can set another team's pipeline" | their-job  |

  # DISPOSITION — "should fail with error of file not configured", "pipeline
  # file not exist" and the bad-syntax cases are argument validation and YAML
  # parsing. The messages are worth having and the tests that pin them are
  # fine where they are; none of them is a promise about the pipeline.
  #
  # DISPOSITION — "when reading the existing config fails" and "when
  # SavePipeline fails" wrap a healthy PostgreSQL-backed row in a type that
  # fails one method, and assert the error comes back out. That is error
  # propagation, and it has no outcome beyond the error.

  # ==========================================================================
  # Running a task
  # ==========================================================================

  # The message is the whole behaviour. A task whose image comes from an
  # artifact fails when nothing produced that artifact, and the author's next
  # question is always "produced by what?" — so the refusal answers it.
  # Silently running against no image, or failing with a bare not-found, both
  # leave them reading the pipeline looking for a step that is not there.
  #
  # Reddened by: TaskStep.imageSpec returning a bare not-found error instead of
  # MissingTaskImageSourceError. Measured: the author is then told "image not
  # found", which names neither the artifact nor the fix.
  Scenario: A task whose image was never produced says what should have produced it
    Given a build of a job running a task step
    And the task takes its image from the artifact "some-image"
    When the task step runs
    Then the step was refused, saying "missing image artifact source: some-image"
    And the step was refused, saying "make sure there's a corresponding 'get' step"

  # Three rules in one message, and each of them is a way an author gets this
  # wrong. Every missing input is named, not just the first, so one build
  # tells them everything they have to fix. An optional input that is absent
  # is not a problem and must not be listed. And an input that was remapped is
  # reported by the name it was LOOKED UP under — "deps", the artifact that is
  # missing — not by "vendor", the name inside the task, which would send them
  # to change the task config when the pipeline is what is wrong.
  #
  # The "code" row is the discriminator for the first rule: an input that was
  # found must not appear in the list.
  #
  # Reddened by: the input.Optional check being dropped — measured, the message
  # becomes "missing inputs: deps, config"; or by the error naming input.Name
  # rather than the mapped inputName — measured, it becomes "missing inputs:
  # vendor", which the first Then catches. The two negative rows are the
  # weaker, standing form of the same two claims.
  Scenario: A task names every input it could not find, by the name it looked for
    Given a build of a job running a task step
    And the task requires the input "code"
    And the task requires the input "vendor", supplied by the artifact "deps"
    And the task allows the optional input "config"
    And the build has produced the artifact "code"
    When the task step runs
    Then the step was refused, saying "missing inputs: deps"
    And the refusal does not mention "vendor"
    And the refusal does not mention "config"
    And the refusal does not mention "code"

  # DISPOSITION — the bulk of task_step_test.go is about the pod that gets
  # built: mounts, outputs, caches, limits, env, sidecars. Every one of those
  # is already a scenario in container-pod.feature and container-spec.feature,
  # asserted against a real Kubernetes API server rather than a ContainerSpec
  # struct. Migrating them here would state the same thing one layer up.
  #
  # DISPOSITION — "runs a task with an image it fetched from an image_resource"
  # is the delegate's FetchImage, which is atc/engine's, not the task step's.

  # ==========================================================================
  # Retrying, and aborting
  # ==========================================================================

  # `attempts: 3` means AT MOST three. The attempts below are real put steps,
  # so what an attempt leaves behind when it runs is a version on the resource
  # rather than a mark in a counter — and attempt 3 is armed to succeed, so if
  # the loop kept going after attempt 2 there would be a version "attempt-3"
  # to find. There is not.
  #
  # A retry that did not stop would triple the cost of every flaky step and,
  # worse, run a step's side effects again after it had already succeeded.
  #
  # Reddened by: the `if attemptOk { break }` going away.
  Scenario: A retried step stops at the first attempt that succeeds
    Given a build of a job whose pipeline has the resource "some-resource"
    And attempt 1 of the retried step fails
    And attempt 2 of the retried step publishes version "attempt-2"
    And attempt 3 of the retried step publishes version "attempt-3"
    When the retried step runs
    Then the step succeeded
    And the build log mentions "attempt 1"
    And the build published version "attempt-2"
    And the build published no version "attempt-3"

  # Abort means now. An operator who hits abort on a step with attempts left
  # is telling the platform to stop, and a retry loop that read the
  # cancellation as merely another failed attempt would keep going — spending
  # the remaining attempts, and holding the build open, after the person
  # asking has already walked away.
  #
  # The absence in the last row has its precondition in the one above it: the
  # build log carries attempt 1's own line, so the loop demonstrably started.
  # Attempt 2 is armed to publish, so it would leave a version if it ran.
  #
  # Reddened by: the `if ctx.Err() != nil` check at the top of RetryStep's
  # loop body being removed.
  Scenario: An aborted build does not spend its remaining attempts
    Given a build of a job whose pipeline has the resource "some-resource"
    And attempt 1 of the retried step fails, and the build is aborted while it runs
    And attempt 2 of the retried step publishes version "attempt-2"
    When the retried step runs
    Then the step was refused, saying "context canceled"
    And the build log mentions "attempt 1"
    And the build published no version "attempt-2"

  # `on_abort` is for aborts. Not for failures, and not for every error — a
  # hook that fired on any error would run cleanup on a cluster blip, and
  # people put destructive things in on_abort precisely because they believe
  # it only runs when a human stopped the build.
  #
  # Every row asserts something present. The hook is a real put, so "the hook
  # ran" is a version on the resource; and the guarded step announces itself
  # to the build log before its fate arrives, so the rows where the hook did
  # NOT run still show that the step it guards did.
  #
  # FOUR rows, because "on nothing else" is three other fates and this outline
  # first had only one of them. It had the abort and the error. The two the
  # sentence turns on hardest — a step that FAILED without erroring, and a
  # step that simply succeeded — were missing, which left on_abort.go's
  # `if stepRunErr == nil { return stepRunOk, nil }` arm unexercised by any
  # row here, and left "not for failures" as a claim with no row behind it.
  #
  # Reddened by: the errors.Is(stepRunErr, context.Canceled) test being
  # widened to any error at all — measured, the errored row goes red and the
  # other three stay green. Or by the hook firing on any unsuccessful step:
  # `!stepRunOk ||` in front of the errors.Is test, with the
  # `stepRunErr == nil` early return weakened to match — measured, the errored
  # row AND the failed row go red, and that is the mutation the failed row
  # exists for.
  #
  # No mutation reddens the failed row ALONE, and that is a property of the
  # production code rather than of this table: the early-return arm cannot be
  # lost on its own, because errors.Is(nil, context.Canceled) is false anyway,
  # so deleting the arm changes no outcome. The succeeded row goes red only
  # when the hook runs unconditionally — measured, rows 2, 3 and 4 red. Those
  # two rows are the standing form of "and on nothing else"; they are here
  # because the outline was an abort and an error, while the sentence above it
  # claimed all four.
  Scenario Outline: An on_abort hook runs on an abort and on nothing else — <case>
    Given a build of a job whose pipeline has the resource "some-resource"
    And <fate>
    And the on_abort hook publishes version "hook-ran"
    When the step runs with its on_abort hook
    Then <outcome>
    And the build log mentions "the step ran"
    And the build <verdict> version "hook-ran"

    Examples:
      | case                             | fate                                    | outcome                                                    | verdict      |
      | the build was aborted            | the step is aborted while it runs       | the step was refused, saying "context canceled"            | published    |
      | the step errored some other way  | the step cannot reach its resource host | the step was refused, saying "the resource host went away" | published no |
      | the step failed without erroring | the resource refuses the step           | the step failed rather than erroring                       | published no |
      | the step did what it was asked   | the step does what it was asked         | the step succeeded                                         | published no |

  # Some failures are worth trying again and most are not, and the platform
  # decides which without asking the author. A cluster the ATC could not
  # reach is transient; a pipeline naming a resource type that does not exist
  # will fail identically forever, and retrying it burns a container and a
  # scheduler slot every time while the author waits for a build that is never
  # going to go green.
  #
  # Both rows say what the author is told. The retried one is told twice —
  # once in the error the engine acts on, which is what the retry machinery
  # reads, and once in the build's own error events, which is the only place a
  # person can see that a retry is coming.
  #
  # The second row's last assertion is an absence with its precondition in the
  # refusal above it: the step ran and was refused, so an empty error log is a
  # statement about classification rather than about a step that never went.
  #
  # Reddened by: RetryErrorStep.toRetry answering false unconditionally (the
  # first row) or true unconditionally (the second). Measured; each reddens
  # its own row, at the verdict, and nothing else in this file.
  #
  # The notice column has its own mutation: deleting the delegate.Errored call
  # that writes "%s, will retry ...". Measured — the first row goes red at the
  # notice and nothing else moves, because the refusal is still marked for
  # retry and the build simply says nothing to the person watching it.
  #
  # A CORRECTION TO WHAT THIS COMMENT FIRST SAID, TWICE OVER.
  #
  # It claimed the first row was reddened by toRetry losing its *url.Error
  # arm. It is not: *url.Error implements Timeout() and Temporary(), so it
  # satisfies net.Error, and the very next arm catches it. Deleting the url
  # arm changes nothing at all — which is worth knowing about that function,
  # and is exactly the kind of claim this file is not allowed to make without
  # measuring it.
  #
  # And the two paragraphs above described assertions the Examples table did
  # not have. The rows said only "marked" and "not marked": nothing here read
  # the build's error events at all, so the delegate.Errored call was
  # unwitnessed, and the second row's last assertion was a verdict rather than
  # the absence the paragraph described. MEASURED, because a wrong "reddened
  # by" is worse than none: the table as it then stood, run against an adapter
  # built with delegate.Errored deleted, passed 2 of 2. The notice column is
  # the repair, and both paragraphs are now true of the table below them.
  Scenario Outline: Only a failure worth trying again is marked for retry — <case>
    Given a build of a job whose pipeline has the resource "some-resource"
    And <failure>
    When the step runs, with its failures classified for retry
    Then the step was refused, saying "<message>"
    And <verdict>
    And <notice>

    Examples:
      | case                             | failure                                           | message               | verdict                             | notice                                                                         |
      | the cluster could not be reached | the step fails with an unreachable Kubernetes API | connection refused    | the refusal is marked for retry     | the build log records an error mentioning "connection refused, will retry ..." |
      | the pipeline names nothing real  | the step fails with an unknown resource type      | unknown resource type | the refusal is not marked for retry | the build log records no error at all                                          |

  # The two rules meet here, and the abort wins. A build that a person
  # stopped must not be restarted by the platform's own judgement that the
  # failure looked transient — the failure did look transient, because the
  # cancellation is why the call failed. Retrying it would ignore the abort
  # and start the work again.
  #
  # The discriminator is the first row of the outline above: the same failure,
  # on a build that was not aborted, IS marked for retry.
  #
  # Reddened by: deleting the `select { case <-ctx.Done(): return }` guard at
  # the top of RetryErrorStep.Run.
  Scenario: A build somebody aborted is not turned into a retry
    Given a build of a job whose pipeline has the resource "some-resource"
    And the step fails with an unreachable Kubernetes API
    And the build has already been aborted
    When the step runs, with its failures classified for retry
    Then the step was refused, saying "connection refused"
    And the refusal is not marked for retry
    And the build log records no error at all

  # DISPOSITION — on_success, on_failure, on_error, ensure, try, in_parallel,
  # across and timeout are the same shape as the two combinators above: a
  # wrapper whose whole content is which of two steps it runs. They were left
  # in Go on purpose rather than overlooked. on_abort and the retry pair are
  # here because each of them has a rule an author gets wrong in production —
  # abort is not failure, and a transient error is not a broken pipeline —
  # and the rest do exactly what their names say.
