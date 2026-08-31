Feature: Reclaiming pipelines, build logs, and the right to intercept a build

  Three collectors, and one sentence between them: a sweep runs, and afterwards
  this row is still here and that row is gone. As in gc-reclamation.feature
  there is no call count anywhere in this file — but unlike the daemon suites
  there was no double to replace either. atc/gc already runs against real
  PostgreSQL, so nothing here is bought by de-faking, and every scenario has to
  earn its place on the sentence alone.

  What each collector costs an operator when it goes wrong:

    The pipeline collector archives a child pipeline whose parent is gone. Set
    a pipeline from a build and then delete the set_pipeline step, and the
    child is left scheduling builds nobody asked for. Archive too eagerly and
    you take down pipelines that are perfectly healthy — including, if one
    predicate goes, every pipeline in the deployment at once.

    The build log collector deletes build events. It is the only thing keeping
    `build_events` from growing without bound, and it is also the only thing
    that can destroy a build log a human still wants. Both directions are
    permanent.

    The build collector retires finished builds from interception. Until a
    build is marked non-interceptible its containers are held for `fly
    intercept`, so a collector that stops running leaks containers on every
    worker, and one that runs too widely cuts an operator off from a build
    that is still going.

  Source: atc/gc/pipeline_collector_test.go (2 specs) and
  atc/gc/build_log_collector_test.go (18 DescribeTable entries + 7 Its). All 27
  are accounted for below — 25 migrated, 2 disposed of with a reason. Two
  scenarios are not migrations. The third named source,
  atc/gc/build_collector_test.go, DOES NOT EXIST and never has, so the build
  collector has no test in atc/gc at all and the scenario at the foot of this
  file is new coverage. The batch-boundary scenario is the other one, and it is
  neither new nor a migration: it puts back a boundary three ginkgo entries
  crossed only because they were long, and which the shorter fixtures that
  replaced them stopped crossing. None of the sources carried requirement
  identifiers, so there are no tags.

  A note on the two reading conventions used throughout:

    "the next sweep will start from the build X" is `jobs.first_logged_build_id`,
    the cursor the collector resumes from. Every fixture below starts that
    cursor on the job's oldest build, which is where an earlier sweep would
    have left it, so the collector's monotonic guard is under test rather than
    trivially satisfied by a zero.

    "has been reaped" is an absence, and an absence passes on an empty table.
    It is not asserted as one here: the check derives the set of builds THIS
    SCENARIO CREATED whose events are now gone, and asks whether the named
    build is in it. A build the fixture never made is in neither set, so a
    fixture that quietly stopped inserting fails both the reaped check and the
    survived one instead of passing on the vacuum. The creating step also
    demands the build have at least one event before the sweep, which is the
    precondition the ginkgo source spells out at line 522.

  # ==========================================================================
  # Pipelines whose parent has gone away
  # ==========================================================================

  # Both ginkgo specs, plus a third case neither of them had, in one database —
  # and the point of one database is that the three rows discriminate each
  # other. A collector that archives nothing passes the two survivors and fails
  # the orphan; a collector that archives everything fails the two survivors.
  # Split across three scenarios, each would have been alone with its own
  # verdict.
  #
  # "standalone" is the case the ginkgo suite did not have and the one with the
  # worst failure: a pipeline saved directly by a team has no parent job at
  # all, and ArchiveAbandonedPipelines excludes it with a single
  # `parent_job_id IS NOT NULL`. Drop that predicate and the LEFT JOIN leaves
  # j.id NULL, which the query already reads as "the parent is gone" — so every
  # ordinary pipeline in the deployment is archived and paused on the next
  # sweep. That is the whole installation, from one line.
  #
  # Reddened by, both measured: pipelineCollector.Run returning nil without
  # calling ArchiveAbandonedPipelines, which fails on the orphan. And, one
  # layer down in atc/db/pipeline_lifecycle.go, dropping the
  # `p.parent_job_id` predicate — which archives "standalone", "healthy-parent"
  # and every other pipeline whose parent job does not exist, and leaves
  # "healthy-child" as the only active pipeline in the database.
  Scenario: A child pipeline is archived when the parent that set it is archived, and only then
    Given a collector for pipelines whose parent has gone away
    And the pipeline "healthy-parent" was set directly by its team
    And the pipeline "doomed-parent" was set directly by its team
    And the pipeline "standalone" was set directly by its team
    And the pipeline "healthy-child" was set by a build of the pipeline "healthy-parent"
    And the pipeline "orphaned-child" was set by a build of the pipeline "doomed-parent"
    And the pipeline "doomed-parent" is archived
    When the collector sweeps for pipelines whose parent is gone
    Then the pipeline sweep completed without error
    And the pipeline "orphaned-child" has been archived
    And the pipeline "healthy-child" is still active
    And the pipeline "standalone" is still active
    And the pipeline "healthy-parent" is still active

  # DISPOSITION — the pipeline collector has no failure scenario here. Its
  # whole body is one lifecycle call and a `return err`, and the sentence for
  # swallowing that error ("a collector that reports success on a failed sweep
  # is worse than one that never ran") is already written, against a real
  # closed connection, in gc-reclamation.feature. The ginkgo suite has no such
  # spec either. A third copy of that sentence would not say anything new; the
  # build log collector below gets one because there the interesting part is
  # WHICH failures abort the sweep and which are survived.

  # ==========================================================================
  # How many build logs a job keeps
  # ==========================================================================

  # The retention policy in one outline. Two finished builds, one two days old
  # and one under a day, and every row is a different rule deciding their fate.
  # The rows are not variations on a number: `Builds` and `Days` are separate
  # arms of reapLogsOfJob with separate `continue`s, and a row that reaps under
  # one arm and not the other is the only thing that tells them apart.
  #
  # The cursor column is the second half of every row. A sweep that deletes the
  # right logs but leaves the cursor behind re-reads them forever; one that
  # advances it too far can never reach the builds it skipped.
  #
  # Reddened by, each measured against the whole file so the split is a fact
  # rather than a claim: making `maxBuildsRetained` an off-by-one
  # (`retainedBuilds > logRetention.Builds`) reddens rows 1 and 4 — the two
  # whose reap comes from the count arm — and not rows 2, 3 or 5. Disabling the
  # `logRetention.Days` arm reddens rows 2 and 3, and neither of the others.
  # Deleting the `logRetention.Builds == 0 && logRetention.Days == 0` early
  # return reddens only row 5: with no rule at all, both builds fall through to
  # the reap.
  Scenario Outline: Two builds, one over two days old and one under a day, against a job that keeps <policy>
    Given a job that keeps <policy>
    And the build "older" failed 49 hours ago
    And the build "newer" failed 23 hours ago
    When the build log collector sweeps
    Then the log sweep completed without error
    And the log of the build "older" <older fate>
    And the log of the build "newer" survived the sweep
    And the next sweep will start from the build "<cursor>"

    Examples:
      | policy                                                                  | older fate         | cursor |
      | only its most recent build                                              | has been reaped    | newer  |
      | any build finished in the last day                                      | has been reaped    | newer  |
      | any build finished in the last 3 days                                   | survived the sweep | older  |
      | only its most recent build, plus anything finished in the last 2 days   | has been reaped    | newer  |
      | no build logs at all                                                    | survived the sweep | older  |

  # The count arm is the one that saves this build, and this is the only angle
  # from which that is visible. The job's single build is well past the age
  # limit, so the age rule alone would reap it — leaving a job whose most
  # recent build has no log, which is the first thing anyone opens when a
  # pipeline breaks. Under a combined policy the two rules are alternatives,
  # not conditions, and it is the count arm's `continue` that makes them so.
  #
  # Reddened by: making the count arm defer to the age limit —
  # `if !maxBuildsRetained && (logRetention.Days == 0 || !buildHasExpired)`,
  # which is what "keep 1 build AND anything under 2 days" reads like if you
  # take the two rules as a conjunction. Measured: it reddens this scenario and
  # nothing else in the file, because this is the only job whose newest build
  # is also past the age limit.
  #
  # Note what does NOT redden it, which was the first answer written here and
  # was wrong: moving the `logRetention.Days` block above the
  # `logRetention.Builds` block. The age arm only reaps by FALLING THROUGH, so
  # running it first still leaves the count arm to catch the build on the way
  # past. Order is not what protects this build; the count arm's `continue` is.
  Scenario: The most recent build keeps its log even after the age limit has passed
    Given a job that keeps only its most recent build, plus anything finished in the last 2 days
    And the build "only" failed 49 hours ago
    When the build log collector sweeps
    Then the log sweep completed without error
    And the log of the build "only" survived the sweep
    And the next sweep will start from the build "only"

  # ...and the same policy from the other side. Here the count arm is spent on
  # "newer" and cannot save "older" — but "older" finished a day inside the age
  # limit, so the age arm keeps it. Two rules, either sufficient. The row in the
  # outline above with this policy is the case where NEITHER covers the older
  # build; this is the case where the second one does, and the difference is a
  # build log kept versus deleted.
  #
  # Reddened by: disabling the `logRetention.Days` arm — which is what "the
  # count is the real rule and the age limit is a secondary cap" would amount
  # to, and would silently delete every build the count arm ran out of room
  # for. Measured: it reddens this scenario and rows 2 and 3 of the outline
  # above, and nothing else.
  Scenario: A build the count rule has no room for is still kept while it is inside the age limit
    Given a job that keeps only its most recent build, plus anything finished in the last 2 days
    And the build "older" failed 24 hours ago
    And the build "newer" failed 23 hours ago
    When the build log collector sweeps
    Then the log sweep completed without error
    And the log of the build "older" survived the sweep
    And the log of the build "newer" survived the sweep
    And the next sweep will start from the build "older"

  # The successful-build floor, and the trim it forces. The job asks to keep 3
  # builds and at least 2 of them successful, and there are only 2 successes in
  # the last 5 builds — so honouring the floor means holding 4 builds, one more
  # than the job allowed. reapLogsOfJob resolves that by going back and reaping
  # the OLDEST of the builds it was keeping only to make up the number, never
  # one of the successes.
  #
  # Read the outcome and the rule is visible: "b5", the newest failure, keeps
  # its log while "b3", an older failure, loses its — and "b2", older still,
  # keeps its because it succeeded. This is the only scenario in the file where
  # what is reaped is not simply the oldest thing there.
  #
  # Reddened by: taking the trim from the front of candidateBuildIDsToKeep
  # rather than the back. The list is newest-first, so `candidates[i-1]` reaps
  # "b5" and spares "b3" — measured, and the failure reads exactly that way:
  # the logs the sweep deleted were [b1 b5] where "b3" was expected. No other
  # scenario in the file changes, because the trim only runs at all when the
  # successful-build floor has forced the job over its own count.
  Scenario: The successful-build floor is honoured by reaping the oldest builds kept only for the count
    Given a job that keeps its 3 most recent builds, at least 2 of them successful
    And the build "b1" failed 2 hours ago
    And the build "b2" succeeded 2 hours ago
    And the build "b3" failed 2 hours ago
    And the build "b4" succeeded 2 hours ago
    And the build "b5" failed 2 hours ago
    When the build log collector sweeps
    Then the log sweep completed without error
    And the log of the build "b1" has been reaped
    And the log of the build "b3" has been reaped
    And the log of the build "b2" survived the sweep
    And the log of the build "b4" survived the sweep
    And the log of the build "b5" survived the sweep
    And the next sweep will start from the build "b2"

  # The operator's ceiling. A pipeline author can ask for any retention they
  # like in the job config; the deployment flag is what decides. This is the
  # only scenario in the file where the number the collector uses is not the
  # number the job asked for, and if the wiring between the calculator and the
  # collector were cut the job would silently get what it asked for — which is
  # how a single pipeline fills the disk the whole deployment shares.
  #
  # Reddened by: the calculator skipping its ceiling — making the
  # `maxBuildLogsToRetain == 0 && maxDaysToRetainBuildLogs == 0` shortcut in
  # BuildLogsToRetain unconditional, so the job's own number is handed back
  # untouched. Nothing is then reaped, because the job asked to keep 10 and
  # there are only 4. Measured: it reddens this scenario alone, since every
  # other job in the file is under a deployment with no ceiling set at all.
  Scenario: The operator's maximum wins over what the job asked for
    Given a job that asks to keep its 10 most recent builds where the operator allows at most 3
    And the build "b1" failed 2 hours ago
    And the build "b2" failed 2 hours ago
    And the build "b3" failed 2 hours ago
    And the build "b4" failed 2 hours ago
    When the build log collector sweeps
    Then the log sweep completed without error
    And the log of the build "b1" has been reaped
    And the log of the build "b2" survived the sweep
    And the log of the build "b3" survived the sweep
    And the log of the build "b4" survived the sweep
    And the next sweep will start from the build "b2"

  # When every build is old enough, nothing is kept — and the cursor has to
  # survive that. The loop only ever records a cursor for a build it KEPT, so
  # after a clean sweep of everything the candidate cursor is zero. Write that
  # zero and the cursor falls back to the beginning of the job's history, and
  # every later sweep re-reads builds whose events are already gone, forever.
  #
  # Reddened by: deleting BOTH monotonicity guards — the `firstLoggedBuildID >
  # job.FirstLoggedBuildID()` comparison in reapLogsOfJob AND the
  # FirstLoggedBuildIDDecreasedError check at the top of UpdateFirstLoggedBuildID
  # in atc/db/job.go.
  #
  # That "both" is the finding, and it was measured rather than assumed:
  # deleting the collector's guard alone reddens NOTHING. The db layer refuses
  # the decrease, the collector logs the refusal, and the sweep carries on. So
  # the guard in atc/gc is a redundant second copy of a rule that is really
  # enforced one layer down — which is worth knowing before anyone reads its
  # deletion as a safe simplification, and is the sort of thing only a mutation
  # finds. The two reap assertions keep passing throughout; this scenario
  # exists for the third one.
  Scenario: A sweep that reaps every build does not send the cursor back to the beginning
    Given a job that keeps any build finished in the last day
    And the build "older" failed 30 hours ago
    And the build "newer" failed 30 hours ago
    When the build log collector sweeps
    Then the log sweep completed without error
    And the log of the build "older" has been reaped
    And the log of the build "newer" has been reaped
    And the next sweep will start from the build "older"

  # ==========================================================================
  # A history longer than one batch
  # ==========================================================================

  # The collector reads a job's builds a page at a time and decides everything
  # afterwards on the assembled list. Seven builds against a batch of five is
  # the smallest fixture that makes it read twice.
  #
  # This is the one boundary the migration lost, and this scenario is here to
  # get it back. Three of the ginkgo entries carried 7, 6 and 6 builds against
  # the same batch of 5, so they crossed it — not because any of them was about
  # paging, but as a side effect of being long. The scenarios that replaced
  # them are 3 and 4 builds and cross nothing. Measured before this scenario
  # existed: getBuildsWithPagination (atc/db/build_factory.go) only reports a
  # Newer page when a build exists past the newest one it just returned, so at
  # Limit 5 a job with 5 builds gets its whole history on the first page, and
  # `for page != nil` in reapLogsOfJob ran its body exactly once in all 24
  # scenarios this file then had.
  #
  # What the second page is FOR is visible in the outcome rather than in a call
  # count. The pages arrive oldest-first and retention counts back from the
  # newest build, so each batch is PREPENDED —
  # `append(buildsOfBatch, buildsToConsiderDeleting...)`. Get the paging wrong
  # in either direction and the two builds kept are the wrong two.
  #
  # Reddened by, both measured through a `go build -overlay` so the shared tree
  # was never edited:
  #
  #   - `page = pagination.Newer` becoming `page = nil`, which stops after the
  #     first page. The run says: expected the builds whose logs the sweep
  #     deleted to include "b4", found [b1 b2 b3]. "b4" and "b5" survive,
  #     because a history truncated at "b5" retains those two. With those two
  #     lines lifted so the run reaches the last one, the cursor comes back
  #     "b4" where this scenario says "b6".
  #     Note which lines do NOT move — "b6" and "b7" survive either way, since
  #     a build the collector never read cannot be reaped — which is what makes
  #     the cursor the sharpest of the three: it can only land on "b6" if the
  #     second page was read at all.
  #
  #   - the batches concatenated the other way round,
  #     `append(buildsToConsiderDeleting, buildsOfBatch...)`. The list is then
  #     oldest-page-first and the two builds retained are "b5" and "b4", as
  #     though they were the newest in the job. Measured: the survivors are
  #     [b4 b5], and the logs of "b6" and "b7" — the two most recent, the two
  #     anybody is actually looking at — are the ones deleted.
  #
  # Neither mutation moves any other scenario in this file, which was measured
  # by running the whole file under each: every other fixture fits in one page.
  Scenario: A job whose history is longer than one batch is read to the end of it
    Given a job that keeps its 2 most recent builds
    And the build "b1" failed 2 hours ago
    And the build "b2" failed 2 hours ago
    And the build "b3" failed 2 hours ago
    And the build "b4" failed 2 hours ago
    And the build "b5" failed 2 hours ago
    And the build "b6" failed 2 hours ago
    And the build "b7" failed 2 hours ago
    When the build log collector sweeps
    Then the log sweep completed without error
    And the log of the build "b1" has been reaped
    And the log of the build "b2" has been reaped
    And the log of the build "b3" has been reaped
    And the log of the build "b4" has been reaped
    And the log of the build "b5" has been reaped
    And the log of the build "b6" survived the sweep
    And the log of the build "b7" survived the sweep
    And the next sweep will start from the build "b6"

  # ==========================================================================
  # Which builds a sweep is allowed to touch at all
  # ==========================================================================

  # A running build is not garbage, and it does not spend a retention slot
  # either. Both halves are here: "b3" keeps its log while it runs, and "b2" —
  # which is older than "b3" — keeps its log too, because the running build
  # took none of the two slots on its way past. Reaping a running build's
  # events is worse than losing an old log: the web is streaming those events
  # to whoever is watching the build right now.
  #
  # Reddened by: deleting the `build.IsRunning()` branch. "b3" is then retained
  # in "b2"'s place and "b2" is reaped, so the assertion that fails is about a
  # build that was never running at all — which is the second half, and the one
  # a shorter fixture would have missed.
  Scenario: A running build keeps its log and does not use up a retention slot
    Given a job that keeps its 2 most recent builds
    And the build "b1" failed 2 hours ago
    And the build "b2" failed 2 hours ago
    And the build "b3" is still running
    And the build "b4" failed 2 hours ago
    When the build log collector sweeps
    Then the log sweep completed without error
    And the log of the build "b1" has been reaped
    And the log of the build "b2" survived the sweep
    And the log of the build "b3" survived the sweep
    And the log of the build "b4" survived the sweep
    And the next sweep will start from the build "b2"

  # Draining is a deployment that ships build events somewhere else before they
  # are deleted. Where one is configured, reaping an undrained build destroys
  # the only copy, so an undrained build is untouchable AND pins the cursor
  # behind it — the sweep may not advance past a build it has not been allowed
  # to reap, or the drainer's backlog is stranded on the far side.
  #
  # The two rows are the same three builds and the same policy. Only the
  # deployment differs, and with no drainer the `drained` column means nothing
  # at all: "b2" is reaped like any other build and the cursor moves past it.
  #
  # Reddened by, one per row and measured separately: making the drained check
  # unconditional (`if true` in place of `if br.drainerConfigured`) reddens the
  # second row, where "b2" then survives a deployment that has nowhere to drain
  # to. Deleting that whole block reddens the first row, where "b2" is reaped
  # out from under a drainer that had not shipped it yet.
  Scenario Outline: An undrained build is reaped only where no drainer is waiting for it — <case>
    Given a job that keeps only its most recent build
    And <deployment>
    And the build "b1" failed 2 hours ago
    And the build "b2" failed 2 hours ago
    And the build "b2" has not been drained yet
    And the build "b3" failed 2 hours ago
    When the build log collector sweeps
    Then the log sweep completed without error
    And the log of the build "b1" has been reaped
    And the log of the build "b2" <b2 fate>
    And the log of the build "b3" survived the sweep
    And the next sweep will start from the build "<cursor>"

    Examples:
      | case                              | deployment              | b2 fate            | cursor |
      | a drainer is waiting for the logs | a drainer is configured | survived the sweep | b2     |
      | no drainer to wait for            | no drainer is configured | has been reaped   | b3     |

  # `reap_time` records that a sweep has already dealt with a build. The
  # collector filters those out before it counts anything, so they neither
  # occupy a retention slot nor get offered for deletion a second time.
  #
  # The observable is thin and worth naming as such: a build with reap_time set
  # normally has no events left to delete, so the only way to see the filter at
  # work is to leave the events in place and watch them not be touched. That is
  # what the ginkgo entry does and there is nothing better available — the
  # alternative, that the reaped build eats a slot and some other build is
  # deleted in its place, cannot arise, because the already-reaped builds are
  # always the oldest and the oldest is what gets reaped anyway.
  #
  # Reddened by: deleting the `!build.ReapTime().IsZero()` filter — "b1" is
  # then listed for deletion alongside "b2" and its leftover events go.
  Scenario: A build an earlier sweep already reaped is not offered for reaping again
    Given a job that keeps only its most recent build
    And the build "b1" failed 2 hours ago
    And the build "b1" was reaped by an earlier sweep
    And the build "b2" failed 2 hours ago
    And the build "b3" failed 2 hours ago
    When the build log collector sweeps
    Then the log sweep completed without error
    And the log of the build "b2" has been reaped
    And the log of the build "b1" survived the sweep
    And the log of the build "b3" survived the sweep
    And the next sweep will start from the build "b3"

  # Pausing is how an operator says "leave this alone", and a paused pipeline
  # or job whose logs kept being deleted underneath them would make pausing
  # useless for the one thing people pause for — going and looking at what
  # happened.
  #
  # The second pipeline is what makes the row mean anything. "Nothing was
  # reaped" is also what a broken fixture, a collector that never ran, and a
  # sweep that aborted all look like. A healthy pipeline in the same database,
  # reaped in the same pass, says the sweep ran and did its work and stepped
  # over this one on purpose.
  #
  # Reddened by: deleting `if pipeline.Paused() { continue }` (first row) or
  # `if job.Paused() { continue }` (second). Neither reddens the other, which
  # is why these are two rows and not one — they are two guards, in two loops,
  # and an operator who pauses a job has not paused the pipeline.
  Scenario Outline: A paused <thing> is left alone while the rest of the deployment is reaped
    Given a job that keeps only its most recent build
    And the build "older" failed 2 hours ago
    And the build "newer" failed 2 hours ago
    And <what is paused>
    And a second pipeline whose job keeps only its most recent build
    And the build "other-older" failed 2 hours ago
    And the build "other-newer" failed 2 hours ago
    When the build log collector sweeps
    Then the log sweep completed without error
    And the log of the build "older" survived the sweep
    And the log of the build "newer" survived the sweep
    And the next sweep will start from the build "older"
    And the log of the build "other-older" has been reaped
    And the log of the build "other-newer" survived the sweep

    Examples:
      | thing    | what is paused                        |
      | pipeline | the pipeline holding that job is paused |
      | job      | the job itself is paused                |

  # DISPOSITION — two ginkgo entries are not scenarios here.
  #
  # "no eligible reap" is one running build and nothing else, asserting that
  # nothing was reaped. Its content is that a running build is not reaped,
  # which the running-build scenario above asserts alongside two builds that
  # ARE reaped, so a collector that reaped nothing at all could not pass it.
  #
  # "no builds" is a job with no builds, asserting that nothing happened. There
  # is no mutation that reddens it: the `len(buildsToConsiderDeleting) == 0`
  # early return it appears to cover can be deleted outright and the loop below
  # it does nothing on an empty slice, so the same nothing happens either way.
  # A scenario that cannot fail is not coverage.

  # ==========================================================================
  # What a sweep does when the database will not answer
  # ==========================================================================

  # Two failures that end the pass, and the reason they must: both happen
  # before the collector knows what work there is, so continuing would mean
  # reaping from an unknown fraction of the deployment and reporting a clean
  # pass. The component runner treats a successful pass as done and waits a
  # whole interval before the next one, so a swallowed error here is a cycle in
  # which no log is collected and nothing says so.
  #
  # The failure is a real one: the collector holds its lifecycle, or its
  # pipeline factory, over a connection that has been closed, which is how this
  # arrives in production when the ATC loses PostgreSQL mid-sweep. The
  # scenario's own connection is untouched, so the survivors below are read
  # from a database that can still answer. That the refusal says "closed" is
  # then a statement about passing the database's error through, not about a
  # sentinel the test supplied — a collector that invented an error of its own
  # would fail this line.
  #
  # Reddened by: `return err` becoming `return nil` after either call in
  # buildLogCollector.Run. The two survivors keep passing, because a collector
  # that gave up quietly also reaps nothing; the refusal is the assertion.
  Scenario Outline: A sweep that cannot find out what to reap is refused, not reported clean — <case>
    Given a job that keeps only its most recent build
    And the build "older" failed 2 hours ago
    And the build "newer" failed 2 hours ago
    And <fault>
    When the build log collector sweeps
    Then the log sweep was refused, saying "closed"
    And the log of the build "older" survived the sweep
    And the log of the build "newer" survived the sweep
    And the next sweep will start from the build "older"

    Examples:
      | case                                           | fault                                                    |
      | the cleanup of deleted pipelines cannot run    | the database behind the deleted-pipeline cleanup has gone away |
      | the list of pipelines cannot be read           | the database the pipeline listing reads has gone away    |

  # ...and the four that must NOT end the pass. Once the collector knows what
  # work there is, a pipeline or a job that fails is one pipeline or one job:
  # the loops log it and carry on, because a deployment where one broken
  # pipeline stops log collection for every other pipeline fills its disk over
  # a weekend.
  #
  # The ginkgo specs proved "carried on" by looking for the error in the log
  # buffer. That is a weaker claim than it looks — a collector that logged and
  # then returned would say exactly the same thing. Here the second pipeline is
  # reaped in the same pass, which is the outcome the log line was standing in
  # for, and it is asserted on every row.
  #
  # The last row is the one with a different fate, and it is the safe
  # direction: the events are gone but the cursor never moved, so the next
  # sweep re-reads builds it has already dealt with rather than advancing past
  # builds it has not. The row above it is the same rule from the other side —
  # the delete failed, so the cursor must not move either, or those events
  # would never be offered again.
  #
  # Reddened by the two `continue`s that carry these rows, measured separately.
  # Turning the one after `pipeline.Jobs()` fails into a `return` reddens the
  # first row alone, and it reddens it on the NEIGHBOUR — "other-older" is
  # never reaped, which is the assertion the log-buffer check could not make.
  # Turning the one after `reapLogsOfJob` fails into `return err` reddens the
  # other three rows, on the sweep reporting a failure that belongs to one job.
  Scenario Outline: A pipeline the database will not answer for is skipped, and the rest is still reaped — <case>
    Given a job that keeps only its most recent build
    And the build "older" failed 2 hours ago
    And the build "newer" failed 2 hours ago
    And a second pipeline whose job keeps only its most recent build
    And the build "other-older" failed 2 hours ago
    And the build "other-newer" failed 2 hours ago
    And <fault>
    When the build log collector sweeps
    Then the log sweep completed without error
    And the log of the build "older" <older fate>
    And the log of the build "newer" survived the sweep
    And the next sweep will start from the build "older"
    And the log of the build "other-older" has been reaped
    And the log of the build "other-newer" survived the sweep

    Examples:
      | case                              | fault                                                              | older fate         |
      | its jobs cannot be listed         | the database will not list the jobs of the first pipeline          | survived the sweep |
      | its build history cannot be read  | the database will not list the builds of the first pipeline's job  | survived the sweep |
      | its build events cannot be deleted | the database will not delete the build events of the first pipeline | survived the sweep |
      | its cursor cannot be advanced     | the database will not advance the log cursor of the first pipeline's job | has been reaped |

  # ==========================================================================
  # Builds that can no longer be intercepted
  # ==========================================================================

  # NEW COVERAGE, not a migration. atc/gc/build_collector_test.go does not
  # exist and never has, so the build collector — which runs on every ATC, on a
  # timer — has had no test at this layer at all. Its whole body is one
  # lifecycle call, and the sentence is worth one scenario because both
  # directions of getting it wrong are expensive.
  #
  # `interceptible` is what holds a finished build's containers open for `fly
  # intercept`. The container collector will not reclaim them while it is set,
  # so a build collector that stops marking builds leaks a container per build
  # on every worker until somebody notices the disk. Marking too widely is the
  # opposite failure and the more surprising one: a build still in flight would
  # have its containers collected out from under the step that is running in
  # them.
  #
  # The three lines before the When are the shape of this scenario, and the
  # reason for them is that `interceptible` is NOT a column only this collector
  # writes. atc/db/build.go clears it in the same UPDATE that completes any
  # build that did not succeed, so "the finished build can no longer be
  # intercepted" is a post-state two different things can produce, and on its
  # own it attributes nothing to the sweep. Read the column before as well as
  # after and the sweep is the only thing that happened in between.
  #
  # The failed build is here to hold that difference still rather than leave it
  # as a claim in a comment. It arrives at the sweep already retired, by
  # Finish's hand and not the collector's, and the scenario says so out loud.
  # It is also the answer to "why not use a failure for the main assertion":
  # not because of a grace period — this tree never gives a failed build one —
  # but because a succeeded build is the only finished build whose retirement
  # the collector can be responsible for.
  #
  # Reddened by, all three measured through a `go build -overlay` so the shared
  # tree was never edited:
  #
  #   - buildCollector.Run returning nil without calling
  #     MarkNonInterceptibleBuilds. The run says: expected the builds the sweep
  #     retired from interception to include "finished", found [failed]. This
  #     one held before the preconditions were added and still holds — and the
  #     "found" half of it is the failed build, retired by Finish rather than
  #     by the sweep, which is the difference the preconditions keep visible.
  #
  #   - Finish clearing interceptible for a SUCCEEDED build too. Every Then
  #     still passes — the finished build really is non-interceptible when the
  #     sweep is over — and the line that reddens is the precondition. The cost
  #     of not having had it was measured rather than argued: with BOTH
  #     mutations applied at once, so that the collector does nothing at all
  #     and Finish does the whole job, this scenario AS IT STOOD BEFORE THIS
  #     REPAIR ran green.
  #
  #   - Finish NOT clearing interceptible for a non-succeeded build: the
  #     "failed" precondition reddens before the sweep has run at all.
  #
  # The running build is pinned one layer down, by the `completed: true`
  # predicate in atc/db/build_factory.go, and that predicate is the only thing
  # standing between a build still in flight and having its containers taken
  # away underneath it.
  Scenario: A finished build stops being interceptible while a running one does not
    Given a collector that retires finished builds from interception
    And the build "finished" has finished successfully
    And the build "failed" has finished, failing
    And the build "running" is still in flight
    And the build "finished" can be intercepted before the sweep
    And the build "running" can be intercepted before the sweep
    And the build "failed" could not be intercepted even before the sweep
    When the collector sweeps builds that can no longer be intercepted
    Then the interception sweep completed without error
    And the build "finished" can no longer be intercepted
    And the build "running" can still be intercepted
    And the build "failed" can no longer be intercepted

  # ==========================================================================
  # The measurement
  # ==========================================================================
  #
  # 25 leaf scenarios once the four outlines are expanded, so 25 sequential
  # template-database clones. Measured on this file over three runs: 7.4 s,
  # 7.8 s and 8.1 s for the whole thing, against the ~3.9 s fixed cost
  # gc-reclamation.feature measured for the postmaster. That puts a scenario at
  # ~160 ms here rather than the ~113 ms measured there — these fixtures save
  # pipelines, jobs and up to seven builds apiece, where that file's saved
  # workers and containers. The run-to-run spread is wider than any single
  # scenario costs, so read the per-scenario figure as an order of magnitude
  # and not as a budget.
  #
  # The 27 ginkgo specs behind them are 18 DescribeTable entries, 7 Its, and 2
  # pipeline-collector specs. 25 are migrated, 2 are disposed of above with a
  # reason, and 2 scenarios are not migrations.
  #
  # On the fixtures that shrank — the drain entries went from 7 and 6 builds to
  # 3, the running-build entry from 6 to 4. Every RULE the larger fixture
  # exercised is still exercised, which is what this note used to claim and is
  # still true; it was just not the whole of what the length bought. Those
  # three entries also crossed the collector's batch of 5, and crossing it was
  # not a rule any of them was about — it was a side effect of being long, and
  # the shrink dropped it without saying so. Measured after the fact:
  # `for page != nil` in reapLogsOfJob ran its body exactly once in all 24
  # scenarios this file had before the batch-boundary scenario above was
  # written to hold that line on purpose.
  #
  # EVERY SCENARIO HAS BEEN SHOWN TO FAIL. Fifteen mutation runs over
  # atc/gc/pipeline_collector.go, atc/gc/build_log_collector.go,
  # atc/gc/build_log_retention_calculator.go, atc/gc/build_collector.go,
  # atc/db/pipeline_lifecycle.go, atc/db/job.go and atc/db/build.go redden all
  # 25, and each scenario's note above names the change that reddens it. The
  # six added by this repair were run through `go build -overlay`, which
  # substitutes a mutated copy of a file at build time, so no mutation was ever
  # written into a tree that other work was sharing.
  #
  # Four of the notes above were written wrong first and corrected by a
  # measurement. The first two were caught by their own author's runs; the last
  # two by a later audit, and those are the more useful ones, because both had
  # been read past by someone who believed them:
  #
  #   - the cursor guard in reapLogsOfJob is REDUNDANT. Deleting it reddens
  #     nothing, because UpdateFirstLoggedBuildID in atc/db/job.go refuses the
  #     decrease itself. It takes both to move the cursor backwards.
  #
  #   - running the age arm before the count arm reddens nothing either. The
  #     age arm reaps by falling through rather than by deleting, so the count
  #     arm still catches the build on the way past. What protects the most
  #     recent build is that arm's `continue`, not the order of the two.
  #
  #   - "logBatchSize is smaller than any fixture below is long, so the paging
  #     loop is exercised" was false, and so was the coverage it claimed.
  #     getBuildsWithPagination only reports a Newer page when a build exists
  #     past the newest one it just returned, so five builds against a batch of
  #     five is one page and the loop body runs once. The claim is corrected
  #     where it stood, in steps/gc_pipelines.go, and the scenario it promised
  #     now exists.
  #
  #   - "the ATC keeps a failed build interceptible for a grace period" was
  #     false in this tree: db.Build.Finish clears the column for every
  #     non-succeeded status, in the same UPDATE that completes the build. The
  #     scenario that comment was defending had no reading of the column from
  #     before the sweep, so its verdict rested on which of two writers got
  #     there first. Measured: with the collector stubbed out AND Finish
  #     clearing the column for succeeded builds too, that scenario ran green.
  #     It has preconditions now, and under the same pair it reddens on them.
