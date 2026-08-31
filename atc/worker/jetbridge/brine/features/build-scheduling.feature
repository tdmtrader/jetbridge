Feature: Which build runs, and what happens to one that is already running

  Two suites meet here, on either side of the moment a build starts.

  atc/scheduler decides which of a job's queued builds may run now. Everything
  it does is visible to whoever is watching the job page: a build that starts,
  a build that sits at "pending" and the reason, a build cancelled from the
  queue, a build that appears on its own because a resource produced a new
  version. atc/engine runs the build once it has started, and what it is FOR is
  the interruptions — a cancel, a web shutting down under it, a step that dies.

  Both suites already run against real PostgreSQL, so the usual payoff of this
  migration — replace the recording double, assert the round trip — was mostly
  collected before this file existed. Three doubles were left, and they are the
  three that decide everything the scheduler does: FakeAlgorithm, FakeBuildStarter
  and FakeBuildPlanner. Here all three are the real thing — `algorithm.New` over
  a real `db.VersionsDB`, `scheduler.NewBuildStarter`, and `builds.NewPlanner` —
  which is what lets the trigger scenarios below say not merely that a build
  appeared but which version it will get.

  The rest of what this file replaces is not a double at all: it is the states
  those suites reach by hand. Several get there through
  `job.SaveNextInputMapping(nil, false)`, or by handing the starter a
  `db.SchedulerResources{...}` literal, or by wrapping one build method in a
  decorator that returns `errors.New("bad thing")`. Those states are reachable
  only from a test. Every scenario below is reached from a pipeline, on a
  `db.SchedulerJob` read back through `JobFactory.JobsToScheduleByIDs`, so its
  Resources and ResourceTypes are the pipeline's rather than a literal's. Twice
  that discipline cost something and both times it was the scenario that was
  wrong: a build cannot be put into a serial job's "already running" state
  without a scheduling pass, because the admission query requires
  `inputs_determined`; and a hand-triggered build cannot reach "its inputs are
  unsatisfiable" without a check landing first, because it waits for one.

  Source: atc/scheduler/{buildstarter,scheduler,job_scheduling}_test.go and
  atc/engine/engine_test.go. Not atc/scheduler/algorithm, which is 91 table
  entries over version graphs and 13MB of real-world dumps; that is a Go table
  and it stays one. It is a COLLABORATOR here rather than a subject, never the
  thing being asserted about.

  # ==========================================================================
  # The queue: which build starts, and why the others do not
  # ==========================================================================

  # The positive control, and the row every "still queued" below is a
  # departure from. Two things have to land, and they land in different
  # places: the build has to be admitted and started, and the plan the job
  # describes has to reach the build's row — which is what a web that
  # restarted a second later reads to find out what it is supposed to be
  # running. A build marked started with no plan on it is a build nothing can
  # resume.
  #
  # Reddened by: the starter returning before `nextPendingBuild.Start(plan)`,
  # which leaves the build pending. Or by the planner's VisitTask no longer
  # naming the step — the build still starts, and only the last line fails.
  Scenario: A queued build starts, carrying the plan its job describes
    Given a job that runs a task
    And the build "first" was queued by the scheduler
    When the scheduler runs for this job
    Then the build "first" is running
    And the build "first" carries the plan it will run
    And the build "first" will run the task "some-task"
    And the scheduler is finished with this job for now

  # Cancelling a build that has not started yet only sets a flag; the row
  # stays pending until a scheduling pass reaches it. So the pass has two jobs
  # — finish it as cancelled, and carry on — and both are worth pinning,
  # because the tidy-looking mistake is to treat "this build is not startable"
  # as "stop scheduling this job", which would let one cancelled build wedge
  # a queue behind it indefinitely.
  #
  # The head-of-queue line is not decoration. "The one behind it still
  # started" says nothing unless the cancelled one really was in front.
  #
  # "Never even scheduled" is the line that pins the BRANCH rather than the
  # outcome. The first version of this scenario dropped it and concluded from
  # its own absence that the branch was unpinnable. It is not, and the source
  # says so: "finishes it as aborted without planning it"
  # (atc/scheduler/buildstarter_test.go) asserts four things and the fourth is
  # `IsScheduled()` false. `job.ScheduleBuild` sets `builds.scheduled`, and
  # `Finish` never clears the column — it writes status, end_time, completed,
  # private_plan, nonce and interceptible and leaves `scheduled` alone
  # (atc/db/build.go). So the two rows differ after all.
  #
  # Reddened by: the `IsAborted` branch finishing the build as anything other
  # than aborted — measured with BuildStatusErrored, which fails the "was
  # cancelled" line. And by its `continue` becoming `break`, which leaves
  # "next-in-line" pending.
  #
  # And, now, by deleting the whole `IsAborted` branch: measured by building
  # the file against an overlaid atc/scheduler/buildstarter.go with the
  # branch removed, which reddens exactly one line —
  #
  #   the build "cancelled-in-the-queue" was never even scheduled
  #     expected the builds the scheduler admitted not to include
  #     "cancelled-in-the-queue", but it does: [cancelled-in-the-queue
  #     next-in-line] (the job's builds: cancelled-in-the-queue=aborted,
  #     next-in-line=started)
  #
  # — while status, completion and plan all stay green, exactly as the
  # earlier finding said they would. `build.start` carries
  # `WHERE ... aborted = false`, so an aborted build cannot be started, and
  # the starter's `if !started { Finish(BuildStatusAborted) }` arm then
  # produces the same aborted, completed, unplanned row. What it cannot
  # reproduce is a row that was never admitted, and that is the difference
  # this line reads.
  Scenario: A build cancelled from the queue never runs, and does not hold up the one behind it
    Given a job that runs a task
    And the build "cancelled-in-the-queue" was triggered by hand
    And the build "next-in-line" was triggered by hand
    And the build "cancelled-in-the-queue" was cancelled before the scheduler reached it
    When the scheduler runs for this job
    Then the scheduler found "cancelled-in-the-queue" at the head of the queue
    And the build "cancelled-in-the-queue" was cancelled
    And the build "cancelled-in-the-queue" never got a plan
    And the build "cancelled-in-the-queue" was never even scheduled
    And the build "next-in-line" is running

  # `max_in_flight: 1` — a job that runs one build at a time. This is the case
  # an operator meets most often and understands best: a serial job with
  # something running, and a queue behind it that must stay in order and must
  # not be forgotten.
  #
  # Three claims, and each fails on its own. The blocked build stays queued
  # and unplanned. The build BEHIND it stays queued too, which is what makes
  # the job serial rather than merely rate-limited — if scheduling continued
  # past a build it could not admit, a later build would overtake it and run
  # out of order. And the scheduler says it will come back: runner.go leaves
  # `last_scheduled` where it is when that is true, so the job stays in the
  # set `JobsToSchedule` returns and is looked at again on the next tick. If
  # it said it was finished, this queue would sit until something else
  # happened to request a schedule for the job.
  #
  # The already-running build is also the sibling that stops this scenario
  # passing on an empty database: it is started before the pass and is still
  # started after it.
  #
  # Reddened by: the `!results.scheduled` branch no longer setting needsRetry,
  # which fails the last line alone. The two "still queued" lines are pinned
  # one layer down, by the `len(builds) >= j.maxInFlight` test in
  # db/job.go's isMaxInFlightReached — without it every build in the queue
  # starts at once, which is the whole of what `max_in_flight` buys.
  Scenario: A job that runs one build at a time holds the queue and asks to be run again
    Given a job that runs one build at a time
    And the build "already-running" is already running
    And the build "waiting" was triggered by hand
    And the build "behind-it" was triggered by hand
    When the scheduler runs for this job
    Then the scheduler found "waiting" at the head of the queue
    And the build "already-running" is running
    And the build "waiting" is still queued
    And the build "waiting" never got a plan
    And the build "behind-it" is still queued
    And the scheduler will come back to this job

  # The same visible outcome as the scenario above — a build sitting at
  # "pending" — for the opposite reason, and the difference is the whole point
  # of this pair. A build blocked by max-in-flight is waiting for something
  # that will change on its own, so the scheduler comes back. A build whose
  # inputs cannot be satisfied is waiting for a version that does not exist,
  # and re-running the same pass produces the same answer; the algorithm will
  # request a schedule itself when a check finds something. A scheduler that
  # asked to come back here would re-run the algorithm for this job on every
  # tick, for as long as the resource stays empty.
  #
  # The state is reached honestly: the resource HAS been checked — so the
  # build is entitled to decide — and the check found nothing. That is a
  # resource whose first check has come back empty, or one whose versions have
  # all been disabled.
  #
  # Reddened by: the `!inputsDetermined` branch setting needsRetry, which
  # fails the last line. And by that branch not returning at all — the starter
  # then plans a get step with no version, the planner refuses it, and the
  # build goes to errored instead of waiting, which fails the FIRST line.
  #
  # The first line only, which an earlier version of this comment got wrong:
  # it said that mutation reddened "the first two". Measured, against an
  # overlaid buildstarter.go with the `!inputsDetermined` return deleted, the
  # run reports one failure in this scenario:
  #
  #   the build "nothing-to-get" is still queued
  #     expected the builds still queued to include "nothing-to-get", found []
  #     (the job's builds: nothing-to-get=errored)
  #
  # WITH A FINDING about the middle line. `never got a plan` stays GREEN under
  # that mutation: the planner raises VersionNotProvidedError before
  # `nextPendingBuild.Start(plan)` is reached, so the build is finished as
  # errored with `public_plan` still `{}` — and `HasPlan()` is
  # `string(*b.publicPlan) != "{}"`. The last line stays green too: the
  # mutated branch returns `{finished:true}`, the loop `continue`s, and
  # needsRetry is never set.
  #
  # So the middle line is decorative HERE. It is carried by the serial-job and
  # queued-build scenarios, where a planned-and-started build is the thing
  # being distinguished from one that is not; in this scenario none of the 26
  # mutations listed at the foot of this file reddens it, with or without
  # "is still queued". It is kept because a reader comparing this scenario
  # with the serial one above needs the two rows described in the same terms,
  # not because it discriminates.
  Scenario: A build whose inputs cannot be satisfied waits without making the scheduler spin
    Given a job that gets a resource which has never produced a version
    And the build "nothing-to-get" was triggered by hand
    And the resource has been checked since
    When the scheduler runs for this job
    Then the build "nothing-to-get" is still queued
    And the build "nothing-to-get" never got a plan
    And the scheduler is finished with this job for now

  # Pressing "+" on a job does not mean "run against whatever versions the
  # last check happened to leave behind". A hand-triggered build waits until
  # every resource it gets has been checked SINCE the build was created, and
  # only then picks its versions — otherwise triggering a build by hand
  # immediately after pushing would run it against the code you just replaced.
  #
  # Both rows have a version in the database, and the same one. The only thing
  # that differs is whether a check has landed since the build was created,
  # and that alone decides whether the build runs now or waits.
  #
  # Reddened by: manualTriggerBuild.IsReadyToDetermineInputs returning
  # `true, nil` instead of `m.ResourcesChecked()` — the first row then starts
  # the build instead of waiting for the check. Its retry line is pinned
  # separately, by the blocked-build branch of the starter no longer setting
  # needsRetry, which reddens this row and the serial-job scenario together.
  Scenario Outline: A build triggered by hand waits for its resources to be checked — <case>
    Given a job that gets a resource, and a version of it already exists
    And the build "by-hand" was triggered by hand
    And the resource <check>
    When the scheduler runs for this job
    Then the build "by-hand" is <fate>
    And <retry>

    Examples:
      | case                     | check                      | fate         | retry                                           |
      | no check has landed yet  | has not been checked since | still queued | the scheduler will come back to this job        |
      | a check has landed since | has been checked since     | running      | the scheduler is finished with this job for now |

  # A rerun copies its original's inputs. A build that failed before its
  # inputs were ever resolved has none to copy, so its rerun can never
  # determine what to run — and unlike every other reason a build cannot
  # start, this one will never resolve itself no matter how long the queue
  # waits. Which is exactly why it must not block: a permanently stuck rerun
  # at the head of the queue would otherwise stop the job for good, and the
  # operator's only clue would be a build that says "pending".
  #
  # The queue order is the fixture's most fragile assumption here — pending
  # builds sort by `COALESCE(rerun_of, id)`, so the rerun sorts to its
  # ORIGINAL's position and lands in front of the build created after it,
  # which is the opposite of the order these lines are written in. The head-of
  # -queue line makes that visible instead of assumed.
  #
  # Reddened by: the `RerunOf() != 0` arm of the inputs-undetermined branch
  # `continue`-ing becoming a `break` — "waiting-behind-it" then stays queued
  # for ever alongside the rerun.
  Scenario: A rerun that can never resolve its inputs does not block the queue behind it
    Given a job that runs a task
    And the build "an-old-failure" failed before its inputs were ever resolved
    And the build "waiting-behind-it" was queued by the scheduler
    And the build "the-rerun" is a rerun of "an-old-failure"
    When the scheduler runs for this job
    Then the scheduler found "the-rerun" at the head of the queue
    And the build "the-rerun" is still queued
    And the build "waiting-behind-it" is running
    And the scheduler is finished with this job for now

  # DISPOSITION — "marks the build as errored when the plan cannot be created"
  # is not here, and the reason is worth writing down because it is the one
  # place this file gave something up.
  #
  # The behaviour is real and worth having: a build the planner refuses must
  # be finished as errored, not left pending, or it wedges the job silently.
  # But the ginkgo case reaches it by calling `SaveNextInputMapping(nil, true)`
  # — inputs declared SATISFIED while naming no version — and then getting a
  # resource, so the planner raises VersionNotProvidedError. Under the real
  # algorithm that state does not occur: a resolution that names no version
  # for a get is not reported as resolved. Every other planner refusal
  # (UnknownResourceError, UnknownPrototypeError) needs a job config that
  # `atc/configvalidate` rejects at set-pipeline time, or a
  # `db.SchedulerResources` literal that disagrees with the pipeline the job
  # is in.
  #
  # So a scenario for it would have to construct, by hand, a database state no
  # pipeline produces — which is failure mode 3 of this programme: the Given
  # would be enforcing the invariant, and deleting the production guard would
  # change nothing. It stays in Go, where the fixture is visible as a fixture.
  #
  # The mutation run bears this out from the other side. Removing the
  # `!inputsDetermined` guard from the starter makes the unsatisfiable-inputs
  # scenario above go red with `nothing-to-get=errored` — the errored-on-an-
  # unplannable-build path really does work, and it is reachable only by
  # breaking the guard that normally stops the starter from getting there.

  # ==========================================================================
  # Triggering: when a new version makes a build on its own
  # ==========================================================================

  # The line a pipeline author writes, and what it does. One word — `trigger`
  # — separates this scenario from the next one; the pipelines are otherwise
  # identical, the resource is the same resource and the version is the same
  # version.
  #
  # The version assertion is the second half and does not follow from the
  # first: a build that runs against the wrong version is worse than no build.
  # It is only sayable because the algorithm here is the real one — the
  # ginkgo case handed the scheduler a fake whose Compute returned a mapping
  # the test wrote, so it could assert that a build appeared but not what the
  # build would get.
  #
  # Reddened by: dropping `EnsurePendingBuildExists` from the trigger branch —
  # the job ends with no builds at all.
  Scenario: A new version of a resource the job triggers on starts a build
    Given a job that gets "code" on every new version
    And a new version "v1" of "code" appears
    When the scheduler runs for this job
    Then the job has exactly one build
    And the build the scheduler queued is running
    And the build the scheduler queued will get "code" at version "v1"

  # ...and the same version arriving for an input the job does NOT trigger on.
  # Nothing runs — which is the whole contract of `trigger: false`, and a
  # scheduler that ignored the flag would start a build for every commit to
  # every resource any job mentions.
  #
  # "No build was queued" is an absence and would pass on a database where the
  # scheduler never ran at all, so it does not stand alone: the job is flagged
  # as having new inputs in the same pass, and that flag is what puts the
  # orange dot on the job in the UI telling the author there is something new
  # they could run by hand. If the pass had not happened, the flag would be
  # unset and this scenario fails on its second line.
  #
  # Reddened by: hoisting EnsurePendingBuildExists out of the
  # `if inputConfig.Trigger` — the count goes to 1. Or by dropping
  # SetHasNewInputs, which fails the flag.
  Scenario: A new version of an input the job does not trigger on flags the job but starts nothing
    Given a job that gets "code" but is not triggered by it
    And a new version "v1" of "code" appears
    When the scheduler runs for this job
    Then the job has no builds at all
    And the job is flagged as having new inputs

  # A version is only new until something has used it. The first pass here
  # queues a build and flags the job; the build adopts the version as its
  # input, which is what makes the version stop being a first occurrence. The
  # second pass must then notice, clear the flag, and — just as importantly —
  # not queue a second build for a version that has already been built.
  #
  # A scheduler that never cleared the flag would leave the "new inputs"
  # marker on for ever, and it is the marker that tells an author whether
  # anything is waiting; permanently on, it says nothing. A scheduler that
  # treated an already-used version as new again would build the same commit
  # on every tick.
  #
  # The build count is the sibling that keeps the SECOND pass honest: 1 means
  # the first pass really did run and queue, so the second pass had a used
  # version to look at rather than an empty database.
  #
  # It does not keep the FLAG assertion honest, which is why the Given now
  # states the precondition outright. `EnsurePendingBuildExists` and
  # `SetHasNewInputs` are separate writes in the same function
  # (scheduler.go's ensurePendingBuildExists), so a build count of 1 is
  # compatible with the flag never having been written at all — and
  # "no longer flagged" reads `HasNewInputs()==false`, which is exactly what a
  # job the flag was never set for looks like.
  #
  # Measured twice, against an atc/scheduler/scheduler.go overlaid with the
  # `job.SetHasNewInputs(hasNewInputs)` call deleted. With the Given line
  # neutered — the scenario as it was first written — the whole scenario is
  # GREEN under that mutation: the build is queued, the flag reads false
  # because it was never set, and both Then lines pass. With the Given line in
  # place the scenario reddens on it:
  #
  #   that pass flagged the job as having new inputs
  #     the first pass left the job's new-inputs flag unset, so there is
  #     nothing for the second pass to clear and the scenario would pass on a
  #     scheduler that never writes the flag at all
  #
  # So the line is not a restatement of the trigger:false scenario's flag
  # assertion — it is what stops THIS scenario reporting coverage it does not
  # have.
  #
  # Reddened by: `if hasNewInputs != job.HasNewInputs()` becoming
  # `if hasNewInputs`, which never clears. Or by the FirstOccurrence check
  # dropping out of the trigger branch, which queues a second build. Or, on
  # the Given line, by SetHasNewInputs never being written.
  Scenario: A version stops being new once a build has used it
    Given a job that gets "code" on every new version
    And a new version "v1" of "code" appears
    And the scheduler has already run once for this job
    And that pass flagged the job as having new inputs
    When the scheduler runs for this job
    Then the job has exactly one build
    And the job is no longer flagged as having new inputs

  # ==========================================================================
  # A build that is already running
  # ==========================================================================
  #
  # The engine runs a build's plan and decides what its ending means. The step
  # below is a working leaf whose behaviour each scenario writes — block until
  # interrupted, fail, panic — wrapped in production's own `exec.LogError`
  # exactly as `coreStepFactory.LoadVarStep` wraps the real load_var step.
  # Nothing asks it what it was called with. Every assertion reads the build's
  # row or the build's log, which are what the web and the operator read.

  # `fly abort-build`, or the cancel button. The abort travels the way it does
  # in production — a second handle on the build marks it aborted, PostgreSQL
  # notifies, and the engine's listener cancels the context the step is
  # running under. Nothing in the scenario touches that context.
  #
  # Two independent witnesses, which is why both lines are here. The status
  # is the engine's own conclusion, from `errors.Is(err, context.Canceled)`.
  # The log line is one layer down: `exec.LogError` writes "interrupted" only
  # for a cancelled context, so it says the step really was cancelled rather
  # than that the engine decided it had been.
  #
  # A step that never noticed the cancellation returns successfully after ten
  # seconds instead of hanging, so losing the cancel produces a FAILURE
  # ("expected aborted, got succeeded") rather than a timeout.
  #
  # Reddened by: deleting `cancel()` from the abort goroutine in Run — the
  # step then falls through its ten-second arm, returns successfully, and the
  # build is recorded as SUCCEEDED, which is the failure this shape was
  # designed to produce instead of a hang. Or by the
  # `errors.Is(err, context.Canceled)` arm of finish(), which errors it
  # instead and tells an operator who cancelled a build that it broke.
  #
  # The log line is reddened one layer down, in exec.LogError: measured by
  # replacing AbortedLogMessage, and again by dropping the
  # `delegate.Errored` call outright. That is the layer that owns it, and
  # naming the engine here instead would be a claim this file cannot make.
  Scenario: A build cancelled while it is running stops its step and ends as cancelled
    Given a build that is running a step
    When the build is cancelled while its step is still running
    Then the build finished as "aborted"
    And the finished build's log explains "interrupted"

  # A web being restarted must not take running builds down with it. The
  # engine stops tracking the build and returns, and the row is left at
  # "started" so the next web's build tracker picks it up and re-attaches.
  # Finishing it here instead is the failure this repository has already had
  # in production: every in-flight build went red on a rolling restart.
  #
  # "Still recorded as running" is a positive read of the row, not an absence:
  # a build that had been finished, deleted or never started all fail it.
  #
  # Reddened by: the drain branch calling finish() for job builds as well as
  # check builds — a deploy then errors every build in flight.
  Scenario: A web shutting down leaves a job build running for the next web
    Given a build that is running a step
    When the web shuts down while the step is still running
    Then the build is still recorded as running

  # The exception, and the reason it is an exception. A check build tracks
  # itself as in-flight; a resource with a check that never completes is a
  # resource that is never checked again, and nothing in the UI says why. So
  # the drain finishes check builds even though it leaves job builds alone,
  # and it puts the reason in the log rather than only in the web's process
  # output, where nobody watching the resource will ever see it.
  #
  # Reddened by: removing the `b.build.Name() == db.CheckBuildName` test from
  # the release branch — the check is then left "started" like a job build and
  # the resource wedges, which fails both lines at once because a running
  # build has no finished log to read. And, for the log line alone, by
  # deleting the `b.buildStepErrored(logger, message)` call from finish().
  #
  # WITH A FINDING about what does NOT pin the log line here. The
  # `if !fromRunningStep || b.build.Name() == db.CheckBuildName` test that
  # guards that call is not what this scenario reads, even though the panic
  # scenario below reads exactly that test. The drain branch calls
  # `b.finish(..., false, false)`, so `fromRunningStep` is false AND the build
  # is named `check`: both disjuncts are true, and the emit survives any
  # single-clause edit. Measured, three overlays on atc/engine/engine.go —
  # dropping `!fromRunningStep ||`, dropping `|| b.build.Name() ==
  # db.CheckBuildName`, and replacing the whole condition with `true` — leave
  # this scenario GREEN in all three runs. Only removing the emit reddens it:
  #
  #   the finished build's log explains "build released during drain"
  #     expected the build log to mention "build released during drain",
  #     got "status: started | status: errored"
  #
  # Nothing wrapped this error — it was raised by the engine itself, not
  # returned by a step — so finish() is still the only thing that can put it
  # in the log; it is the guard, not the emit, that this scenario leaves
  # unpinned. The panic scenario below pins the `!fromRunningStep` half, where
  # CheckBuildName is false and the disjunction really does turn on it.
  Scenario: A check dropped by a web shutting down is ended, and says why
    Given a resource check that is running
    When the web shuts down while the step is still running
    Then the build finished as "errored"
    And the finished build's log explains "build released during drain"

  # A step that fails for an ordinary reason ends the build in error. Not
  # "failed" — a failed build is one whose steps ran and said no, and the
  # difference is what a pipeline author looks at first when deciding whether
  # to investigate their code or the cluster.
  #
  # Reddened by: the `else if err != nil` arm of finish() falling through to
  # the failed branch, which fails the status line. The log line belongs to
  # exec.LogError, exactly as in the cancellation scenario above: measured by
  # dropping its `delegate.Errored` call, which empties the log while leaving
  # the status right.
  Scenario: A step that fails ends the build in error, and the log says why
    Given a build that is running a step
    When the step fails, saying "the worker went away"
    Then the build finished as "errored"
    And the finished build's log explains "the worker went away"

  # ...and the failure the engine deliberately does NOT conclude anything
  # about. A step whose worker disappeared is retriable: the engine returns
  # without finishing, and the build tracker runs it again. Ending it here
  # instead would turn every reclaimed spot node into a red build.
  #
  # This is the one scenario where the build being left at "started" is the
  # desired outcome rather than an accident, which is why it sits next to the
  # one above: the same shape of failure, one classification apart.
  #
  # And it is the one scenario in the file whose only assertion is a row that
  # did NOT change — which is the state the Given already put the row in, via
  # `build.Start(plan)`. So it needs a second line saying the engine got as
  # far as the step, and that line is not decoration: `engineBuild.Run` has
  # four early returns before the step is ever built (tracking lock not
  # acquired, Reload error, build not found, build not running), and every one
  # of them leaves the row at "started" too. Without the second line this
  # scenario is green on all four.
  #
  # Measured: with `if !b.build.IsRunning()` inverted to `if
  # b.build.IsRunning()` (overlaid on atc/engine/engine.go, so the engine
  # returns before building the step) the run goes 10/16 — and IN THIS
  # SCENARIO "still recorded as running" stays GREEN, with only the step line
  # red —
  #
  #   the build's step really ran
  #     the engine returned without ever running the build's step, so the
  #     build being left at "started" says nothing about how a retriable
  #     failure is classified — it is the state the build was already in
  #
  # That mutation is deliberately blunt and takes five other scenarios with
  # it; the point is not its blast radius but that this scenario was inside it
  # and did not notice.
  #
  # Reddened by: dropping the `errors.As(runErr, &exec.Retriable{})` test on
  # the done path, which fails the first line.
  Scenario: A step whose worker disappeared leaves the build to be retried
    Given a build that is running a step
    When the step fails because the worker running it went away
    Then the build is still recorded as running
    And the build's step really ran

  # A panic is the case nothing else covers, because it is the one error that
  # never passed through a step's own error handling — so if the engine does
  # not surface it, the build shows an "errored" badge with an empty log and
  # the reason exists only in the web's process output — which is exactly what
  # was measured when the emitting branch was removed. And the recover itself
  # is load-bearing beyond this one build: the plan runs on its own goroutine,
  # so a panic that got out would take the whole web down with it rather than
  # failing one build.
  #
  # Reddened by: the recover branch not setting runErr, which finishes the
  # build as failed rather than errored. Or by the `!fromRunningStep` test in
  # finish() — the panic flag is what makes it true here, and removing the
  # test empties the log while leaving the status right. This is one of only
  # two scenarios in the file whose log line the ENGINE owns; the other is
  # the drained check above, and both are the paths no step wrapper ever
  # saw.
  Scenario: A step that panics ends the build in error rather than leaving it running
    Given a build that is running a step
    When the step panics
    Then the build finished as "errored"
    And the finished build's log explains "panic in running build plan"

  # DISPOSITION — "does not double-emit the error event" on a job build. The
  # ginkgo case asserts the engine's finish() adds no error event when the
  # step already reported one, and it demonstrates this by counting events on
  # a build whose step was a bare fake with no `exec.LogError` around it — so
  # what it really measures is that finish() emitted nothing, with the wrapper
  # half asserted in a comment. Stated as a count over the whole log here it
  # would be a claim about `exec.LogError`'s wrapping rather than about the
  # engine, and the count differs by which leaf the plan reaches. The engine's
  # own contribution to that rule — the branch that DOES emit, for a panic and
  # for a check build — is the two scenarios above.

  # DISPOSITION — "does not build the step" for a build that already finished,
  # was deleted, or whose tracking lock is held by another web. The behaviour
  # matters (two webs must not run one build's steps twice), but every
  # available assertion for it is either a call count on the stepper factory,
  # which this programme does not write, or the absence of a change to a row
  # that nothing was going to change anyway. Left in Go.

  # DISPOSITION — the error-path cases of both schedulers: "returns the error
  # when the pending builds cannot be fetched", "…when scheduling the build
  # fails", "…when saving the next input mapping fails", and a dozen siblings.
  # Each wraps a build or a job in a decorator whose one method returns
  # `errors.New("bad thing")`, and asserts the wrapping of that sentinel. The
  # gc file could migrate its equivalents because a closed PostgreSQL
  # connection produces them all at once; here the failures are per-method on
  # a job that must otherwise keep working, and a closed connection takes the
  # whole job with it. They stay in Go, where the decorator is visible.

  # ==========================================================================
  # What was measured
  # ==========================================================================
  #
  # 15 scenarios, one of them an Outline with two rows, so 16
  # template-database clones.
  # Whole file: 9.8 s, against ~3.9 s of fixed cost for the postmaster, which
  # puts the marginal cost of a scenario here at roughly 370 ms — three times
  # what gc-reclamation measured, because most of these scenarios save a
  # pipeline and several run a real algorithm pass over a real VersionsDB.
  #
  # Every scenario was reddened by a change to production and the change put
  # back. 26 mutations, each run against the whole file so the greens are
  # evidence too. Every one is reproducible without editing the tree, by
  # building the adapter against an overlay:
  #
  #   go build -overlay /tmp/ov.json -o .build/brine-adapter-jetbridge \
  #     ./cmd/brine-adapter-jetbridge
  #   brine run features/build-scheduling.feature --mode sync
  #
  #   M1a  IsAborted branch deleted outright        -> 1 red (see below)
  #   M1b  IsAborted branch finishes as errored     -> 1 red
  #   M2   finished-build `continue` -> `break`     -> 1 red
  #   M3   blocked build stops asking to come back  -> 2 red (serial, outline)
  #   M4   undetermined inputs set needsRetry       -> 1 red
  #   M5   manual build skips ResourcesChecked      -> 1 red
  #   M6   stuck rerun `continue` -> `break`        -> 1 red
  #   M7   `if inputConfig.Trigger` ignored         -> 1 red
  #   M8   EnsurePendingBuildExists never called    -> 2 red
  #   M9   new-inputs flag never cleared            -> 1 red
  #   M9b  new-inputs flag never written            -> 2 red
  #   M10  the starter never calls Start            -> 6 red
  #   M11  the planner renames the task             -> 1 red
  #   M12  an abort no longer cancels the step      -> 1 red
  #   M13  a drain finishes job builds too          -> 1 red
  #   M14  a drain leaves check builds running      -> 1 red
  #   M15  a retriable failure ends the build       -> 1 red
  #   M16  a panic no longer errors the build       -> 1 red
  #   M17  finish() never writes to the log         -> 2 red
  #   M18  exec.LogError writes nothing             -> 2 red
  #   M19  AbortedLogMessage replaced               -> 1 red
  #   M20  a build with no inputs is planned anyway -> 2 red
  #   M21  max_in_flight stops holding the queue    -> 1 red
  #   M22  finish(): `!fromRunningStep ||` dropped  -> 1 red (panic only)
  #   M23  finish(): `|| CheckBuildName` dropped    -> 0 red
  #   M24  finish()'s emit guard replaced by `true` -> 0 red
  #   M25  engine returns before building the step  -> 6 red
  #
  # M23 and M24 are the two that found nothing, and they are recorded rather
  # than discarded because a comment in this file used to claim one of them
  # reddened the drained-check scenario. It does not: on that path both
  # disjuncts of the guard are true, so no single-clause edit to it changes
  # what the build log says. The scenario is written against the emit, not the
  # guard, and now says so.
  #
  # M1a and M25 are the two that DID find something after this file was first
  # written, and each of them found the same kind of hole: a scenario whose
  # assertions were all satisfied by a row the mutation left unchanged.
  # Deleting the `IsAborted` branch leaves an aborted, completed, unplanned
  # build — identical on every column the scenario read except `scheduled`.
  # Returning from `engineBuild.Run` before the step is built leaves a build
  # at `started` — which was the retriable scenario's entire assertion. Both
  # are now pinned by one added line, and both lines are quoted with the
  # failure they produce beside the scenario they belong to.

  # ==========================================================================
  # What this file does not reach
  # ==========================================================================
  #
  # atc/scheduler/runner_test.go — the scheduling LOCK and the concurrency
  # guard around it. Its subject is that two webs do not schedule one job at
  # the same time, and every way to state that is either a call count or a
  # timing arrangement; the ginkgo suite needs a wrapper around
  # AcquireSchedulingLock just to know when a pass finished. Left in Go.
  #
  # atc/scheduler/metrics_test.go and the engine's own metric emission —
  # counters read by an operator through Prometheus, not through a build.
  # A Gherkin sentence about a counter increment is a call count wearing a
  # sentence.
  #
  # atc/engine/{build_step_delegate,task_delegate,check_delegate,get_delegate,
  # put_delegate,set_pipeline_delegate,builder}_test.go — 4,300 lines, and the
  # largest single thing in these two packages. They are a different subject
  # from this file: what a step is allowed to do to its build while it runs
  # (write output, fetch an image, ask the policy agent, record a version).
  # Nothing here touches them, and this file makes none of them deletable.
