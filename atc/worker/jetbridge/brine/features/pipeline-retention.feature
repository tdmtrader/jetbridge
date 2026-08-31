Feature: Putting a pipeline to sleep, letting a build start, and a cache that outlives its worker

  Three policies from atc/db. Not the data layer — the decisions taken in it.

  This file is the first in the programme with no payoff to collect. Every
  earlier one replaced a recording double with a working one and asserted the
  round trip; atc/db has no double to replace. 31,186 lines, 1,013 specs, all
  of them already on real PostgreSQL, and in the whole package exactly two
  hand-written doubles and zero counterfeiter fakes. So nothing below is bought
  by de-faking, and the only thing that can justify a scenario here is that its
  sentence is one an operator or a pipeline author would recognise. That test
  was applied harshly: atc/db's pagination cursors, id-range boundaries and
  query shapes are good Go tests that would become worse Gherkin ones, and they
  are not here. Nine scenarios, from three sources.

  What each policy costs when it goes wrong:

    The automatic pauser turns off pipelines nobody is using any more. Too
    timid and a deployment silently schedules builds for teams that left a year
    ago; too eager and it turns off the pipeline someone set five minutes ago,
    or the one whose overnight build is still running. Its damage is quiet in
    both directions, which is why the attribution matters: `paused by
    automatic-pipeline-pauser` is the only thing that tells the person who
    finds their pipeline off that a human did not do it.

    Admission decides which builds start. It is the pipeline author's whole
    contract with `max_in_flight` and `serial_groups` — the promise that two
    deploys never run at once — and the operator's whole contract with `fly
    pause-job` and `fly pause-pipeline`. A build let through early breaks an
    invariant the author wrote the config to get; a build turned away wrongly
    stalls a queue with nothing to say why.

    Cache validity decides where a get step may read from. A resource cache is
    streamed from the worker that produced it to whichever worker needs it, and
    the streamed copy carries the ORIGIN worker's identity. Prune the origin
    and the copy is no longer evidence of anything — but a build that was
    already running when the prune happened has been reading it all along, and
    pulling it out from under that build breaks a build for a reason that has
    nothing to do with it.

  Sources: atc/db/pipeline_pauser_test.go (7 cases), job_test.go's ScheduleBuild
  Describe (15), team_test.go's FindWorkersForResourceCache Describe (9), and
  worker_resource_caches_test.go (8) — 39 in all, against 9 scenarios. None of
  them carry requirement identifiers, so there are no tags.

  NOTHING IS DELETED ON THE STRENGTH OF THIS FILE. The programme's protocol is
  per-test both-red evidence: one mutation has to redden a named ginkgo test AND
  a named scenario here. The ginkgo half was never run, so none of the 39 is
  evidenced for deletion. What is below is coverage the deployment did not have,
  not a replacement for coverage it did.

  Production mutations WERE run, six of them, and every MEASURED note below
  reports one. None touched the worktree: each mutant is a copy of a single
  production file under /tmp, compiled in with

      go build -overlay <overlay>.json -o <scratch>/.build/... ./cmd/brine-adapter-jetbridge

  and run from a `.brine` manifest outside this directory. That last part is not
  optional — `brine run --set runner.binary=...` is accepted and then silently
  ignored, so a run that seems to exonerate a mutation but was pointed at the
  mutant with `--set` was never running it at all. A scratch manifest is the
  only way to aim a run at a different adapter binary.

  Falsifiability is also a property of how the scenarios are BUILT: every one of
  them varies its discriminator inside its own database and asserts both
  outcomes, so the thing each scenario is about is the only thing that differs
  between the row that passes and the row that does not. Twelve
  deliberately-wrong variants were run the same way, and all twelve failed:
  eight with a flipped expectation; two with the discriminator taken out of the
  fixture — drop the serial group, or the max-in-flight limit, and the build
  that the real scenario says is turned away is let through, so the refusal
  there is the policy's and not the fixture's; and two asserting about a
  pipeline and a build the fixture never created, which report exactly that
  rather than passing on a vacuum.

  # ==========================================================================
  # Dormancy: the automatic pipeline pauser
  # ==========================================================================
  #
  # One thing to read the whole section with. pipeline_pauser.go has TWO
  # independent guards, and either one alone spares a pipeline:
  #
  #     p.id NOT IN (... jobs whose newest build ended recently, or that have
  #                      a build in flight ...)
  #     p.last_updated < CURRENT_DATE - <threshold>
  #
  # A pipeline saved during a test fails the second guard outright, so unless
  # the fixture backdates `last_updated` the scenario is about that guard and
  # nothing else. Three of the seven ginkgo cases are in that position — "last
  # run was 1 day ago", "last run was 10 days ago", and "pipeline with a build
  # currently running", the last of which sets `last_updated` to five days ago
  # against a ten-day threshold. Each asserts a pipeline was NOT paused, and
  # each would still pass with the entire build-age subquery deleted. Every
  # scenario below backdates deliberately, which is what puts the subquery
  # under test.

  # The central case, and the assessment's own sentence. Four claims travel
  # together because they are one decision: the pipeline goes off, the pause is
  # attributed to the pauser rather than to a person, a pipeline that is still
  # in use stays on, and the line between the two sits where the caller put it.
  #
  # "integration" never ran, and that is load-bearing rather than scenery. A
  # job with no builds at all has no end_time to compare, and the difference
  # between "nothing ran recently" and "nothing ran" is the difference between
  # pausing this pipeline and sparing it forever.
  #
  # "just-past" and "still-used" straddle the threshold, one on each side, and
  # they are a pair because one spared pipeline does not pin the interval to the
  # number the caller passed. Both are configured thirty days back, like every
  # pipeline here, so the last_updated guard spares neither of them and the
  # build-age comparison is the only thing deciding either.
  #
  # Where the edge actually is. The comparison is
  # `b.end_time > CURRENT_DATE - <n> day` — against MIDNIGHT n days ago, not
  # against a moment n days ago — and the fixture writes
  # `end_time = NOW() - make_interval(days => N)`. So a build N days old clears
  # the comparison by exactly the current time of day: spared whenever N <= n,
  # paused whenever N > n, with up to 24 hours of margin either side rather than
  # an exact edge. "Ten days ago is not more than ten days ago" was the wrong
  # reason for the right answer, and the difference matters, because it is what
  # let the interval be severed from the caller's argument unnoticed.
  #
  # Reddened by: pipeline.Pause("automatic-pipeline-pauser") losing its
  # argument — the pipeline still goes off and the attribution row fails alone.
  # Or by the subquery's build-age comparison widening to include a NULL
  # end_time, which spares "abandoned" on the strength of a job that never ran.
  #
  # And by the subquery's interval being severed from the caller's threshold in
  # EITHER direction, which is what the pair is for. MEASURED, by replacing
  # `strconv.Itoa(daysSinceLastBuild)+" day"` on pipeline_pauser.go:48 with a
  # constant, building the adapter through `go build -overlay` and running this
  # file from a scratch manifest outside the tree:
  #
  #     "9 day"    "still-used" paused                        RED
  #     "11 day"   "just-past" spared                         RED
  #     "13 day"   "just-past" spared                         RED
  #     "20 day"   "abandoned" spared (and "just-past" too)   RED
  #
  # Without "just-past" — the version the audit found — "13 day" left this whole
  # file at 9 passed, 0 failed. The prose then claimed that "hardcoding one
  # longer than the caller's threshold" pauses "still-used"; it does the
  # opposite, and a longer interval that reddens anything reddens the
  # "abandoned" line instead. Only a SHORTER interval can pause "still-used".
  Scenario: A pipeline nobody has built in fifteen days is paused, and says so
    Given the automatic pipeline pauser
    And the pipeline "abandoned" was set up 30 days ago with the jobs "unit" and "integration"
    And the job "unit" in the pipeline "abandoned" finished a build 15 days ago
    And the pipeline "just-past" was set up 30 days ago with the jobs "unit" and "integration"
    And the job "unit" in the pipeline "just-past" finished a build 11 days ago
    And the pipeline "still-used" was set up 30 days ago with the jobs "unit" and "integration"
    And the job "unit" in the pipeline "still-used" finished a build 10 days ago
    When the pauser pauses every pipeline idle for 10 days
    Then the pauser completed without error
    And the pipeline "abandoned" is paused
    And the pipeline "abandoned" was paused by "automatic-pipeline-pauser"
    And the pipeline "just-past" is paused
    And the pipeline "still-used" is not paused

  # A pipeline is idle when nothing has run RECENTLY and nothing is running
  # NOW, and the second half has never been under test. The ginkgo case for it
  # dates the pipeline's configuration five days back against a ten-day
  # threshold, so the last_updated guard spares it before the running build is
  # ever consulted: deleting `OR j.next_build_id IS NOT NULL` leaves that case
  # green.
  #
  # What it costs is a long build — a nightly, a soak, a release train — being
  # turned off underneath itself, on the strength of a last COMPLETED build
  # that is old precisely because the current one has not finished yet.
  #
  # Both pipelines here last finished twenty days ago and both were configured
  # thirty days ago, so the running build is the only difference between them.
  #
  # Reddened by: deleting `OR j.next_build_id IS NOT NULL` from the subquery.
  Scenario: A pipeline with a build still running is not idle, however old its last finished build
    Given the automatic pipeline pauser
    And the pipeline "abandoned" was set up 30 days ago with the jobs "unit" and "integration"
    And the job "unit" in the pipeline "abandoned" finished a build 20 days ago
    And the pipeline "long-running" was set up 30 days ago with the jobs "unit" and "integration"
    And the job "unit" in the pipeline "long-running" finished a build 20 days ago
    And the job "integration" in the pipeline "long-running" has a build running right now
    When the pauser pauses every pipeline idle for 10 days
    Then the pauser completed without error
    And the pipeline "abandoned" is paused
    And the pipeline "long-running" is not paused

  # The other guard, and the one that reads backwards until you see what it is
  # for. A pipeline that has NEVER run looks exactly like a pipeline nobody
  # uses — no job has a completed build, none has one in flight — so on the
  # build-age subquery alone, `fly set-pipeline` would be followed immediately
  # by the pauser turning the new pipeline off before anyone could trigger it.
  # The configuration date is the only thing standing between a new pipeline
  # and that.
  #
  # "abandoned" is here because "just-set is not paused" is an absence, and an
  # absence passes on a database where the fixture inserted nothing.
  #
  # Reddened by: deleting the `p.last_updated < CURRENT_DATE - ?` clause.
  Scenario: A pipeline set today is left alone even though nothing has ever run in it
    Given the automatic pipeline pauser
    And the pipeline "abandoned" was set up 30 days ago with the jobs "unit" and "integration"
    And the job "unit" in the pipeline "abandoned" finished a build 20 days ago
    And the pipeline "just-set" was set up just now with the jobs "unit" and "integration"
    When the pauser pauses every pipeline idle for 10 days
    Then the pauser completed without error
    And the pipeline "abandoned" is paused
    And the pipeline "just-set" is not paused

  # ==========================================================================
  # Admission: which builds the scheduler lets start
  # ==========================================================================
  #
  # Every scenario in this section asks the same question twice with one thing
  # changed in between, and that is deliberate. A refusal is the easiest
  # assertion in this repository to fake: ScheduleBuild returns (false, nil) for
  # a paused job, a full queue, a build that does not exist, and a fixture that
  # quietly failed to insert anything. Asking again after the ONE change the
  # scenario is about, and getting a different answer, is what makes the refusal
  # mean the thing the sentence says it means.

  # The assessment's sentence, with the clause that makes it a claim: "until one
  # finishes". The ginkgo suite has both halves — two builds running, refused;
  # one build running, admitted — but in separate contexts over separate
  # databases, so neither half witnesses the other, and a `>=` that had become
  # a `>` would need both cases read side by side to notice.
  #
  # The last line is a second fact. The answer and the row are not the same
  # thing: a scheduler that returned true without setting `scheduled` would
  # leave the build in the pending queue forever while telling the caller it
  # had started.
  #
  # Reddened by: `len(builds) >= j.maxInFlight` becoming `>`, which admits
  # "third" on the first ask. Or by getRunningBuildsBySerialGroup dropping
  # `b.completed = false`, so the finished build keeps its slot and "third" is
  # refused on the second ask too.
  Scenario: A job that allows two builds at a time lets a third start only when one finishes
    Given a scheduler deciding which builds may start
    And the job "deploy" allows 2 builds at a time
    And the build "first" of "deploy" is running
    And the build "second" of "deploy" is running
    And the build "third" of "deploy" is waiting
    When the scheduler asks whether "third" may start, then "first" finishes and it asks again
    Then the build "third" was turned away the first time it asked
    And the build "third" was let through the second time it asked
    And the build "third" is marked scheduled

  # A serial group is the same slot shared between DIFFERENT jobs, and that is
  # the whole reason it exists: "never deploy to staging while a production
  # deploy is going out" is not something max_in_flight on either job can say.
  # The limit here is one, because a job with serial groups has a max-in-flight
  # of one whatever else its config asks for.
  #
  # Reddened by: getRunningBuildsBySerialGroup counting only the scheduling
  # job's own builds — replacing the jobs_serial_groups join with a predicate on
  # j.id. "integration" is then admitted while "unit" is still running, which is
  # the promise the config was written to get.
  Scenario: Two jobs sharing a serial group share one slot
    Given a scheduler deciding which builds may start
    And the jobs "unit" and "integration" share the serial group "deploy"
    And the build "running-unit" of "unit" is running
    And the build "queued-integration" of "integration" is waiting
    When the scheduler asks whether "queued-integration" may start, then "running-unit" finishes and it asks again
    Then the build "queued-integration" was turned away the first time it asked
    And the build "queued-integration" was let through the second time it asked
    And the build "queued-integration" is marked scheduled

  # Nothing is running in this scenario at all. Both builds are queued, the slot
  # is free, and "newer" is still refused — because a serial group is a QUEUE
  # and not just a counter, and the build at the front of it is the one that
  # goes. Take the ordering away and a serial group stops being fair: the build
  # the scheduler happens to reach first wins, which over a busy group is the
  # newest one, and the oldest can wait indefinitely.
  #
  # Pausing "unit" is what proves the refusal was about the queue. It is also a
  # behaviour of its own, and one nobody has asserted: a paused job's queued
  # build is taken OUT of its serial group's queue, so pausing a job to stop it
  # deploying does not also stop everything that shares its group. If it did,
  # `fly pause-job` on one member would quietly wedge the whole group.
  #
  # Reddened by: isMaxInFlightReached losing its
  # `nextMostPendingBuild.ID() != buildID` comparison, which lets "newer"
  # through on the first ask; or by getNextPendingBuildBySerialGroup's
  # `ORDER BY COALESCE(rerun_of, id) ASC, id ASC` reversing, which puts "newer"
  # at the head and does the same.
  #
  # And, separately, by that query losing `j.paused = false`, which leaves
  # "unit" at the head of the queue after it has been paused and keeps "newer"
  # waiting on a job that will never run. MEASURED, by deleting that predicate
  # from getNextPendingBuildBySerialGroup (job.go:1121), building through
  # `go build -overlay` and running this scenario from a scratch manifest: it
  # reddens the second line with `expected the build "newer" to be let through
  # the second time it asked, but the scheduler turned it away`.
  #
  # An earlier version of this note cited a different probe — pausing
  # "integration" rather than "unit" in a scratch variant — as the measurement
  # for that predicate. It is not one. "integration" owns "newer", so pausing it
  # makes isPipelineOrJobPaused return true at `if j.paused` (job.go:1391) and
  # the second ask is refused before isMaxInFlightReached is called at all. The
  # probe showed the sentence CAN fail; it said nothing about the queue.
  Scenario: The oldest queued build in a serial group goes first, and pausing its job lets the rest move
    Given a scheduler deciding which builds may start
    And the jobs "unit" and "integration" share the serial group "deploy"
    And the build "older" of "unit" is waiting
    And the build "newer" of "integration" is waiting
    When the scheduler asks whether "newer" may start, then the job "unit" is paused and it asks again
    Then the build "newer" was turned away the first time it asked
    And the build "newer" was let through the second time it asked

  # The two arms of one function. isPipelineOrJobPaused is a single guard with a
  # job branch and a pipeline branch, and both are asserted here for the same
  # reason gc-reclamation.feature sweeps containers and volumes together: they
  # are one rule, and a scenario that exercised one would leave the other free
  # to drift.
  #
  # "audit-1" is the witness both refusals need. Without a build that WAS let
  # through in this same database, "turned away" is satisfied by a fixture that
  # created nothing — ScheduleBuild answers false for a build that does not
  # exist, and never reaches either guard to do it.
  #
  # Reddened by: the `if j.paused` early return going, which lets "deploy-1"
  # through. Or the pipelines lookup beneath it going, which lets "audit-2"
  # through after the pipeline has been paused.
  Scenario: Neither a paused job nor a paused pipeline lets a build start
    Given a scheduler deciding which builds may start
    And the jobs "deploy" and "audit" have no limit of their own
    And the job "deploy" is paused
    And the build "deploy-1" of "deploy" is waiting
    And the build "audit-1" of "audit" is waiting
    And the build "audit-2" of "audit" is waiting
    When the scheduler asks whether "deploy-1" and "audit-1" may start, then the pipeline is paused and it asks about "audit-2"
    Then the build "deploy-1" was turned away
    And the build "audit-1" was let through
    And the build "audit-2" was turned away
    And the build "deploy-1" is not marked scheduled
    And the build "audit-1" is marked scheduled

  # DISPOSITION — job_test.go's remaining ScheduleBuild contexts stay in Go and
  # are not restated here. "inputs determined as false", "another build within
  # the serial group is scheduled first", "created earlier", "other succeeded
  # builds within the same serial group" and "multiple serial groups" are five
  # readings of one query — getNextPendingBuildBySerialGroup, which decides both
  # who is in the queue and who is at the head of it — and that query is the
  # scenario above, differing only in how the queue was arranged. Writing five Gherkin scenarios whose Givens are permutations of a
  # creation order would be a worse table than the Go one, and the programme's
  # rules forbid it. "when the build does not exist" asserts that ScheduleBuild
  # errors on a deleted row; that is a caller bug, not a policy, and it has no
  # sentence.

  # ==========================================================================
  # A resource cache that outlives the worker it came from
  # ==========================================================================

  # The assessment's third sentence. A cache produced on worker-1 and streamed
  # to worker-2 keeps worker-1's identity on worker-2 — that is what makes it a
  # cache of a particular thing rather than a directory of bytes — so pruning
  # worker-1 invalidates a copy sitting on a worker that is perfectly healthy.
  #
  # And that has to be true WITHOUT breaking the build that is already reading
  # it. A build that started before the prune has been using this copy all
  # along; the bytes did not change when a different worker went away. So the
  # rule is dated rather than absolute: invalid from the moment of the prune,
  # and builds older than that moment keep it.
  #
  # Both readers are here, because there are two of them implementing one rule.
  # team.FindWorkersForResourceCache is what placement asks — which workers
  # could serve this — and volumeRepository.FindResourceCacheVolume is what the
  # get step itself asks about a particular worker. Either one drifting alone
  # would mean a build sent to a worker whose copy it is then refused, or the
  # reverse.
  #
  # The survival line separates two very different behaviours. "Finds no cache"
  # has to mean the rule declined to use the copy, not that the prune deleted
  # it — and there are two ways to delete it. The volume row can go, and the
  # volume row can stay while its worker_resource_cache_id is nulled or
  # repointed, which unhooks a perfectly good copy of a large artifact and
  # leaves it as garbage waiting for the collector. So the line reads the row
  # directly and asserts both: the handle still resolves, and it still points
  # at the worker_resource_caches row the streamed copy was registered under.
  # That second half is team_test.go's `Expect(v.WorkerResourceCacheID()).
  # To(Equal(uwrc2.ID))`, and an earlier version of this scenario dropped it.
  #
  # It is asserted FIRST, and the ordering is part of the assertion. brine stops
  # a scenario at its first failing step; asserted last, a prune that unhooked
  # the volume reddens "finds the cache on worker-2" three lines earlier and the
  # survival line is never reached, so it could not be shown to do anything.
  #
  # Reddened by: team.FindWorkersForResourceCache losing its
  # `invalid_since > to_timestamp(?)` arm, which strands the build that was
  # already running. Or that arm becoming unconditional, which hands the copy to
  # a build that started after the prune. The volume half is reddened one layer
  # down, by worker_resource_cache.find's comparison of invalid_since against
  # the build's start time.
  #
  # The survival line is reddened by a prune that unhooks what it invalidated
  # instead of leaving it for the collector. MEASURED, by appending to
  # worker.Delete() (atc/db/worker.go:119)
  #
  #     UPDATE volumes SET worker_resource_cache_id = NULL
  #      WHERE worker_resource_cache_id IN (
  #            SELECT id FROM worker_resource_caches
  #             WHERE worker_base_resource_type_id IS NULL)
  #
  # built through `go build -overlay` and run from a scratch manifest outside
  # the tree. It fails with `the volume on "worker-2" is still present but no
  # longer holds the cache: its worker_resource_cache_id is 0, and the streamed
  # copy was registered under <id>`. The version of this line the audit found
  # asked only whether FindVolume still resolved the handle, which that mutation
  # leaves true — and with the line asserted last, the scenario reported the
  # earlier lookup instead and said nothing about the row.
  #
  # WHAT THIS DOES NOT CATCH. Each worker in this fixture holds exactly one
  # cache volume, so nothing can make FindResourceCacheVolume return a DIFFERENT
  # volume on worker-2: the handle comparison on the two "finds the cache" lines
  # is parity with team_test.go rather than a discriminator here. Restoring it
  # was still right — it is what the sentence says — but a scenario that needed
  # it to fail would have to put a second, unrelated volume on worker-2, and
  # getVolume has no ORDER BY, so which one came back would be up to the
  # planner. That is a flaky test, and it is not written.
  Scenario: A cache whose origin worker was pruned is still good for the builds that were already running
    Given the resource cache "golang-1.22" produced on the worker "worker-1"
    And the cache streamed to the worker "worker-2"
    And the worker "worker-1" is pruned
    When a build that started before the prune and a build that started after both look for a worker holding the cache
    Then the volume holding the cache on "worker-2" is still there
    And a build that started before the prune is offered the worker "worker-2"
    And a build that started before the prune finds the cache on the worker "worker-2"
    And a build that started after the prune is offered no worker at all
    And a build that started after the prune finds no cache on the worker "worker-2"

  # The complement of gc-reclamation.feature's "stalled, not disposable". There,
  # a worker that stopped heartbeating keeps its row, because it may only be
  # having a bad minute and throwing it away would discard its whole inventory.
  # Here is the other half of that bargain: keeping the row does not mean
  # keeping the worker in service. Its cache is valid, its copy is on disk, and
  # it is still not offered — because a build sent to a worker that is not
  # answering does not start.
  #
  # worker-1 is running and holds the same cache, so "not offered worker-2" is
  # a statement about worker-2 rather than about a lookup that found nothing.
  #
  # Reddened by: dropping `w.state = running` from
  # team.FindWorkersForResourceCache.
  Scenario: A stalled worker is not offered as a source, even though its cache is still valid
    Given the resource cache "golang-1.22" produced on the worker "worker-1"
    And the cache streamed to the worker "worker-2"
    And the worker "worker-2" has stopped heartbeating and stalled
    When a build looks for a worker holding the cache
    Then the build is offered the worker "worker-1"
    And the build is not offered the worker "worker-2"

  # DISPOSITION — worker_resource_caches_test.go's FindOrCreate cases stay in
  # Go. Their assertion is the second return value of a two-value function:
  # whether the row the caller found was initialized from the source worker it
  # named. Nothing a build or an operator can experience differs on it — the
  # single caller, InitializeStreamedResourceCache, uses it to decide whether to
  # commit, and what it decides is already the subject of the two scenarios
  # above. Migrating them would mean writing a sentence about a boolean.

  # ==========================================================================
  # The measurement
  # ==========================================================================
  #
  # 9 scenarios, so 9 template-database clones. Measured on this file rather
  # than reasoned about (n=3 each, this machine, with several other suites
  # running alongside — which is most of why these numbers are noisier than
  # gc-reclamation.feature's). Re-measured after the repairs, because the first
  # scenario gained three steps and a third pipeline:
  #
  #   this file, 1 scenario   (:142)    4,586 ms mean  (4,683 / 4,587 / 4,487)
  #   this file, 9 scenarios            6,245 ms mean  (6,107 / 5,759 / 6,869)
  #   -> marginal cost of a scenario    ~207 ms
  #
  # Read that as a range, not a number. The same file measured ~105 ms per
  # scenario before the repairs, and the difference is machine load rather than
  # anything in the file: the 9-scenario runs spread 1.1 s between best and
  # worst here, which is most of the gap. What the two measurements agree on is
  # the shape — a fixed ~4.5 s to stand up Postgres and clone once, and
  # something on the order of a tenth to a fifth of a second per scenario after
  # that, so the whole file is under two seconds of database time.
  #
  # The comparison that matters here is not clone-for-clone, though, because
  # nothing was deleted: this file's 9 clones are ADDED to atc/db's 1,013
  # specs, which already clone a template database each. That is the honest
  # shape of a migration into a package with no doubles left to remove — the
  # coverage is new, the Go suite is untouched, and the cost is the sum rather
  # than the difference.
