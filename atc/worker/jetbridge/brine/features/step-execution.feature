Feature: What each kind of step promises

  A pipeline is written in steps, and each kind of step makes a promise its
  author relies on: a get fetches the version it was told to, a put publishes
  what it created and nothing when it did not, set_pipeline does not undo
  somebody else's newer change, a task says which artifact it could not find,
  and a retry stops when it has succeeded while an abort stops now.

  Source: atc/exec — get_step_test.go, put_step_test.go,
  set_pipeline_step_test.go, task_step_test.go, retry_step_test.go,
  retry_error_step_test.go and on_abort_test.go.

  These cases assert build-visible outcomes through real PostgreSQL and engine
  delegates. Pipeline artifact reads, task preflight and retry classification
  use real dependencies. Live resource gets, publication and retries are grouped
  under features/live, including the explicitly approved partial-output put fault.

  The real retry cases distinguish an interrupted API route from a missing
  required input, and ensure cancellation prevents retry. An unmapped resource
  type is an image name in the Kubernetes worker, not the fabricated "unknown
  resource type" error the previous fixture supplied.

  Mutation evidence and migration history are recorded in V5-MIGRATION.md.
  Dispositions below retain Go assertions not established by these scenarios.

  # ==========================================================================
  # Getting a version
  # ==========================================================================

  # A get step fetches the version its plan pinned
  # now runs against an actual Git HTTP repository in live/get-step.feature.


  # The version a put created is published, and is the one the get after it fetches
  # now uses the actual time resource in live/time-resource.feature.


  # A cached get reads a real volume associated with its version and worker in
  # PostgreSQL. A separate producer build owns the original cache; the real
  # artifact daemon serves its bytes through a published EndpointSlice.
  #
  # The existing provenance assertion checks both fromCache and the returned
  # artifact's actual version file, and requires no resource pod to exist.
  # Cache lookup and bytes are independently verified before the get runs.
  #
  # Pin loss or bypassing the cache makes the get miss this association and
  # attempt a resource pod. Local envtest does not execute pods, so that path
  # cannot succeed and is bounded by a deadline. This proves cache reuse, not
  # resource-script execution or producer publication.
  #
  # Paired mutations preserve pin/provenance/refetch sensitivity. Registering a
  # nil artifact used to pass; the real returned-artifact read now rejects it.
  Scenario: A cache the worker already holds is served without running the resource script again
    Given a build of a job whose pipeline has the resource "some-resource"
    And the chosen worker already holds a cache of version "v2"
    When the get step runs, pinned to version "v2"
    Then the step succeeded
    And the build log mentions "found existing resource cache"
    And the build fetched version "v2"
    And the build's artifact "some-resource" came from a cache on the worker

  # A get the resource refuses is a failed build, and hands nothing downstream
  # now runs against an actual Git HTTP repository in live/get-step.feature.


  # The get timeout now runs against a physically stalled Git HTTP server in
  # live/get-step.feature, with a real resource process observed before expiry.


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
  # The version-then-exit-4 contract now runs with the explicitly approved
  # executable fault in features/live/partial-put.feature.

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

  # A retried step stops at the first attempt that succeeds
  # now uses the actual time resource in live/time-resource.feature.


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
      | case                             | failure                                                         | message                           | verdict                             | notice                                                                         |
      | the cluster could not be reached | the step fails with an unreachable Kubernetes API               | connection refused                | the refusal is marked for retry     | the build log records an error mentioning "connection refused, will retry ..." |
      | the pipeline names nothing real  | the step requires an input artifact that was never produced      | input not found: missing-artifact | the refusal is not marked for retry | the build log records no error at all                                          |

  # The two rules meet here, and the abort wins. A build that a person
  # stopped must not be restarted by the platform's own judgement that the
  # failure looked transient. Here the real API request hits connection
  # refusal, and the build is aborted before that failure is classified.
  # Retrying it would ignore the abort and start the work again.
  #
  # The discriminator is the first row of the outline above: the same failure,
  # on a build that was not aborted, IS marked for retry.
  #
  # Reddened by: deleting the `select { case <-ctx.Done(): return }` guard at
  # the top of RetryErrorStep.Run.
  Scenario: A build somebody aborted is not turned into a retry
    Given a build of a job whose pipeline has the resource "some-resource"
    And the step fails with an unreachable Kubernetes API
    And the build is aborted as the API request fails
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
